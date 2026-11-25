package panel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"githubarchive/internal/model"
)

const maxHistoryOutput = 2 << 20
const maxFileBytes = 1 << 20
const maxTreeEntries = 500

// gitReader runs local, read-only commands with ambient Git configuration and
// tracing disabled. No authorization token is required for local objects.
func gitReader(ctx context.Context, mirror string, args ...string) *exec.Cmd {
	a := append([]string{"--git-dir=" + mirror}, args...)
	cmd := exec.CommandContext(ctx, "git", a...)
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1")
	cmd.Stderr = io.Discard
	return cmd
}

type cappedOutput struct {
	bytes.Buffer
	max int
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	if len(p) > b.max-b.Len() {
		return 0, errors.New("git output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func localOutput(ctx context.Context, mirror string, max int, args ...string) ([]byte, error) {
	cmd := gitReader(ctx, mirror, args...)
	out := &cappedOutput{max: max}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// mirrorDir first resolves through os.Root to prohibit symlinks escaping the
// archive; the final resolved path is checked again before Git opens it.
func (p *Server) mirrorDir(r *http.Request) (string, error) {
	repo := model.Repository{Owner: r.URL.Query().Get("owner"), Name: r.URL.Query().Get("name")}
	dir, err := p.mirrorDirFor(repo)
	if err == nil || !model.ValidSegment(repo.Owner) || !model.ValidSegment(repo.Name) {
		return dir, err
	}
	owner, name := p.canonicalRepository(r, repo.Owner, repo.Name)
	if owner == repo.Owner && name == repo.Name {
		return "", err
	}
	return p.mirrorDirFor(model.Repository{Owner: owner, Name: name})
}

func (p *Server) mirrorDirFor(repo model.Repository) (string, error) {
	absolute, err := repo.MirrorPath(p.root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(p.root, absolute)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		return "", errors.New("not a mirror directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(p.root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	inside, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) || filepath.IsAbs(inside) {
		return "", errors.New("mirror outside archive")
	}
	return resolved, nil
}

func validObjectID(s string) bool {
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
func repoPath(s string) bool {
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// reachableCommit accepts only actual commit objects traversable from a local
// ref. rev-list is scanned rather than materialized, regardless of repo size.
func reachableCommit(ctx context.Context, mirror, sha string) bool {
	if !validObjectID(sha) {
		return false
	}
	typ, err := localOutput(ctx, mirror, 128, "cat-file", "-t", sha)
	if err != nil || strings.TrimSpace(string(typ)) != "commit" {
		return false
	}
	cmd := gitReader(ctx, mirror, "rev-list", "--all")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	if err = cmd.Start(); err != nil {
		return false
	}
	scanner := bufio.NewScanner(pipe)
	found := false
	for scanner.Scan() {
		if scanner.Text() == sha {
			found = true
			break
		}
	}
	if found {
		_ = cmd.Process.Kill()
	}
	_ = pipe.Close()
	_ = cmd.Wait()
	return found && ctx.Err() == nil
}
func (p *Server) targetCommit(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	m, err := p.mirrorDir(r)
	if err != nil {
		http.Error(w, "mirror not found", 404)
		return "", "", false
	}
	sha := r.URL.Query().Get("sha")
	if !validObjectID(sha) {
		http.Error(w, "invalid commit", 400)
		return "", "", false
	}
	if !reachableCommit(r.Context(), m, sha) {
		http.Error(w, "commit not found", 404)
		return "", "", false
	}
	return m, sha, true
}

type historyCommit struct {
	SHA              string          `json:"sha"`
	Parents          []string        `json:"parents"`
	Timestamp        string          `json:"timestamp"`
	Subject          string          `json:"subject"`
	Author           string          `json:"author"`
	Refs             []string        `json:"refs"`
	RefsTruncated    bool            `json:"refs_truncated,omitempty"`
	Changes          []historyChange `json:"changes,omitempty"`
	ChangesTruncated bool            `json:"changes_truncated,omitempty"`
}

func (p *Server) repositoryHistory(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	m, err := p.mirrorDir(r)
	if err != nil {
		http.Error(w, "mirror not found", 404)
		return
	}
	limit, offset := paging(r)
	args := []string{"log", "--topo-order", "--no-show-signature", "--format=%H%x00%P%x00%aI%x00%an%x00%s", "--max-count=" + strconv.Itoa(limit+1), "--skip=" + strconv.Itoa(offset)}
	tip, compare := r.URL.Query().Get("tip"), r.URL.Query().Get("compare")
	if tip == "" && compare == "" {
		args = append(args, "--all")
	} else {
		if !validObjectID(tip) || (compare != "" && !validObjectID(compare)) {
			http.Error(w, "invalid history tip", http.StatusBadRequest)
			return
		}
		for _, sha := range []string{tip, compare} {
			if sha != "" {
				if !reachableCommit(r.Context(), m, sha) {
					http.Error(w, "history tip not found", http.StatusNotFound)
					return
				}
				args = append(args, sha)
			}
		}
	}
	out, err := localOutput(r.Context(), m, maxHistoryOutput, append(args, "--")...)
	if err != nil {
		http.Error(w, "history unavailable", 500)
		return
	}
	commits := []historyCommit{}
	for _, line := range bytes.Split(bytes.TrimSuffix(out, []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		fields := bytes.Split(line, []byte{0})
		if len(fields) != 5 {
			http.Error(w, "history unavailable", 500)
			return
		}
		parents := strings.Fields(string(fields[1]))
		if parents == nil {
			parents = []string{}
		}
		commits = append(commits, historyCommit{SHA: string(fields[0]), Parents: parents, Timestamp: string(fields[2]), Author: string(fields[3]), Subject: string(fields[4]), Refs: []string{}})
	}
	more := len(commits) > limit
	if more {
		commits = commits[:limit]
	}
	indices := map[string]int{}
	for i := range commits {
		indices[commits[i].SHA] = i
	}
	// Annotate only this page; refs are scanned incrementally, not retained in full.
	cmd := gitReader(r.Context(), m, "for-each-ref", "--format=%(objectname)%00%(refname)%00%(*objectname)")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "history unavailable", 500)
		return
	}
	if err = cmd.Start(); err != nil {
		http.Error(w, "history unavailable", 500)
		return
	}
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		f := strings.Split(scanner.Text(), "\x00")
		if len(f) != 3 {
			continue
		}
		i, ok := indices[f[0]]
		if !ok {
			i, ok = indices[f[2]]
		}
		if !ok {
			continue
		}
		c := &commits[i]
		if len(c.Refs) < 32 {
			c.Refs = append(c.Refs, f[1])
			continue
		}
		c.RefsTruncated = true
		// Keep the retention pin visible even if many branch/tag names point
		// at the same commit.
		if strings.HasPrefix(f[1], "refs/keep/") {
			for j := len(c.Refs) - 1; j >= 0; j-- {
				if !strings.HasPrefix(c.Refs[j], "refs/keep/") {
					c.Refs[j] = f[1]
					break
				}
			}
		}
	}
	scanErr := scanner.Err()
	_ = pipe.Close()
	err = cmd.Wait()
	if scanErr != nil || err != nil {
		http.Error(w, "history unavailable", 500)
		return
	}
	if err := p.annotateHistory(r, commits, indices); err != nil {
		http.Error(w, "ref changes unavailable", http.StatusInternalServerError)
		return
	}
	jsonReply(w, struct {
		Commits []historyCommit `json:"commits"`
		HasMore bool            `json:"has_more"`
	}{commits, more})
}
