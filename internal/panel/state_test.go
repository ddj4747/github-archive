package panel

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventAndRefStateAndCommitBundle(t *testing.T) {
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
		c := exec.Command("git", args...)
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q", work)
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("old state"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", ".")
	git("-C", work, "-c", "user.name=Tester", "-c", "user.email=t@example.invalid", "commit", "-qm", "old")
	old := git("-C", work, "rev-parse", "HEAD")
	git("-C", work, "-c", "user.name=Tester", "-c", "user.email=t@example.invalid", "tag", "-a", "v1", old, "-m", "tag")
	tag := git("-C", work, "rev-parse", "refs/tags/v1")
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("new state"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", ".")
	git("-C", work, "-c", "user.name=Tester", "-c", "user.email=t@example.invalid", "commit", "-qm", "new")
	newSHA := git("-C", work, "rev-parse", "HEAD")
	git("init", "--bare", "-q", mirror)
	git("-C", mirror, "fetch", "-q", work, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	git("-C", mirror, "update-ref", "refs/keep/"+old, old)
	p, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	events := []struct{ id, typ, payload string }{
		{"head", "PushEvent", fmt.Sprintf(`{"ref":"refs/heads/main","head":%q}`, old)},
		{"after", "PushEvent", fmt.Sprintf(`{"after":%q}`, old)},
		{"conflict", "PushEvent", fmt.Sprintf(`{"head":%q,"after":%q}`, old, newSHA)},
		{"zero", "PushEvent", fmt.Sprintf(`{"head":%q}`, strings.Repeat("0", 40))},
		{"unavailable", "PushEvent", fmt.Sprintf(`{"head":%q}`, strings.Repeat("a", 40))},
		{"missing", "PushEvent", `{"commits":[{"sha":"some-commit"}]}`},
		{"watch", "WatchEvent", fmt.Sprintf(`{"head":%q}`, old)},
		{"merged", "PullRequestEvent", fmt.Sprintf(`{"action":"closed","pull_request":{"merged":true,"merge_commit_sha":%q,"base":{"repo":{"full_name":"A/B"}}}}`, old)},
		{"unmerged", "PullRequestEvent", fmt.Sprintf(`{"action":"closed","pull_request":{"merged":false,"merge_commit_sha":%q}}`, old)},
		{"wrong-base", "PullRequestEvent", fmt.Sprintf(`{"action":"closed","pull_request":{"merged":true,"merge_commit_sha":%q,"base":{"repo":{"full_name":"other/repo"}}}}`, old)},
	}
	for _, e := range events {
		_, err = p.db.Exec(`INSERT INTO activity_events(scope,event_id,event_type,login,repo_owner,repo_name,at,raw) VALUES(?,?,?,?,?,?,?,?)`, "user:alice", e.id, e.typ, "alice", "A", "B", "2026-01-01T00:00:00Z", `{"payload":`+e.payload+`}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		id        string
		available bool
	}{{"head", true}, {"after", true}, {"merged", true}, {"conflict", false}, {"zero", false}, {"unavailable", false}, {"missing", false}, {"watch", false}, {"unmerged", false}, {"wrong-base", false}} {
		w := request(t, p, "GET", "/api/event-state?scope=user%3Aalice&event_id="+test.id, "localhost:8765", "", "")
		var state resolvedState
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Available != test.available {
			t.Fatalf("%s: %d %s", test.id, w.Code, w.Body.String())
		}
		if test.available && (state.SHA != old || state.Owner != "a" || state.Name != "b") {
			t.Fatalf("%s: %+v", test.id, state)
		}
		if !test.available && state.Reason == "" {
			t.Fatalf("missing unavailable reason: %s", test.id)
		}
	}
	_, err = p.db.Exec(`INSERT INTO ref_changes(owner,name,ref_name,old_sha,new_sha,kind) VALUES(?,?,?,?,?,?)`, "a", "b", "refs/heads/old", old, "", "delete")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.db.Exec(`INSERT INTO ref_changes(owner,name,ref_name,old_sha,new_sha,kind) VALUES(?,?,?,?,?,?)`, "a", "b", "refs/tags/v1", "", tag, "create")
	if err != nil {
		t.Fatal(err)
	}
	// The target can later be removed from config. Its ref rows preserve the
	// mirror's casing even if the event's repo name has a different case.
	if err = os.WriteFile(cfg, []byte("token_env: HISTORY_NO_TOKEN\ndata_root: '"+filepath.ToSlash(data)+"'\nusers:\n  - login: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w := request(t, p, "GET", "/api/event-state?scope=user%3Aalice&event_id=head", "localhost:8765", "", "")
	var state resolvedState
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || !state.Available || state.Owner != "a" || state.Name != "b" {
		t.Fatalf("removed target casing: %d %s", w.Code, w.Body.String())
	}
	params := url.Values{"owner": {"a"}, "name": {"b"}, "ref_name": {"refs/heads/old"}, "old_sha": {old}, "new_sha": {""}}
	w = request(t, p, "GET", "/api/ref-state?"+params.Encode(), "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Available || state.Reason == "" {
		t.Fatalf("deleted after: %d %s", w.Code, w.Body.String())
	}
	params.Set("side", "before")
	w = request(t, p, "GET", "/api/ref-state?"+params.Encode(), "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || !state.Available || state.SHA != old {
		t.Fatalf("deleted before: %d %s", w.Code, w.Body.String())
	}
	params.Set("ref_name", "refs/tags/v1")
	params.Set("old_sha", "")
	params.Set("new_sha", tag)
	params.Set("side", "after")
	w = request(t, p, "GET", "/api/ref-state?"+params.Encode(), "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || !state.Available || state.SHA != old {
		t.Fatalf("annotated tag: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/repository/commit-bundle?owner=a&name=b&sha="+old, "localhost:8765", "", "")
	if w.Code != 200 {
		t.Fatalf("bundle %d %s", w.Code, w.Body.String())
	}
	bundle := filepath.Join(root, "old.bundle")
	if err = os.WriteFile(bundle, w.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	git("clone", "-q", bundle, restored)
	if got := git("-C", restored, "rev-parse", "HEAD"); got != old {
		t.Fatalf("restored %s, want %s", got, old)
	}
	body, err := os.ReadFile(filepath.Join(restored, "file.txt"))
	if err != nil || string(body) != "old state" {
		t.Fatalf("restored file %q %v", body, err)
	}
	cmd := exec.Command("git", "-C", restored, "cat-file", "-e", newSHA+"^{commit}")
	if cmd.Run() == nil {
		t.Fatal("bundle contains newer unrelated commit")
	}
}
