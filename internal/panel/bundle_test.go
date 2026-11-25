package panel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleDownloadContainsRetainedRefs(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("token_env: PANEL_BUNDLE_TOKEN\ndata_root: '"+filepath.ToSlash(dataRoot)+"'\nrepositories:\n  - owner: alice\n    name: project\n"), 0600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	mirror := filepath.Join(dataRoot, "repos", "alice", "project", "mirror.git")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", work)
	if err := os.WriteFile(filepath.Join(work, "example.txt"), []byte("retained content"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", "example.txt")
	git("-C", work, "-c", "user.name=Example", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial")
	sha := git("-C", work, "rev-parse", "HEAD")
	git("init", "--bare", "-q", mirror)
	git("-C", mirror, "fetch", "-q", work, "+refs/heads/*:refs/heads/*")
	git("-C", mirror, "update-ref", "refs/keep/"+sha, sha)
	p, err := Open(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	w := request(t, p, "GET", "/api/repository/bundle?owner=alice&name=project", "localhost:8765", "", "")
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatalf("bundle response %d: %s", w.Code, w.Body.String())
	}
	bundlePath := filepath.Join(root, "archive.bundle")
	if err := os.WriteFile(bundlePath, w.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	heads := git("bundle", "list-heads", bundlePath)
	if !strings.Contains(heads, "refs/keep/"+sha) || !strings.Contains(heads, "refs/heads/") {
		t.Fatalf("bundle missing refs: %s", heads)
	}
	restored := filepath.Join(root, "restored")
	git("init", "-q", restored)
	git("-C", restored, "fetch", "-q", bundlePath, "refs/*:refs/archive/*")
	git("-C", restored, "switch", "--detach", "-q", sha)
	contents, err := os.ReadFile(filepath.Join(restored, "example.txt"))
	if err != nil || string(contents) != "retained content" {
		t.Fatalf("restored content %q: %v", contents, err)
	}
	if w := request(t, p, "GET", "/api/repository/bundle?owner=..&name=project", "localhost:8765", "", ""); w.Code != 400 {
		t.Fatalf("accepted invalid path: %d", w.Code)
	}
}
