package panel

import (
	"io"
	"net/http"
	"os"
	"path/filepath"

	"githubarchive/internal/model"
)

const maxLogTail = 32 << 10

// log reads only the end of a target's current log. os.Root confines symlink
// resolution as well as lexical paths to the archive directory.
func (p *Server) log(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	owner, name, login := r.URL.Query().Get("owner"), r.URL.Query().Get("name"), r.URL.Query().Get("login")
	var relative string
	if login != "" && owner == "" && name == "" && model.ValidSegment(login) {
		relative = filepath.Join("users", login, "archive.log")
	} else if login == "" && model.ValidSegment(owner) && model.ValidSegment(name) {
		relative = filepath.Join("repos", owner, name, "archive.log")
	} else {
		http.Error(w, "invalid target", http.StatusBadRequest)
		return
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		http.Error(w, "cannot read archive log", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	f, err := root.Open(relative)
	if os.IsNotExist(err) {
		jsonReply(w, struct {
			Text string `json:"text"`
		}{""})
		return
	}
	if err != nil {
		http.Error(w, "cannot read archive log", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "cannot read archive log", http.StatusInternalServerError)
		return
	}
	start := info.Size() - maxLogTail
	if start < 0 {
		start = 0
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		http.Error(w, "cannot read archive log", http.StatusInternalServerError)
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, maxLogTail))
	if err != nil {
		http.Error(w, "cannot read archive log", http.StatusInternalServerError)
		return
	}
	jsonReply(w, struct {
		Text string `json:"text"`
	}{string(b)})
}
