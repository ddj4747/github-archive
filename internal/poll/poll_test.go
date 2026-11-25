package poll

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"githubarchive/internal/github"
	"githubarchive/internal/model"
	"githubarchive/internal/store"
)

func TestRepoTransactionsAndSnapshot(t *testing.T) {
	oldFetch, oldRead, oldDiff, oldWrite, oldEvents := fetchMirror, readSnapshot, diffRefs, writeSnapshot, repoEvents
	defer func() {
		fetchMirror, readSnapshot, diffRefs, writeSnapshot, repoEvents = oldFetch, oldRead, oldDiff, oldWrite, oldEvents
	}()
	fetchMirror = func(context.Context, string, string, string) error { return nil }
	readSnapshot = func(string) (map[string]string, error) { return nil, nil }
	diffRefs = func(_ context.Context, _, _, _ string, r model.Repository, _ map[string]string) ([]model.RefChange, map[string]string, error) {
		return []model.RefChange{{Owner: r.Owner, Name: r.Name, RefName: "refs/heads/main", NewSHA: "new", Kind: "create"}}, map[string]string{"refs/heads/main": "new"}, nil
	}
	written := false
	writeSnapshot = func(string, map[string]string) error { written = true; return nil }
	repoEvents = func(context.Context, *github.Client, string, string, string) ([]github.Event, string, error) {
		return []github.Event{{ID: "123", Type: "PushEvent", Raw: `{"public":false}`, At: time.Now()}}, "123", nil
	}
	root := t.TempDir()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A real SQLite failure must roll back the already inserted event and both cursors.
	raw, err := sql.Open("sqlite", filepath.Join(root, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`CREATE TRIGGER fail_ref BEFORE INSERT ON ref_changes BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	r := model.Repository{Owner: "octocat", Name: "test"}
	if err = PollRepo(context.Background(), root, "secret", r, db, nil); err == nil {
		t.Fatal("expected transaction failure")
	}
	if written {
		t.Fatal("snapshot written after rollback")
	}
	for _, feed := range []string{model.RepoEventsFeed(r.Owner, r.Name), model.RefsFeed(r.Owner, r.Name)} {
		if c, err := db.GetCursor(feed); err != nil || c != "" {
			t.Fatalf("cursor %q: %q %v", feed, c, err)
		}
	}
	if e, err := db.ListEvents(model.RepoScope(r.Owner, r.Name), 100); err != nil || len(e) != 0 {
		t.Fatalf("events: %v %v", e, err)
	}
	if _, err = raw.Exec(`DROP TRIGGER fail_ref`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if err = PollRepo(context.Background(), root, "secret", r, db, nil); err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Fatal("snapshot missing")
	}
	if c, _ := db.GetCursor(model.RepoEventsFeed(r.Owner, r.Name)); c != "123" {
		t.Fatal(c)
	}
	if c, _ := db.GetCursor(model.RefsFeed(r.Owner, r.Name)); c == "" {
		t.Fatal("empty refs cursor")
	}
	if e, err := db.ListEvents(model.RepoScope(r.Owner, r.Name), 100); err != nil || len(e) != 1 || !strings.Contains(e[0].Raw, `"public":false`) {
		t.Fatalf("events: %v %v", e, err)
	}
	log, err := os.ReadFile(filepath.Join(root, "repos", "octocat", "test", "archive.log"))
	if err != nil || !strings.Contains(string(log), "123") || !strings.Contains(string(log), "old=") || strings.Contains(string(log), "secret") {
		t.Fatalf("log: %q %v", log, err)
	}

	// The Events API can be unavailable while Git still sees new refs.
	written = false
	apiFailure := errors.New("event feed unavailable")
	repoEvents = func(context.Context, *github.Client, string, string, string) ([]github.Event, string, error) {
		return nil, "", apiFailure
	}
	diffRefs = func(_ context.Context, _, _, _ string, r model.Repository, _ map[string]string) ([]model.RefChange, map[string]string, error) {
		return []model.RefChange{{Owner: r.Owner, Name: r.Name, RefName: "refs/heads/main", OldSHA: "new", NewSHA: "replacement", Kind: "force_push"}}, map[string]string{"refs/heads/main": "replacement"}, nil
	}
	if err := PollRepo(context.Background(), root, "secret", r, db, nil); !errors.Is(err, apiFailure) || !written {
		t.Fatalf("expected a saved snapshot and an API error, got written=%t: %v", written, err)
	}
	if cursor, _ := db.GetCursor(model.RepoEventsFeed(r.Owner, r.Name)); cursor != "123" {
		t.Fatalf("failed event feed moved the cursor to %q", cursor)
	}
	raw, err = sql.Open("sqlite", filepath.Join(root, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var detected string
	if err := raw.QueryRow(`SELECT detected_at FROM ref_observations WHERE new_sha='replacement'`).Scan(&detected); err != nil || detected == "" {
		t.Fatalf("missing timestamped force-push: %q %v", detected, err)
	}

	written = false
	gitFailure := errors.New("Git transport unavailable")
	fetchMirror = func(context.Context, string, string, string) error { return gitFailure }
	repoEvents = func(context.Context, *github.Client, string, string, string) ([]github.Event, string, error) {
		return []github.Event{{ID: "124", Type: "PushEvent", Raw: "{}", At: time.Now()}}, "124", nil
	}
	if err := PollRepo(context.Background(), root, "secret", r, db, nil); !errors.Is(err, gitFailure) || written {
		t.Fatalf("expected Git error and no snapshot, got written=%t: %v", written, err)
	}
	if cursor, _ := db.GetCursor(model.RepoEventsFeed(r.Owner, r.Name)); cursor != "124" {
		t.Fatalf("successful event feed did not move the cursor: %q", cursor)
	}
}

func TestUserErrorsCancellationAndNoMirrors(t *testing.T) {
	old := userEvents
	defer func() { userEvents = old }()
	root := t.TempDir()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u := model.User{Login: "person"}
	called := 0
	userEvents = func(context.Context, *github.Client, string, string) ([]github.Event, string, error) {
		called++
		return nil, "", errors.New("failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PollUser(ctx, root, "secret", u, db, nil); !errors.Is(err, context.Canceled) || called != 0 {
		t.Fatal(err, called)
	}
	if err := PollUser(context.Background(), root, "secret", u, db, nil); err == nil {
		t.Fatal("expected error")
	}
	if c, _ := db.GetCursor(model.UserEventsFeed(u.Login)); c != "" {
		t.Fatal(c)
	}
	userEvents = func(context.Context, *github.Client, string, string) ([]github.Event, string, error) {
		return []github.Event{{ID: "id1", Raw: "{}"}}, "id1", nil
	}
	if err := PollUser(context.Background(), root, "secret", u, db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "repos")); !os.IsNotExist(err) {
		t.Fatalf("unexpected repos: %v", err)
	}
	if c, _ := db.GetCursor(model.UserEventsFeed(u.Login)); c != "id1" {
		t.Fatal(c)
	}
}

func TestRunSequentialAndRecoversPanic(t *testing.T) {
	old := runUser
	defer func() { runUser = old }()
	root := t.TempDir()
	started := make(chan string, 2)
	release := make(chan struct{})
	runUser = func(ctx context.Context, _ string, _ string, u model.User, _ *store.Store, _ *github.Client) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("target poll has no deadline")
		}
		started <- u.Login
		if u.Login == "first" {
			<-release
			panic("ghp_SUPERSECRETVALUE")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, root, nil, []model.User{{Login: "first", PollInterval: time.Hour}, {Login: "second", PollInterval: time.Hour}}, "ghp_SUPERSECRETVALUE", nil, nil)
	}()
	if got := <-started; got != "first" {
		t.Fatal(got)
	}
	select {
	case got := <-started:
		t.Fatalf("started concurrently: %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-started:
		if got != "second" {
			t.Fatal(got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second not run")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation blocked")
	}
	log, err := os.ReadFile(filepath.Join(root, "users", "first", "archive.log"))
	if err != nil || strings.Contains(string(log), "ghp_SUPERSECRETVALUE") || !strings.Contains(string(log), "panic") {
		t.Fatalf("log: %q %v", log, err)
	}
}
