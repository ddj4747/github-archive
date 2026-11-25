package github

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEventsStopAtCursorAndRespectPageOrder(t *testing.T) {
	requests := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Link", "<http://"+r.Host+"/page2>; rel=\"next\"")
			fmt.Fprint(w, `[{"id":"5","public":false},{"id":"4"}]`)
			return
		}
		w.Header().Set("Link", "<http://"+r.Host+"/page3>; rel=\"next\"")
		fmt.Fprint(w, `[{"id":"3"},{"id":"2"}]`)
	})
	events, newest, err := RepoEvents(context.Background(), c, "alice", "repo", "3")
	if err != nil || newest != "5" || len(events) != 2 || events[0].ID != "5" || events[0].Public || events[1].ID != "4" || requests != 2 {
		t.Fatalf("events=%+v newest=%q requests=%d error=%v", events, newest, requests, err)
	}
}

func TestConditionalEventsRequireCommittedCursor(t *testing.T) {
	requests := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("ETag", `"version-3"`)
		if requests == 3 {
			if r.Header.Get("If-None-Match") != `"version-3"` {
				t.Error("missing conditional validator")
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			t.Error("validator used before cursor commit")
		}
		fmt.Fprint(w, `[{"id":"3"},{"id":"2"}]`)
	})
	for i := 0; i < 2; i++ {
		events, newest, err := UserEvents(context.Background(), c, "alice", "2")
		if err != nil || newest != "3" || len(events) != 1 || events[0].ID != "3" {
			t.Fatalf("%+v %q %v", events, newest, err)
		}
	}
	events, newest, err := UserEvents(context.Background(), c, "alice", "3")
	if err != nil || newest != "3" || len(events) != 0 || requests != 3 {
		t.Fatalf("%+v %q %v requests=%d", events, newest, err, requests)
	}
}

func TestEventPollIntervalDoesNotBlockOtherCalls(t *testing.T) {
	requests := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/users/alice/events/public" {
			w.Header().Set("X-Poll-Interval", "3600")
			fmt.Fprint(w, `[{"id":"3"}]`)
			return
		}
		fmt.Fprint(w, `[]`)
	})
	if _, _, err := UserEvents(context.Background(), c, "alice", ""); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _, err := UserEvents(context.Background(), c, "alice", "3")
	var deferred *PollDeferredError
	if !errors.As(err, &deferred) || deferred.RetryAfter < 59*time.Minute || time.Since(start) > time.Second {
		t.Fatalf("deferred=%v error=%v", deferred, err)
	}
	if _, err := c.GetPages(context.Background(), "/another-endpoint"); err != nil || requests != 2 {
		t.Fatalf("unrelated request: %v, count=%d", err, requests)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := UserEvents(ctx, c, "alice", "3"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestEventCacheBoundedAndNetworkErrorRedacted(t *testing.T) {
	c, err := New("ghp_SUPERSECRETVALUE")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxEventValidators+10; i++ {
		c.updateEventEntry(fmt.Sprint(i), func(e *eventCacheEntry) { e.etag = "test" })
	}
	if len(c.eventCache) != maxEventValidators || len(c.eventOrder) != maxEventValidators {
		t.Fatalf("cache=%d order=%d", len(c.eventCache), len(c.eventOrder))
	}
	err = networkError(&net.DNSError{Err: "sensitive ghp_SUPERSECRETVALUE", Name: "secret.example"})
	if !strings.Contains(err.Error(), "DNS") || strings.Contains(err.Error(), "ghp_SUPERSECRETVALUE") || strings.Contains(err.Error(), "secret.example") {
		t.Fatal(err)
	}
}

func TestRateLimitSharedWithDiscoveryWithoutBlocking(t *testing.T) {
	requests := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	start := time.Now()
	_, _, err := UserEvents(context.Background(), c, "alice", "")
	var status *HTTPStatusError
	if !errors.As(err, &status) || status.StatusCode != 429 || status.RetryAfter < 59*time.Minute || time.Since(start) > time.Second {
		t.Fatalf("rate limit %v", err)
	}
	_, _, err = DiscoverRepositories(context.Background(), c, "alice", "")
	if !errors.As(err, &status) || status.StatusCode != 429 || requests != 1 || time.Since(start) > time.Second {
		t.Fatalf("discovery was not deferred: %v requests=%d", err, requests)
	}
}

func TestOrdinaryForbiddenDoesNotSetRateLimit(t *testing.T) {
	requests := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("X-RateLimit-Remaining", "99")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		w.WriteHeader(http.StatusForbidden)
	})
	for i := 0; i < 2; i++ {
		_, _, err := UserEvents(context.Background(), c, "alice", "")
		var status *HTTPStatusError
		if !errors.As(err, &status) || status.StatusCode != 403 || status.RetryAfter != 0 {
			t.Fatalf("forbidden: %v", err)
		}
	}
	if requests != 2 {
		t.Fatalf("ordinary forbidden deferred: %d requests", requests)
	}
}
