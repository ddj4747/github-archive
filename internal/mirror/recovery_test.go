package mirror

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchRepairsCorruptConfigWithoutLosingHistory(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	mirror := filepath.Join(root, "archive", "mirror.git")
	gitTest(t, root, "init", "--bare", remote)
	gitTest(t, root, "init", work)
	gitTest(t, work, "config", "user.email", "test@example.com")
	gitTest(t, work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "file"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "file")
	gitTest(t, work, "commit", "-m", "initial")
	sha := gitTest(t, work, "rev-parse", "HEAD")
	gitTest(t, work, "push", remote, "HEAD:refs/heads/main")
	ctx := context.Background()
	if err := fetchRemote(ctx, mirror, remote, "local-token"); err != nil {
		t.Fatal(err)
	}
	gitTest(t, mirror, "update-ref", "refs/keep/"+sha, sha)
	configPath := filepath.Join(mirror, "config")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), original...)
	corrupt[0] = 0
	if err = os.WriteFile(configPath, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if err = fetchRemote(ctx, mirror, remote, "local-token"); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(mirror, "config.corrupt-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup: %v %v", backups, err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(backup, corrupt) {
		t.Fatal("backup did not preserve original config", err)
	}
	if got := gitTest(t, mirror, "cat-file", "-t", "refs/keep/"+sha); got != "commit" {
		t.Fatal(got)
	}
	if got := gitTest(t, mirror, "config", "--get", "gc.auto"); got != "0" {
		t.Fatal(got)
	}
	if got := gitTest(t, mirror, "config", "--get", "fetch.prune"); got != "false" {
		t.Fatal(got)
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(config, []byte("local-token")) {
		t.Fatal("token leaked to config")
	}
	if err = fetchRemote(ctx, mirror, remote, "local-token"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(config, after) {
		t.Fatal("normal fetch rewrote config", err)
	}
	if err = fetchRemote(ctx, mirror, filepath.Join(root, "no-such-remote"), "local-token"); err == nil {
		t.Fatal("expected fetch error")
	}
	after, err = os.ReadFile(configPath)
	if err != nil || !bytes.Equal(config, after) {
		t.Fatal("unrelated fetch failure changed config", err)
	}
	backups, _ = filepath.Glob(filepath.Join(mirror, "config.corrupt-*"))
	if len(backups) != 1 {
		t.Fatal("unrelated failure caused repair")
	}
}

func TestRecoveryRefusesAmbiguousFormatAndCredentials(t *testing.T) {
	root := t.TempDir()
	mirror := filepath.Join(root, "mirror.git")
	gitTest(t, root, "init", "--bare", mirror)
	config := filepath.Join(mirror, "config")
	corrupt := []byte("\x00invalid")
	if err := os.WriteFile(config, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if err := repairConfig(context.Background(), mirror, "local-remote", "token"); err == nil {
		t.Fatal("accepted repo without refs")
	}
	got, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(got, corrupt) {
		t.Fatal("changed ambiguous config", err)
	}
	ref := filepath.Join(mirror, "refs", "heads", "test")
	if err = os.WriteFile(ref, []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := []byte("\x00token")
	if err = os.WriteFile(config, secret, 0600); err != nil {
		t.Fatal(err)
	}
	if err = repairConfig(context.Background(), mirror, "local-remote", "token"); err == nil {
		t.Fatal("copied credentials into backup")
	}
	backups, _ := filepath.Glob(filepath.Join(mirror, "config.corrupt-*"))
	if len(backups) != 0 {
		t.Fatal("created backup of credentials")
	}
	if err = os.WriteFile(ref, []byte(strings.Repeat("b", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if format, err := inferObjectFormat(mirror); err != nil || format != 64 {
		t.Fatalf("sha256: %d %v", format, err)
	}
	if err = os.WriteFile(filepath.Join(mirror, "refs", "heads", "other"), []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = inferObjectFormat(mirror); err == nil {
		t.Fatal("accepted mixed object formats")
	}
}

func TestBatchRefObjectCheck(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	gitTest(t, root, "init", repo)
	gitTest(t, repo, "config", "user.email", "test@example.com")
	gitTest(t, repo, "config", "user.name", "Test")
	gitTest(t, repo, "commit", "--allow-empty", "-m", "initial")
	sha := gitTest(t, repo, "rev-parse", "HEAD")
	refs := map[string]string{"a": sha, "b": sha}
	if err := checkRefObjects(context.Background(), repo, "token", refs); err != nil {
		t.Fatal(err)
	}
	refs["missing"] = strings.Repeat("a", 40)
	if err := checkRefObjects(context.Background(), repo, "token", refs); err == nil {
		t.Fatal("missing object accepted")
	}
}

func TestRepairPreservesSHA256ObjectFormat(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	mirror := filepath.Join(root, "mirror.git")
	gitTest(t, root, "init", "--bare", "--object-format=sha256", remote)
	gitTest(t, root, "init", "--object-format=sha256", work)
	gitTest(t, work, "config", "user.email", "test@example.com")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "commit", "--allow-empty", "-m", "initial")
	sha := gitTest(t, work, "rev-parse", "HEAD")
	gitTest(t, work, "push", remote, "HEAD:refs/heads/main")
	gitTest(t, root, "init", "--bare", "--object-format=sha256", mirror)
	if err := fetchRemote(context.Background(), mirror, remote, "token"); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(mirror, "config")
	if err := os.WriteFile(configPath, []byte("\x00broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := fetchRemote(context.Background(), mirror, remote, "token"); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, mirror, "rev-parse", "--show-object-format"); got != "sha256" {
		t.Fatal(got)
	}
	if got := gitTest(t, mirror, "cat-file", "-t", sha); got != "commit" {
		t.Fatal(got)
	}
}
