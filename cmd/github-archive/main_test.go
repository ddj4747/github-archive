package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"githubarchive/internal/github"
	"githubarchive/internal/model"
	"githubarchive/internal/store"
)

func TestRunInitializesAndCancels(t *testing.T) {
	const token = "ghp_SUPERSECRETVALUE"
	t.Setenv("ARCHIVE_TEST_TOKEN", token)
	root := t.TempDir()
	data := filepath.Join(root, "data")
	config := filepath.Join(root, "config.yaml")
	contents := "token_env: ARCHIVE_TEST_TOKEN\ndata_root: '" + filepath.ToSlash(data) + "'\nusers:\n  - login: octocat\n"
	if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	old := runLoop
	defer func() { runLoop = old }()
	entered := make(chan struct{})
	runLoop = func(ctx context.Context, dir string, repos []model.Repository, users []model.User, gotToken string, db *store.Store, c *github.Client) error {
		if dir != data || len(repos) != 0 || len(users) != 1 || users[0].Login != "octocat" || gotToken != token || db == nil || c == nil {
			t.Error("wrong initialized arguments")
		}
		if _, err := os.Stat(filepath.Join(data, "archive.sqlite")); err != nil {
			t.Error(err)
		}
		close(entered)
		<-ctx.Done()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, config) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not start")
	}
	checkHeartbeat := func(wantState string) {
		t.Helper()
		db, err := store.Open(data)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		last, err := db.Heartbeat()
		if err != nil || last.IsZero() {
			t.Fatalf("heartbeat: %v %v", last, err)
		}
		state, err := db.GetCursor(store.WorkerStateFeed)
		if err != nil || state != wantState {
			t.Fatalf("worker state %q: %v", state, err)
		}
	}
	checkHeartbeat("running")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown blocked")
	}
	checkHeartbeat("stopped")
	if err := filepath.Walk(data, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), token) {
				t.Errorf("token in %s", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunReloadsOnlyValidConfigurationSerially(t *testing.T) {
	t.Setenv("ARCHIVE_RELOAD_TOKEN_A", "token-one")
	t.Setenv("ARCHIVE_RELOAD_TOKEN_B", "token-two")
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	dataA, dataB := filepath.Join(root, "a"), filepath.Join(root, "b")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("token_env: ARCHIVE_RELOAD_TOKEN_A\ndata_root: '" + filepath.ToSlash(dataA) + "'\nusers:\n  - login: first\n")
	oldLoop, oldInterval, oldHeartbeat := runLoop, reloadInterval, heartbeatInterval
	defer func() { runLoop, reloadInterval, heartbeatInterval = oldLoop, oldInterval, oldHeartbeat }()
	reloadInterval = 30 * time.Millisecond
	heartbeatInterval = 30 * time.Millisecond
	type started struct {
		dir, login, token string
		overlapping       bool
	}
	starts := make(chan started, 5)
	stops := make(chan string, 5)
	var active atomic.Int32
	runLoop = func(ctx context.Context, dir string, _ []model.Repository, users []model.User, token string, _ *store.Store, _ *github.Client) error {
		overlapping := active.Add(1) != 1
		starts <- started{dir, users[0].Login, token, overlapping}
		<-ctx.Done()
		active.Add(-1)
		stops <- users[0].Login
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, path) }()
	defer cancel()
	waitStart := func() started {
		t.Helper()
		select {
		case s := <-starts:
			return s
		case <-time.After(5 * time.Second):
			t.Fatal("loop did not start")
			return started{}
		}
	}
	first := waitStart()
	if first.dir != dataA || first.login != "first" || first.token != "token-one" || first.overlapping {
		t.Fatalf("first loop: %+v", first)
	}
	dbA, err := store.Open(dataA)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	firstBeat, err := dbA.Heartbeat()
	if err != nil || firstBeat.IsZero() {
		t.Fatalf("first heartbeat: %v %v", firstBeat, err)
	}
	write("invalid: [")
	select {
	case s := <-starts:
		t.Fatalf("invalid config restarted loop: %+v", s)
	case <-time.After(150 * time.Millisecond):
	}
	latestBeat, err := dbA.Heartbeat()
	if err != nil || !latestBeat.After(firstBeat) {
		t.Fatalf("periodic heartbeat %v after %v: %v", latestBeat, firstBeat, err)
	}
	select {
	case s := <-stops:
		t.Fatalf("invalid config stopped loop: %s", s)
	default:
	}
	write("token_env: ARCHIVE_RELOAD_TOKEN_B\ndata_root: '" + filepath.ToSlash(dataB) + "'\npoll_interval: 8m\nusers:\n  - login: second\n")
	second := waitStart()
	if second.dir != dataB || second.login != "second" || second.token != "token-two" || second.overlapping {
		t.Fatalf("reloaded loop: %+v", second)
	}
	select {
	case s := <-stops:
		if s != "first" {
			t.Fatalf("stopped %s first", s)
		}
	default:
		t.Fatal("new loop started before old stopped")
	}
	if _, err := os.Stat(filepath.Join(dataB, "archive.sqlite")); err != nil {
		t.Fatal(err)
	}
	if state, err := dbA.GetCursor(store.WorkerStateFeed); err != nil || state != "stopped" {
		t.Fatalf("old root state %q: %v", state, err)
	}
	dbB, err := store.Open(dataB)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	if state, err := dbB.GetCursor(store.WorkerStateFeed); err != nil || state != "running" {
		t.Fatalf("new root state %q: %v", state, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reload process did not stop")
	}
	if active.Load() != 0 {
		t.Fatal("loop left running")
	}
}
