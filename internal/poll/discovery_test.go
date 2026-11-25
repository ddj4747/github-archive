package poll

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"githubarchive/internal/github"
	"githubarchive/internal/model"
	"githubarchive/internal/store"
)

func TestDiscoveryCommitsPageAndCursorTogether(t *testing.T) {
	old := discoverRepositories
	defer func() { discoverRepositories = old }()
	discoverRepositories = func(_ context.Context, _ *github.Client, login, cursor string) ([]github.DiscoveredRepository, string, error) {
		if login != "alice" || cursor != "" {
			t.Errorf("unexpected input: %s %s", login, cursor)
		}
		return []github.DiscoveredRepository{{Owner: "alice", Name: "one"}, {Owner: "alice", Name: "two"}}, "public:2", nil
	}
	root := t.TempDir()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw, err := sql.Open("sqlite", filepath.Join(root, "archive.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(`CREATE TRIGGER discovery_fail BEFORE INSERT ON discovered_repositories WHEN NEW.name = 'two' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	u := model.User{Login: "alice", DiscoverRepositories: true}
	if _, err = PollDiscovery(context.Background(), u, db, nil); err == nil {
		t.Fatal("expected failure")
	}
	if c, _ := db.GetCursor("repo-discovery:alice"); c != "" {
		t.Fatal(c)
	}
	if _, _, found, err := db.NextDiscovered("alice"); err != nil || found {
		t.Fatalf("partial commit: %v %v", found, err)
	}
	if _, err = raw.Exec(`DROP TRIGGER discovery_fail`); err != nil {
		t.Fatal(err)
	}
	more, err := PollDiscovery(context.Background(), u, db, nil)
	if err != nil || !more {
		t.Fatal(more, err)
	}
	if c, _ := db.GetCursor("repo-discovery:alice"); c != "public:2" {
		t.Fatal(c)
	}
	if _, _, found, err := db.NextDiscovered("alice"); err != nil || !found {
		t.Fatal(found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PollDiscovery(ctx, u, db, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRunArchivesDiscoveredAndContinuesAfterRestart(t *testing.T) {
	oldUser, oldRepo, oldDiscovery := runUser, runRepo, runDiscovery
	defer func() { runUser, runRepo, runDiscovery = oldUser, oldRepo, oldDiscovery }()
	root := t.TempDir()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := model.User{Login: "alice", PollInterval: time.Hour, DiscoverRepositories: true}
	if err := db.InTx(func(tx *store.Tx) error { return tx.UpsertDiscovered("alice", "existing", "alice") }); err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 3)
	runUser = func(context.Context, string, string, model.User, *store.Store, *github.Client) error { return nil }
	runDiscovery = func(_ context.Context, _ model.User, db *store.Store, _ *github.Client) (bool, error) {
		return false, db.InTx(func(tx *store.Tx) error { return tx.UpsertDiscovered("alice", "new", "alice") })
	}
	runRepo = func(_ context.Context, _ string, _ string, r model.Repository, _ *store.Store, _ *github.Client) error {
		seen <- r.Name
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, root, nil, []model.User{user}, "secret", db, nil) }()
	got := map[string]bool{}
	for len(got) < 2 {
		select {
		case name := <-seen:
			got[name] = true
		case <-time.After(5 * time.Second):
			t.Fatal("queued repositories not polled", got)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked")
	}
	if !got["existing"] || !got["new"] {
		t.Fatal(got)
	}
}

func TestDiscoveryUnsupportedPrivateListingDoesNotBlockPublicRescan(t *testing.T) {
	old := discoverRepositories
	defer func() { discoverRepositories = old }()
	discoverRepositories = func(context.Context, *github.Client, string, string) ([]github.DiscoveredRepository, string, error) {
		return nil, "", &github.HTTPStatusError{StatusCode: 403}
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetCursor("repo-discovery:alice", "visible:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := PollDiscovery(context.Background(), model.User{Login: "alice"}, db, nil); err == nil {
		t.Fatal("expected error")
	}
	if c, err := db.GetCursor("repo-discovery:alice"); err != nil || c != "" {
		t.Fatalf("public scan was blocked: %q %v", c, err)
	}
}

func TestAuthenticatedEventsHaveIndependentCursorAndStorePrivate(t *testing.T) {
	old := authenticatedUserEvents
	defer func() { authenticatedUserEvents = old }()
	authenticatedUserEvents = func(_ context.Context, _ *github.Client, login, cursor string) ([]github.Event, string, error) {
		if login != "alice" || cursor != "" {
			t.Errorf("unexpected input %s %s", login, cursor)
		}
		return []github.Event{{ID: "private-id", Raw: `{"public":false}`}}, "private-id", nil
	}
	root := t.TempDir()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetCursor(model.UserEventsFeed("alice"), "public-id"); err != nil {
		t.Fatal(err)
	}
	if err := PollAuthenticatedUser(context.Background(), root, "secret", model.User{Login: "alice"}, db, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetCursor(model.UserEventsFeed("alice")); got != "public-id" {
		t.Fatal(got)
	}
	if got, _ := db.GetCursor("user-authenticated-events:alice"); got != "private-id" {
		t.Fatal(got)
	}
	if events, err := db.ListEvents(model.UserScope("alice"), 100); err != nil || len(events) != 1 || events[0].Raw != `{"public":false}` {
		t.Fatal(events, err)
	}
}

func TestExcludedDiscoveryDoesNotBlockOtherRepositories(t *testing.T) {
	oldUser, oldRepo, oldDiscovery := runUser, runRepo, runDiscovery
	defer func() { runUser, runRepo, runDiscovery = oldUser, oldRepo, oldDiscovery }()
	root := t.TempDir()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InTx(func(tx *store.Tx) error {
		if err := tx.UpsertDiscovered("alice", "a-blocked", "alice"); err != nil {
			return err
		}
		return tx.UpsertDiscovered("alice", "b-allowed", "alice")
	}); err != nil {
		t.Fatal(err)
	}
	runUser = func(context.Context, string, string, model.User, *store.Store, *github.Client) error { return nil }
	runDiscovery = func(context.Context, model.User, *store.Store, *github.Client) (bool, error) { return false, nil }
	seen := make(chan string, 2)
	runRepo = func(_ context.Context, _ string, _ string, r model.Repository, _ *store.Store, _ *github.Client) error {
		seen <- r.Name
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, root, nil, []model.User{{Login: "alice", DiscoverRepositories: true, PollInterval: time.Hour, ExcludeRepositories: []string{"A-BLOCKED"}}}, "secret", db, nil)
	}()
	select {
	case got := <-seen:
		if got != "b-allowed" {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("allowed repository was blocked")
	}
	select {
	case got := <-seen:
		t.Fatalf("unexpected poll %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown blocked")
	}
}
