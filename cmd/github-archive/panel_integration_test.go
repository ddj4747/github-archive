package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"githubarchive/internal/github"
	"githubarchive/internal/model"
	"githubarchive/internal/panel"
	"githubarchive/internal/store"
)

func TestPanelTargetEditsReloadRunningArchiver(t *testing.T) {
	t.Setenv("PANEL_INTEGRATION_TOKEN", "test-token")
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	content := fmt.Sprintf("token_env: PANEL_INTEGRATION_TOKEN\ndata_root: '%s'\nusers:\n  - login: alice\n", filepath.ToSlash(filepath.Join(root, "data")))
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	oldLoop, oldInterval := runLoop, reloadInterval
	defer func() { runLoop, reloadInterval = oldLoop, oldInterval }()
	reloadInterval = 20 * time.Millisecond
	started := make(chan []model.User, 3)
	var active atomic.Int32
	runLoop = func(ctx context.Context, _ string, _ []model.Repository, users []model.User, _ string, _ *store.Store, _ *github.Client) error {
		if active.Add(1) != 1 {
			t.Error("polling loops overlap")
		}
		started <- append([]model.User(nil), users...)
		<-ctx.Done()
		active.Add(-1)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, path) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("archiver did not stop")
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("initial loop did not start")
	}
	p, err := panel.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	server := httptest.NewServer(p)
	defer server.Close()
	resp, err := http.Get(server.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	err = json.NewDecoder(resp.Body).Decode(&cfg)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	cfg["users"] = append(cfg["users"].([]any), map[string]any{"login": "bob", "poll_interval": "", "discover_repositories": true, "authenticated_events": false, "exclude_repositories": []string{}})
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, server.URL+"/api/config", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", server.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("save failed: %s %s", resp.Status, b)
	}
	select {
	case users := <-started:
		if len(users) != 2 || users[1].Login != "bob" || !users[1].DiscoverRepositories {
			t.Fatalf("reload: %+v", users)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("updated targets were not applied")
	}
}
