package panel

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

type treeEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size *int64 `json:"size,omitempty"`
}

func splitNUL(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (p *Server) repositoryTree(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	m, sha, ok := p.targetCommit(w, r)
	if !ok {
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "." {
		dir = ""
	}
	if !repoPath(dir) {
		http.Error(w, "invalid path", 400)
		return
	}
	object := sha
	offset, err := queryOffset(r)
	if err != nil {
		http.Error(w, "invalid directory offset", http.StatusBadRequest)
		return
	}
	if dir != "" {
		object += ":" + dir
	}
	cmd := gitReader(r.Context(), m, "ls-tree", "-z", "--long", object)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "tree unavailable", 500)
		return
	}
	if err = cmd.Start(); err != nil {
		http.Error(w, "tree unavailable", 500)
		return
	}
	s := bufio.NewScanner(pipe)
	s.Split(splitNUL)
	s.Buffer(make([]byte, 4096), 256<<10)
	entries := []treeEntry{}
	truncated := false
	malformed := false
	for s.Scan() {
		if offset > 0 {
			offset--
			continue
		}
		if len(entries) == maxTreeEntries {
			truncated = true
			break
		}
		header, name, ok := strings.Cut(s.Text(), "\t")
		f := strings.Fields(header)
		if !ok || len(f) != 4 || name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
			malformed = true
			break
		}
		entry := treeEntry{Name: name, Path: path.Join(dir, name), Type: f[1], SHA: f[2]}
		if f[3] != "-" {
			n, e := strconv.ParseInt(f[3], 10, 64)
			if e != nil {
				malformed = true
				break
			}
			entry.Size = &n
		}
		entries = append(entries, entry)
	}
	scanErr := s.Err()
	if truncated || malformed || scanErr != nil {
		_ = cmd.Process.Kill()
	}
	_ = pipe.Close()
	waitErr := cmd.Wait()
	if malformed || scanErr != nil || (!truncated && waitErr != nil) || r.Context().Err() != nil {
		http.Error(w, "tree unavailable", 404)
		return
	}
	jsonReply(w, struct {
		Entries   []treeEntry `json:"entries"`
		Truncated bool        `json:"truncated"`
	}{entries, truncated})
}

func queryOffset(r *http.Request) (int64, error) {
	value := r.URL.Query().Get("offset")
	if value == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(value, 10, 63)
	return int64(n), err
}

func (p *Server) repositoryFile(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	m, sha, ok := p.targetCommit(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("path")
	if name == "" || !repoPath(name) {
		http.Error(w, "invalid path", 400)
		return
	}
	object := sha + ":" + name
	typ, err := localOutput(r.Context(), m, 128, "cat-file", "-t", object)
	if err != nil || strings.TrimSpace(string(typ)) != "blob" {
		http.Error(w, "file not found", 404)
		return
	}
	sizeText, err := localOutput(r.Context(), m, 128, "cat-file", "-s", object)
	if err != nil {
		http.Error(w, "file not found", 404)
		return
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeText)), 10, 64)
	if err != nil || size < 0 {
		http.Error(w, "file unavailable", 500)
		return
	}
	if size > maxFileBytes && !r.URL.Query().Has("offset") {
		http.Error(w, "file exceeds 1 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	offset, err := queryOffset(r)
	if err != nil || offset > size {
		http.Error(w, "invalid file offset", http.StatusBadRequest)
		return
	}
	const hexPageBytes = 4096
	forceBinary := r.URL.Query().Has("offset") && r.URL.Query().Get("binary") == "1"
	pageLimit := int64(maxFileBytes)
	if forceBinary {
		pageLimit = hexPageBytes
	}
	data, err := readBlobPage(r.Context(), m, object, offset, size, pageLimit)
	if err != nil {
		http.Error(w, "file unavailable", 500)
		return
	}
	// At a page boundary the final UTF-8 character can straddle two reads.
	// Resume at the beginning of that character so text previews join exactly.
	if !forceBinary && offset+int64(len(data)) < size {
		i := len(data) - 1
		for i > 0 && len(data)-i < utf8.UTFMax && !utf8.RuneStart(data[i]) {
			i--
		}
		if utf8.RuneStart(data[i]) && !utf8.FullRune(data[i:]) {
			data = data[:i]
		}
	}
	encoding := "utf-8"
	var content string
	if forceBinary || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		encoding = "base64"
		if r.URL.Query().Has("offset") {
			data = data[:min(len(data), hexPageBytes)]
		}
		content = base64.StdEncoding.EncodeToString(data)
	} else {
		content = string(data)
	}
	next := offset + int64(len(data))
	if next == size {
		next = 0
	}
	jsonReply(w, struct {
		Content    string `json:"content"`
		Encoding   string `json:"encoding"`
		Size       int64  `json:"size"`
		NextOffset int64  `json:"next_offset,omitempty"`
	}{content, encoding, size, next})
}

