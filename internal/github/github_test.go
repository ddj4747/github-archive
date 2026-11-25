package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(handler)
	old := apiBase
	apiBase = s.URL
	t.Cleanup(func() { apiBase = old; s.Close() })
	c, err := New("ghp_SUPERSECRETVALUE")
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

func TestSince(t *testing.T) {
	pages := [][]byte{[]byte(`[{"id":"3","type":"PushEvent","actor":{"login":"cat"},"repo":{"name":"owner/repo"},"created_at":"2024-01-01T00:00:00Z","public":false},{"id":"2"}]`), []byte(`[{"id":"1"}]`)}
	got, newest, err := Since(pages, "2")
	if err != nil || newest != "3" || len(got) != 1 || got[0].ID != "3" || got[0].Public || got[0].RepoOwner != "owner" || got[0].RepoName != "repo" || !strings.Contains(got[0].Raw, `"public":false`) {
		t.Fatalf("events=%+v newest=%q error=%v", got, newest, err)
	}
	got, newest, err = Since(pages, "3")
	if err != nil || newest != "3" || len(got) != 0 {
		t.Fatalf("cursor at head: %v %q %v", got, newest, err)
	}
	got, _, err = Since(pages, "not-present")
	if err != nil || len(got) != 3 {
		t.Fatalf("missing cursor: %v %v", got, err)
	}
	if _, _, err := Since([][]byte{[]byte("[")}, ""); err == nil {
		t.Fatal("malformed page accepted")
	}
}

func TestEndpointsAndPagination(t *testing.T) {
	var paths []string
	c, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_SUPERSECRETVALUE" || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("missing headers")
		}
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `[{"id":"1"}]`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			w.Header().Set("Link", "<http://"+r.Host+"/next?page=2>; rel=\"next\"")
			fmt.Fprint(w, `[{"id":"2","public":false}]`)
			return
		}
		fmt.Fprint(w, `[]`)
	})
	_ = server
	events, newest, err := RepoEvents(context.Background(), c, "owner", "repo", "")
	if err != nil || len(events) != 2 || events[0].Public || newest != "2" {
		t.Fatalf("%+v %q %v", events, newest, err)
	}
	_, _, err = UserEvents(context.Background(), c, "cat", "")
	if err != nil {
		t.Fatal(err)
	}
	if paths[0] != "/repos/owner/repo/events?per_page=30" || paths[2] != "/users/cat/events/public?per_page=30" {
		t.Fatalf("paths: %v", paths)
	}
}

func TestLimitsAndCredentialSafety(t *testing.T) {
	t.Run("pages", func(t *testing.T) {
		var count int
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			count++
			w.Header().Set("Link", "<http://"+r.Host+"/next>; rel=\"next\"")
			fmt.Fprint(w, `[]`)
		})
		pages, err := c.GetPages(context.Background(), "/start")
		if err != nil || count != 10 || len(pages) != 10 {
			t.Fatalf("count=%d pages=%d err=%v", count, len(pages), err)
		}
	})
	t.Run("untrusted next", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Link", "<https://evil.example/path>; rel=\"next\"")
			fmt.Fprint(w, `[]`)
		})
		if _, err := c.GetPages(context.Background(), "/start"); err == nil {
			t.Fatal("accepted off-host next")
		}
		if _, err := c.GetPages(context.Background(), "https://evil.example/"); err == nil {
			t.Fatal("accepted absolute path")
		}
	})
	t.Run("size", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", maxBody+1)) })
		if _, err := c.GetPages(context.Background(), "/start"); err == nil {
			t.Fatal("accepted oversized response")
		}
	})
	t.Run("delay", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(429)
		})
		start := time.Now()
		_, err := c.GetPages(context.Background(), "/start")
		if err == nil || time.Since(start) > 2*time.Second || strings.Contains(err.Error(), "ghp_SUPERSECRETVALUE") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("retry", func(t *testing.T) {
		var count atomic.Int32
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if count.Add(1) == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				return
			}
			fmt.Fprint(w, `[]`)
		})
		if _, err := c.GetPages(context.Background(), "/start"); err != nil || count.Load() != 2 {
			t.Fatalf("%v count=%d", err, count.Load())
		}
	})
}
