package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogEndpoint(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "archive")
	cfg := filepath.Join(dir, "config.yaml")
	data := "token_env: ABSENT_ARCHIVE_TOKEN\ndata_root: '" + filepath.ToSlash(root) + "'\nusers:\n  - login: octocat\n"
	if err := os.WriteFile(cfg, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, path := range []string{"/api/log?login=octocat", "/api/log?owner=a&name=b"} {
		w := request(t, p, "GET", path, "localhost:8765", "", "")
		if w.Code != 200 || w.Body.String() != "{\"text\":\"\"}\n" {
			t.Fatalf("missing %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/log", "/api/log?login=..", "/api/log?login=alice%2F..", "/api/log?owner=a&name=..", "/api/log?login=a&owner=b&name=c"} {
		w := request(t, p, "GET", path, "localhost:8765", "", "")
		if w.Code != 400 {
			t.Fatalf("invalid %s: %d", path, w.Code)
		}
	}
	logDir := filepath.Join(root, "repos", "a", "b")
	if err = os.MkdirAll(logDir, 0700); err != nil {
		t.Fatal(err)
	}
	contents := strings.Repeat("x", maxLogTail+1024) + "end\n"
	if err = os.WriteFile(filepath.Join(logDir, "archive.log"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	w := request(t, p, "GET", "/api/log?owner=a&name=b", "localhost:8765", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Text string `json:"text"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Text) != maxLogTail || result.Text != contents[len(contents)-maxLogTail:] {
		t.Fatalf("tail length %d", len(result.Text))
	}
	userDir := filepath.Join(root, "users", "octocat")
	if err = os.MkdirAll(userDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(userDir, "archive.log"), []byte("user error\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w = request(t, p, "GET", "/api/log?login=octocat", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "user error") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLogRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "archive")
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("token_env: ABSENT_ARCHIVE_TOKEN\ndata_root: '"+filepath.ToSlash(root)+"'\nusers:\n  - login: a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	outside := filepath.Join(dir, "outside")
	if err = os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outside, "archive.log"), []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "users"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, "users", "a")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	w := request(t, p, "GET", "/api/log?login=a", "localhost:8765", "", "")
	if w.Code == 200 || strings.Contains(w.Body.String(), "outside secret") {
		t.Fatalf("escaped: %d %s", w.Code, w.Body.String())
	}
}