// Stream from Git so the memory required for a preview is independent of the
// blob size. Close the process as soon as the requested page is available.
func readBlobPage(ctx context.Context, mirror, object string, offset, size, limit int64) ([]byte, error) {
	cmd := gitReader(ctx, mirror, "cat-file", "blob", object)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	data := make([]byte, min(limit, size-offset))
	if _, err = io.CopyN(io.Discard, out, offset); err == nil {
		_, err = io.ReadFull(out, data)
	}
	partial := offset+int64(len(data)) < size
	if partial || err != nil {
		_ = cmd.Process.Kill()
	}
	_ = out.Close()
	waitErr := cmd.Wait()
	if err != nil {
		return nil, err
	}
	if !partial && waitErr != nil {
		return nil, waitErr
	}
	return data, ctx.Err()
}

func (p *Server) repositorySnapshot(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	m, sha, ok := p.targetCommit(w, r)
	if !ok {
		return
	}
	// Validate all tree paths before writing any response; do not allow ZIP
	// names that can escape an extraction directory on any platform.
	// archive/zip retains the ZIP directory in memory, so cap its entry count
	// and aggregate names before the response begins.
	count, nameBytes := 0, 0
	if err := walkSnapshotTree(r.Context(), m, sha, func(e snapshotEntry) error {
		count++
		nameBytes += len(e.name)
		if count > 100000 || nameBytes > 16<<20 {
			return io.ErrShortBuffer
		}
		return nil
	}); err != nil {
		http.Error(w, "snapshot unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="snapshot-`+sha[:12]+`.zip"`)
	z := zip.NewWriter(w)
	err := walkSnapshotTree(r.Context(), m, sha, func(e snapshotEntry) error {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch e.mode {
		case "100644":
			h.SetMode(0644)
		case "100755":
			h.SetMode(0755)
		case "120000":
			h.SetMode(os.ModeSymlink | 0777)
		case "160000":
			h.Name += "/"
			h.SetMode(os.ModeDir | 0755)
		default:
			return io.ErrUnexpectedEOF
		}
		entry, err := z.CreateHeader(h)
		if err != nil {
			return err
		}
		if e.mode == "160000" {
			return nil
		}
		cmd := gitReader(r.Context(), m, "cat-file", "blob", e.sha)
		cmd.Stdout = entry
		return cmd.Run()
	})
	if err != nil {
		return
	}
	_ = z.Close()
}

type snapshotEntry struct{ mode, sha, name string }

func walkSnapshotTree(ctx context.Context, mirror, sha string, visit func(snapshotEntry) error) error {
	cmd := gitReader(ctx, mirror, "ls-tree", "-r", "-z", "--full-tree", sha)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	s := bufio.NewScanner(pipe)
	s.Split(splitNUL)
	s.Buffer(make([]byte, 4096), 256<<10)
	for s.Scan() {
		header, name, ok := strings.Cut(s.Text(), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || !repoPath(name) || name == "" || !validObjectID(fields[2]) {
			err = io.ErrUnexpectedEOF
			break
		}
		mode := fields[0]
		if mode != "100644" && mode != "100755" && mode != "120000" && mode != "160000" {
			err = io.ErrUnexpectedEOF
			break
		}
		if err = visit(snapshotEntry{mode, fields[2], name}); err != nil {
			break
		}
	}
	if err == nil {
		err = s.Err()
	}
	if err != nil {
		_ = cmd.Process.Kill()
	}
	_ = pipe.Close()
	waitErr := cmd.Wait()
	if err != nil {
		return err
	}
	return waitErr
}
