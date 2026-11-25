package panel

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"githubarchive/internal/github"
)

func TestPanelSearchUsers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("token_env: SEARCH_TEST_TOKEN\ndata_root: '"+filepath.ToSlash(filepath.Join(root, "data"))+"'\nusers:\n  - login: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	old := searchGitHubUsers
	defer func() { searchGitHubUsers = old }()
	searchGitHubUsers = func(_ context.Context, _ *github.Client, q string) ([]github.UserSearchResult, error) {
		if q != "alice" {
			t.Fatal(q)
		}
		return []github.UserSearchResult{{Login: "alice"}}, nil
	}
	w := request(t, p, "GET", "/api/search/users?q=alice", "localhost:8765", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"login":"alice"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(t, p, "GET", "/api/search/users?q=", "localhost:8765", "", ""); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
}

func TestSearchCoalescesAndCaches(t *testing.T) {
	old := searchGitHubUsers
	defer func() { searchGitHubUsers = old }()
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	searchGitHubUsers = func(context.Context, *github.Client, string) ([]github.UserSearchResult, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []github.UserSearchResult{{Login: "alice"}}, nil
	}
	s := &searchCoordinator{entries: make(map[string]searchEntry)}
	var wg sync.WaitGroup
	wg.Add(12)
	for i := 0; i < 12; i++ {
		go func() {
			defer wg.Done()
			users, status, _ := s.get(context.Background(), "alice")
			if status != 0 || len(users) != 1 {
				t.Errorf("search: %d %v", status, users)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("made %d upstream calls", calls.Load())
	}
	_, status, _ := s.get(context.Background(), "ALICE")
	if status != 0 || calls.Load() != 1 {
		t.Fatal("cache miss")
	}
}

func TestCanceledSearchDoesNotCancelSharedRequest(t *testing.T) {
	old := searchGitHubUsers
	defer func() { searchGitHubUsers = old }()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	searchGitHubUsers = func(ctx context.Context, _ *github.Client, _ string) ([]github.UserSearchResult, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return []github.UserSearchResult{{Login: "alice"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s := &searchCoordinator{entries: make(map[string]searchEntry)}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan int, 1)
	go func() { _, status, _ := s.get(ctx, "alice"); finished <- status }()
	<-started
	cancel()
	select {
	case status := <-finished:
		if status != http.StatusBadGateway {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled caller did not return")
	}
	close(release)
	users, status, _ := s.get(context.Background(), "alice")
	if status != 0 || len(users) != 1 || calls.Load() != 1 {
		t.Fatalf("status %d users %v calls %d", status, users, calls.Load())
	}
}

func TestSearchRateLimitCooldownAndCacheBound(t *testing.T) {
	old := searchGitHubUsers
	defer func() { searchGitHubUsers = old }()
	var calls atomic.Int32
	searchGitHubUsers = func(context.Context, *github.Client, string) ([]github.UserSearchResult, error) {
		calls.Add(1)
		return nil, &github.HTTPStatusError{StatusCode: 429, RetryAfter: time.Minute}
	}
	s := &searchCoordinator{entries: make(map[string]searchEntry)}
	for _, q := range []string{"alice", "bob", "other"} {
		_, status, delay := s.get(context.Background(), q)
		if status != 429 || delay < 50*time.Second {
			t.Fatalf("status=%d delay=%v", status, delay)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("made %d calls while rate limited", calls.Load())
	}
	searchGitHubUsers = func(context.Context, *github.Client, string) ([]github.UserSearchResult, error) {
		return []github.UserSearchResult{}, nil
	}
	s = &searchCoordinator{entries: make(map[string]searchEntry)}
	for i := 0; i < searchCacheLimit+10; i++ {
		_, status, _ := s.get(context.Background(), strconv.Itoa(i))
		if status != 0 {
			t.Fatal(status)
		}
	}
	if len(s.entries) != searchCacheLimit {
		t.Fatalf("cache has %d entries", len(s.entries))
	}
}
