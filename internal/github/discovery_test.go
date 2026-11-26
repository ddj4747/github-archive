package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestDiscoveryResumesBeyondThreePagesAndFiltersOwner(t *testing.T) {
	var requests []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		if r.URL.Query().Get("per_page") != "100" {
			t.Error("page size is not bounded")
		}
		if r.URL.Path == "/users/Alice/repos" {
			page := r.URL.Query().Get("page")
			if page != "4" {
				next := map[string]string{"1": "2", "2": "3", "3": "4"}[page]
				w.Header().Set("Link", fmt.Sprintf("<http://%s%s?page=%s>; rel=\"next\"", r.Host, r.URL.Path, next))
			}
			count := 100
			if page == "4" {
				count = 1
			}
			fmt.Fprint(w, "[")
			for i := 0; i < count; i++ {
				if i != 0 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, `{"name":"repo%s-%d","owner":{"login":"Alice"},"private":false}`, page, i)
			}
			fmt.Fprint(w, "]")
			return
		}
		if r.URL.Path != "/user/repos" || r.URL.Query().Get("visibility") != "all" {
			t.Errorf("unexpected path %s", r.URL.RequestURI())
		}
		fmt.Fprint(w, `[{"name":"secret","owner":{"login":"alice"},"private":true},{"name":"elsewhere","owner":{"login":"bob"},"private":true}]`)
	})
	ctx := context.Background()
	cursor := ""
	var got []DiscoveredRepository
	for i := 0; i < 5; i++ {
		page, next, err := DiscoverRepositories(ctx, c, "Alice", cursor)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page...)
		cursor = next
		if i < 4 && cursor == "" {
			t.Fatalf("ended at %d", i)
		}
	}
	if cursor != "" || len(got) != 302 || !got[301].Private || got[301].Name != "secret" || len(requests) != 5 {
		t.Fatalf("got=%+v cursor=%q requests=%v", got, cursor, requests)
	}
}

func TestDiscoveryRejectsBadContinuationAndPrivateEndpointErrors(t *testing.T) {
	t.Run("off-host pagination", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Link", `<https://evil.example/users/Alice/repos?page=2>; rel="next"`)
			fmt.Fprint(w, `[]`)
		})
		if _, _, err := DiscoverRepositories(context.Background(), c, "Alice", ""); err == nil {
			t.Fatal("accepted untrusted next link")
		}
	})
	t.Run("invalid cursor", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
		for _, cursor := range []string{"public:0", "public:-1", "visible:01", "visible:1:2", "other:1"} {
			if _, _, err := DiscoverRepositories(context.Background(), c, "Alice", cursor); err == nil {
				t.Errorf("accepted %s", cursor)
			}
		}
	})
	t.Run("unsupported private listing", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
		_, _, err := DiscoverRepositories(context.Background(), c, "Alice", "visible:1")
		if err == nil || strings.Contains(err.Error(), "ghp_SUPERSECRETVALUE") {
			t.Fatalf("error: %v", err)
		}
		var status *HTTPStatusError
		if !errors.As(err, &status) || status.StatusCode != http.StatusUnauthorized {
			t.Fatalf("missing status: %v", err)
		}
	})
	t.Run("forbidden with remaining quota", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "50")
			w.Header().Set("X-RateLimit-Reset", "9999999999")
			w.WriteHeader(http.StatusForbidden)
		})
		_, _, err := DiscoverRepositories(context.Background(), c, "Alice", "visible:1")
		var status *HTTPStatusError
		if !errors.As(err, &status) || status.StatusCode != http.StatusForbidden {
			t.Fatalf("missing status: %v", err)
		}
	})
	t.Run("malformed page", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `[{"name":"../escape","owner":{"login":"Alice"}}]`)
		})
		if _, _, err := DiscoverRepositories(context.Background(), c, "Alice", ""); err == nil {
			t.Fatal("accepted invalid repository")
		}
	})
}

func TestPublicDiscoverySkipsAuthenticatedListing(t *testing.T) {
	requests := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/users/Alice/repos" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected public request: %s", r.URL.Path)
		}
		fmt.Fprint(w, `[{"name":"repo","owner":{"login":"Alice"}}]`)
	})
	c.token = ""
	repos, next, err := DiscoverRepositories(context.Background(), c, "Alice", "")
	if err != nil || len(repos) != 1 || next != "" || requests != 1 {
		t.Fatalf("repos=%v next=%q requests=%d err=%v", repos, next, requests, err)
	}
	_, next, err = DiscoverRepositories(context.Background(), c, "Alice", "visible:2")
	if err != nil || next != "" || requests != 1 {
		t.Fatalf("resuming visible scan: next=%q requests=%d err=%v", next, requests, err)
	}
}
