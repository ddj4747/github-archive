package panel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoricalBrowsingAndSnapshot(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	cfg := filepath.Join(root, "config.yaml")
	work := filepath.Join(root, "work")
	mirror := filepath.Join(data, "repos", "a", "b", "mirror.git")
	if err := os.WriteFile(cfg, []byte("token_env: HISTORY_NO_TOKEN\ndata_root: '"+filepath.ToSlash(data)+"'\nrepositories:\n  - owner: a\n    name: b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", work)
	if err := os.MkdirAll(filepath.Join(work, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"docs/hello.txt": "historic bytes\n", "plain.txt": "commit $Format:%H$\n", ".gitattributes": "docs/hello.txt export-ignore\nplain.txt export-subst\n", "binary.dat": string([]byte{0, 1, 255})} {
		if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(name)), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	largeText := strings.Repeat("a", maxFileBytes-1) + "📁 end"
	if err := os.WriteFile(filepath.Join(work, "docs", "large.txt"), []byte(largeText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "large.bin"), append([]byte{0}, bytes.Repeat([]byte("x"), 4999)...), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", ".")
	git("-C", work, "-c", "user.name=Tester", "-c", "user.email=t@example.invalid", "commit", "-qm", "historic")
	old := git("-C", work, "rev-parse", "HEAD")
	oldRef := git("-C", work, "symbolic-ref", "HEAD")
	git("init", "--bare", "-q", mirror)
	git("-C", mirror, "fetch", "-q", work, "+refs/heads/*:refs/heads/*")
	git("-C", mirror, "update-ref", "refs/keep/"+old, old)
	git("-C", work, "checkout", "--orphan", "replacement")
	git("-C", work, "rm", "-q", "-r", "--cached", ".")
	if err := os.WriteFile(filepath.Join(work, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", "new.txt")
	git("-C", work, "-c", "user.name=Tester", "-c", "user.email=t@example.invalid", "commit", "-qm", "replacement")
	replacement := git("-C", work, "rev-parse", "HEAD")
	git("-C", mirror, "fetch", "-q", work, "+refs/heads/replacement:refs/heads/replacement")
	git("-C", mirror, "update-ref", "-d", oldRef)
	git("-C", mirror, "gc", "--prune=now")
	var updates strings.Builder
	for i := 0; i < 35; i++ {
		fmt.Fprintf(&updates, "update refs/heads/test%02d %s\n", i, old)
	}
	updateCmd := exec.Command("git", "-C", mirror, "update-ref", "--stdin")
	updateCmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	updateCmd.Stdin = strings.NewReader(updates.String())
	if out, err := updateCmd.CombinedOutput(); err != nil {
		t.Fatalf("create refs: %v %s", err, out)
	}
	p, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	_, err = p.db.Exec(`INSERT INTO ref_changes VALUES
		('a','b','refs/heads/removed',?1,'','delete'),
		('a','b','refs/heads/main',?1,?2,'force_push'),
		('a','b','refs/heads/main','',?1,'create'),
		('a','other','refs/heads/unrelated',?1,'','delete')`, old, replacement)
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, p, "GET", "/api/refs?owner=a&name=b&kind=rewrites", "localhost:8765", "", "")
	var rewrites struct{ Items []refRow }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rewrites) != nil || len(rewrites.Items) != 2 {
		t.Fatalf("repository rewrites: %d %s", w.Code, w.Body.String())
	}
	base := "owner=a&name=b&sha=" + old
	w = request(t, p, "GET", "/api/repository/history?owner=a&name=b&limit=1", "localhost:8765", "", "")
	if w.Code != 200 {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	var page struct {
		Commits []historyCommit `json:"commits"`
		HasMore bool            `json:"has_more"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Commits) != 1 || !page.HasMore {
		t.Fatalf("history page: %v %+v", err, page)
	}
	w = request(t, p, "GET", "/api/repository/history?owner=a&name=b&limit=100", "localhost:8765", "", "")
	if w.Code != 200 {
		t.Fatalf("history: %d %s", w.Code, w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range page.Commits {
		if c.SHA == old {
			found = true
			if !strings.Contains(strings.Join(c.Refs, ","), "refs/keep/"+old) {
				t.Fatal("missing pinned ref", c.Refs)
			}
			if len(c.Refs) != 32 || !c.RefsTruncated {
				t.Fatalf("ref annotation bound: %+v", c)
			}
			if len(c.Changes) != 2 || c.Changes[0] != (historyChange{"refs/heads/removed", "delete", "before"}) || c.Changes[1] != (historyChange{"refs/heads/main", "force_push", "before"}) {
				t.Fatalf("old tip changes: %+v", c.Changes)
			}
		}
		if c.SHA == replacement && (len(c.Changes) != 1 || c.Changes[0] != (historyChange{"refs/heads/main", "force_push", "after"})) {
			t.Fatalf("replacement changes: %+v", c.Changes)
		}
	}
	if !found {
		t.Fatal("historic commit missing")
	}
	w = request(t, p, "GET", "/api/repository/history?owner=a&name=b&tip="+old, "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Commits) != 1 || page.Commits[0].SHA != old {
		t.Fatal("deleted branch graph included an unrelated tip")
	}
	w = request(t, p, "GET", "/api/repository/history?owner=a&name=b&tip="+old+"&compare="+replacement, "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Commits) != 2 {
		t.Fatal("force-push comparison did not show both histories")
	}
	w = request(t, p, "GET", "/api/repository/history?owner=a&name=b&tip=HEAD", "localhost:8765", "", "")
	if w.Code != 400 {
		t.Fatal("accepted ref expression instead of a retained commit ID")
	}
	w = request(t, p, "GET", "/api/repository/tree?"+base+"&path=docs", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "hello.txt") {
		t.Fatalf("tree: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/repository/file?"+base+"&path=docs%2Fhello.txt", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "historic bytes") {
		t.Fatalf("file: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/repository/file?"+base+"&path=binary.dat", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"encoding":"base64"`) {
		t.Fatalf("binary: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/repository/tree?"+base+"&path=docs&offset=1", "localhost:8765", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "hello.txt") || !strings.Contains(w.Body.String(), "large.txt") {
		t.Fatal("second directory page did not start at the requested entry")
	}
	var combined strings.Builder
	var offset int64
	for i := 0; i < 2; i++ {
		link := fmt.Sprintf("/api/repository/file?%s&path=docs/large.txt&offset=%d", base, offset)
		w = request(t, p, "GET", link, "localhost:8765", "", "")
		var page struct {
			Content    string `json:"content"`
			Encoding   string `json:"encoding"`
			NextOffset int64  `json:"next_offset"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Encoding != "utf-8" {
			t.Fatalf("invalid UTF-8 preview page %d", i+1)
		}
		combined.WriteString(page.Content)
		offset = page.NextOffset
	}
	if combined.String() != largeText || offset != 0 {
		t.Fatal("preview did not preserve the multibyte character across the page boundary")
	}
	for _, tc := range []struct {
		params     string
		size, next int
	}{
		{"offset=0", 4096, 4096},
		{"offset=4096&binary=1", 904, 0},
	} {
		w := request(t, p, "GET", "/api/repository/file?"+base+"&path=large.bin&"+tc.params, "localhost:8765", "", "")
		var part struct {
			Encoding   string `json:"encoding"`
			Content    []byte `json:"content"`
			NextOffset int    `json:"next_offset"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &part) != nil || part.Encoding != "base64" || len(part.Content) != tc.size || part.NextOffset != tc.next {
			t.Fatalf("invalid binary preview for %s", tc.params)
		}
	}
	for _, endpoint := range []string{"tree", "file"} {
		w = request(t, p, "GET", "/api/repository/"+endpoint+"?"+base+"&path="+url.QueryEscape("../outside"), "localhost:8765", "", "")
		if w.Code != 400 {
			t.Fatalf("path traversal %s: %d", endpoint, w.Code)
		}
	}
	w = request(t, p, "GET", "/api/repository/tree?owner=a&name=b&sha=HEAD", "localhost:8765", "", "")
	if w.Code != 400 {
		t.Fatalf("invalid sha %d", w.Code)
	}
	w = request(t, p, "GET", "/api/repository/snapshot?"+base, "localhost:8765", "", "")
	if w.Code != 200 {
		t.Fatalf("snapshot: %d %s", w.Code, w.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(b)
	}
	if files["docs/hello.txt"] != "historic bytes\n" || files["plain.txt"] != "commit $Format:%H$\n" {
		t.Fatalf("snapshot ignored/substituted tracked files: %+v", files)
	}
}
