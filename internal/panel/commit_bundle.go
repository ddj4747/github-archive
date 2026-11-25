package panel

import (
	"io"
	"net/http"
	"strings"
)

// commitBundle emits one named tip and its ancestry. It does not alter the
// mirror or include unrelated current refs and newer commits.
func (p *Server) commitBundle(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	mirror, sha, ok := p.targetCommit(w, r)
	if !ok {
		return
	}
	cmd := gitReader(r.Context(), mirror, "pack-objects", "--stdout", "--revs")
	cmd.Stdin = strings.NewReader(sha + "\n")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "bundle unavailable", 500)
		return
	}
	if err = cmd.Start(); err != nil {
		http.Error(w, "bundle unavailable", 500)
		return
	}
	first := make([]byte, 12)
	n, err := io.ReadFull(pipe, first)
	if err != nil || n != len(first) || string(first[:4]) != "PACK" {
		_ = pipe.Close()
		_ = cmd.Wait()
		http.Error(w, "bundle unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="snapshot-`+sha[:12]+`.bundle"`)
	version := "# v2 git bundle\n"
	if len(sha) == 64 {
		version = "# v3 git bundle\n@object-format=sha256\n"
	}
	header := version + sha + " HEAD\n" + sha + " refs/heads/archive\n\n"
	_, writeErr := io.WriteString(w, header)
	if writeErr == nil {
		_, writeErr = w.Write(first)
	}
	if writeErr == nil {
		_, writeErr = io.Copy(w, pipe)
	}
	if writeErr != nil {
		_ = pipe.Close()
	}
	_ = cmd.Wait()
}
