package mirror

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"githubarchive/internal/model"
)

func TestRemoteValidation(t *testing.T) {
	if err := ValidateGitHubRemote("https://github.com/octocat/Hello-World.git"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"http://github.com/a/b.git", "https://user@github.com/a/b.git", "https://github.com:443/a/b.git", "https://github.com/a/../b.git", "https://github.com/a/b.git/", "https://github.com/a/b.git?x=1", "https://github.com/a/b.git#x", "file:///a/b.git", "git@github.com:a/b.git"} {
		if ValidateGitHubRemote(s) == nil {
			t.Errorf("accepted %q", s)
		}
	}
	path := filepath.Join(t.TempDir(), "no", "mirror.git")
	if Fetch(context.Background(), path, "file:///nope", "secret") == nil {
		t.Fatal("accepted file URL")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("invalid remote created directory: %v", err)
	}
}

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func TestFetchDiffAndRetention(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	mirror := filepath.Join(root, "archive", "mirror.git")
	gitTest(t, root, "init", "--bare", remote)
	gitTest(t, root, "init", work)
	gitTest(t, work, "config", "user.email", "test@example.com")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "checkout", "-b", "archive-test")
	if err := os.WriteFile(filepath.Join(work, "file"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "file")
	gitTest(t, work, "commit", "-m", "one")
	old := gitTest(t, work, "rev-parse", "HEAD")
	gitTest(t, work, "push", remote, "HEAD:refs/heads/archive-test")
	ctx := context.Background()
	if err := fetchRemote(ctx, mirror, remote, "secret"); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, mirror, "config", "--get", "fetch.prune"); got != "false" {
		t.Fatalf("prune: %s", got)
	}
	repo := model.Repository{Owner: "a", Name: "b"}
	changes, snapshot, err := diffRemote(ctx, mirror, remote, "secret", repo, nil)
	if err != nil || len(changes) != 1 || changes[0].Kind != "create" || changes[0].OldSHA != "" || changes[0].NewSHA != old {
		t.Fatalf("create: %+v %v", changes, err)
	}
	if err := WriteSnapshot(filepath.Dir(mirror), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot, err = ReadSnapshot(filepath.Dir(mirror))
	if err != nil {
		t.Fatal(err)
	}
	// Replace history with an unrelated commit and preserve the old tip through gc.
	gitTest(t, work, "checkout", "--orphan", "replacement")
	gitTest(t, work, "commit", "--allow-empty", "-m", "replacement")
	next := gitTest(t, work, "rev-parse", "HEAD")
	gitTest(t, work, "push", "--force", remote, "HEAD:refs/heads/archive-test")
	if err := fetchRemote(ctx, mirror, remote, "secret"); err != nil {
		t.Fatal(err)
	}
	changes, snapshot, err = diffRemote(ctx, mirror, remote, "secret", repo, snapshot)
	if err != nil || len(changes) != 1 || changes[0].Kind != "force_push" || changes[0].OldSHA != old || changes[0].NewSHA != next {
		t.Fatalf("force: %+v %v", changes, err)
	}
	gitTest(t, mirror, "gc", "--prune=now")
	if got := gitTest(t, mirror, "cat-file", "-t", "refs/keep/"+old); got != "commit" {
		t.Fatal(got)
	}
	gitTest(t, work, "push", remote, ":refs/heads/archive-test")
	if err := fetchRemote(ctx, mirror, remote, "secret"); err != nil {
		t.Fatal(err)
	}
	changes, snapshot, err = diffRemote(ctx, mirror, remote, "secret", repo, snapshot)
	if err != nil || len(changes) != 1 || changes[0].Kind != "delete" || changes[0].NewSHA != "" {
		t.Fatalf("delete: %+v %v", changes, err)
	}
	if got := gitTest(t, mirror, "rev-parse", "refs/heads/archive-test"); got != next {
		t.Fatal(got)
	}
	if err := WriteSnapshot(filepath.Dir(mirror), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot, err = ReadSnapshot(filepath.Dir(mirror))
	if err != nil {
		t.Fatal(err)
	}
	changes, _, err = diffRemote(ctx, mirror, remote, "secret", repo, snapshot)
	if err != nil || len(changes) != 0 {
		t.Fatalf("repeat: %+v %v", changes, err)
	}
	config, err := os.ReadFile(filepath.Join(mirror, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "secret") || strings.Contains(string(config), "mirror = true") {
		t.Fatalf("unexpected config: %s", config)
	}
}
