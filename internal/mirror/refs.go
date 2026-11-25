package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"githubarchive/internal/model"
)

// ReadSnapshot reads the last successfully committed remote ref snapshot.
func ReadSnapshot(repoDir string) (map[string]string, error) {
	f, err := os.Open(filepath.Join(repoDir, "ref-snapshot"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var refs map[string]string
	if err := json.NewDecoder(f).Decode(&refs); err != nil {
		return nil, fmt.Errorf("read ref snapshot: %w", err)
	}
	return refs, nil
}

// WriteSnapshot atomically replaces the snapshot only after the database commit.
func WriteSnapshot(repoDir string, snapshot map[string]string) error {
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(repoDir, ".ref-snapshot-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = json.NewEncoder(f).Encode(snapshot); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(repoDir, "ref-snapshot"))
}

// Diff compares the previous snapshot with the live remote, not stale local
// branches, and pins every replaced or deleted tip before returning.
func Diff(ctx context.Context, mirrorPath, remote, token string, repo model.Repository, previous map[string]string) ([]model.RefChange, map[string]string, error) {
	if err := ValidateGitHubRemote(remote); err != nil {
		return nil, nil, err
	}
	return diffRemote(ctx, mirrorPath, remote, token, repo, previous)
}

func diffRemote(ctx context.Context, mirrorPath, remote, token string, repo model.Repository, previous map[string]string) ([]model.RefChange, map[string]string, error) {
	parser := &refStream{refs: make(map[string]string)}
	if err := runGit(ctx, mirrorPath, token, parser, "ls-remote", "--heads", "--tags", "--refs", remote); err != nil {
		return nil, nil, err
	}
	if len(parser.pending) != 0 {
		return nil, nil, fmt.Errorf("incomplete git ls-remote response")
	}
	current := parser.refs
	// The remote can move between fetch and ls-remote. Never advance the
	// snapshot to an object that was not fetched; retry next poll. One batch
	// process checks all objects without a Git process per ref.
	if err := checkRefObjects(ctx, mirrorPath, token, current); err != nil {
		return nil, nil, fmt.Errorf("remote ref object is not available locally: %w", err)
	}
	keys := make(map[string]struct{}, len(previous)+len(current))
	for k := range previous {
		if strings.HasPrefix(k, "refs/heads/") || strings.HasPrefix(k, "refs/tags/") {
			keys[k] = struct{}{}
		}
	}
	for k := range current {
		keys[k] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	var changes []model.RefChange
	for _, ref := range ordered {
		old, next := previous[ref], current[ref]
		if old == next {
			continue
		}
		if old != "" && !objectID(old) {
			return nil, nil, fmt.Errorf("invalid object ID in ref snapshot")
		}
		kind := "create"
		if old != "" {
			// The validated object ID cannot be interpreted as an option.
			if err := runGit(ctx, mirrorPath, token, nil, "update-ref", "refs/keep/"+old, old); err != nil {
				return nil, nil, err
			}
			kind = "delete"
			if next != "" {
				kind = "force_push"
				if runGit(ctx, mirrorPath, token, nil, "merge-base", "--is-ancestor", old, next) == nil {
					kind = "fast_forward"
				}
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
			}
		}
		changes = append(changes, model.RefChange{Owner: repo.Owner, Name: repo.Name, RefName: ref, OldSHA: old, NewSHA: next, Kind: kind})
	}
	return changes, current, nil
}

type refIDReader struct {
	next    func() (string, bool)
	pending string
}

func (r *refIDReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.pending == "" {
		id, ok := r.next()
		if !ok {
			return 0, io.EOF
		}
		r.pending = id + "\n"
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

type objectCheckWriter struct {
	pending []byte
	count   int
}

func (w *objectCheckWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) != 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			i = len(p)
		}
		if len(w.pending)+i > 8192 {
			return 0, fmt.Errorf("object check line too long")
		}
		w.pending = append(w.pending, p[:i]...)
		if i == len(p) {
			break
		}
		fields := strings.Fields(string(w.pending))
		if len(fields) != 2 || !objectID(fields[0]) || (fields[1] != "commit" && fields[1] != "tree" && fields[1] != "blob" && fields[1] != "tag") {
			return 0, fmt.Errorf("object missing or invalid")
		}
		w.count++
		w.pending = w.pending[:0]
		p = p[i+1:]
	}
	return n, nil
}

func checkRefObjects(ctx context.Context, mirrorPath, token string, refs map[string]string) error {
	if len(refs) == 0 {
		return nil
	}
	seq := func(yield func(string) bool) {
		for _, id := range refs {
			if !yield(id) {
				return
			}
		}
	}
	next, stop := iter.Pull(seq)
	defer stop()
	input := &refIDReader{next: next}
	output := &objectCheckWriter{}
	if err := runGitInput(ctx, mirrorPath, token, input, output, "cat-file", "--batch-check=%(objectname) %(objecttype)"); err != nil {
		return err
	}
	if len(output.pending) != 0 || output.count != len(refs) {
		return fmt.Errorf("incomplete object check")
	}
	return nil
}

// refStream parses output as git writes it, retaining no complete output copy.
type refStream struct {
	refs    map[string]string
	pending []byte
}

func (r *refStream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) != 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			i = len(p)
		}
		if len(r.pending)+i > 8192 {
			return 0, fmt.Errorf("git ref line too long")
		}
		r.pending = append(r.pending, p[:i]...)
		if i == len(p) {
			break
		}
		fields := strings.Fields(string(r.pending))
		if len(fields) != 2 || !objectID(fields[0]) {
			return 0, fmt.Errorf("malformed git ls-remote response")
		}
		if strings.HasPrefix(fields[1], "refs/heads/") || strings.HasPrefix(fields[1], "refs/tags/") {
			r.refs[fields[1]] = fields[0]
		}
		r.pending = r.pending[:0]
		p = p[i+1:]
	}
	return n, nil
}

func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
