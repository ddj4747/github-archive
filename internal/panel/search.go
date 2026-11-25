package panel

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"githubarchive/internal/github"
)

var searchGitHubUsers = github.SearchUsers

const searchCacheLimit = 128
const searchCacheTTL = 2 * time.Minute

type searchEntry struct {
	users   []github.UserSearchResult
	expires time.Time
}
type searchCoordinator struct {
	mu            sync.Mutex
	entries       map[string]searchEntry
	inflight      chan struct{}
	blockedUntil  time.Time
	blockedStatus int
}

var searches = &searchCoordinator{entries: make(map[string]searchEntry)}

// Search requests share one bounded cache and at most one upstream request.
// A canceled browser request does not cancel an upstream call shared by others.
func (s *searchCoordinator) get(ctx context.Context, query string) ([]github.UserSearchResult, int, time.Duration) {
	key := strings.ToLower(query)
	for {
		s.mu.Lock()
		now := time.Now()
		if e, ok := s.entries[key]; ok {
			if now.Before(e.expires) {
				s.mu.Unlock()
				return e.users, 0, 0
			}
			delete(s.entries, key)
		}
		if now.Before(s.blockedUntil) {
			status, delay := s.blockedStatus, time.Until(s.blockedUntil)
			s.mu.Unlock()
			return nil, status, delay
		}
		if s.inflight != nil {
			done := s.inflight
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, http.StatusBadGateway, 0
			}
		}
		done := make(chan struct{})
		s.inflight = done
		s.mu.Unlock()
		go s.fetch(query, key, done)
		select {
		case <-done:
			continue
		case <-ctx.Done():
			return nil, http.StatusBadGateway, 0
		}
	}
}

func (s *searchCoordinator) fetch(query, key string, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, err := github.NewPublic()
	var users []github.UserSearchResult
	if err == nil {
		users, err = searchGitHubUsers(ctx, client, query)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(done)
	s.inflight = nil
	now := time.Now()
	if err != nil {
		var status *github.HTTPStatusError
		if errors.As(err, &status) && (status.StatusCode == http.StatusTooManyRequests || status.StatusCode == http.StatusForbidden) {
			delay := status.RetryAfter
			if delay <= 0 {
				delay = time.Minute
			}
			if delay > time.Hour {
				delay = time.Hour
			}
			s.blockedStatus, s.blockedUntil = http.StatusTooManyRequests, now.Add(delay)
		} else {
			s.blockedStatus, s.blockedUntil = http.StatusBadGateway, now.Add(3*time.Second)
		}
		return
	}
	if users == nil {
		users = []github.UserSearchResult{}
	}
	if len(s.entries) >= searchCacheLimit {
		var oldest string
		var expiry time.Time
		for k, e := range s.entries {
			if oldest == "" || e.expires.Before(expiry) {
				oldest, expiry = k, e.expires
			}
		}
		delete(s.entries, oldest)
	}
	s.entries[key] = searchEntry{users: users, expires: now.Add(searchCacheTTL)}
}

func (p *Server) searchUsers(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 100 {
		http.Error(w, "enter a search term of at most 100 characters", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	users, status, delay := searches.get(ctx, q)
	if status != 0 {
		if status == http.StatusTooManyRequests {
			seconds := int((delay + time.Second - 1) / time.Second)
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			http.Error(w, "GitHub search is rate limited; try again later", status)
		} else {
			http.Error(w, "GitHub search is unavailable; try again", http.StatusBadGateway)
		}
		return
	}
	jsonReply(w, struct {
		Users []github.UserSearchResult `json:"users"`
	}{users})
}
