package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSearchUsersPublicAndBounded(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/users" || r.URL.Query().Get("q") != "octo cat type:user" || r.URL.Query().Get("per_page") != "10" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("search should not send credentials")
		}
		fmt.Fprint(w, `{"items":[{"login":"octocat","avatar_url":"https://avatars.githubusercontent.com/example","html_url":"https://github.com/octocat"}]}`)
	})
	c.token = ""
	users, err := SearchUsers(context.Background(), c, " octo cat ")
	if err != nil || len(users) != 1 || users[0].Login != "octocat" {
		t.Fatalf("%+v %v", users, err)
	}
	if _, err := SearchUsers(context.Background(), c, ""); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestSearchRejectsUnsafeAvatarsAndSanitizesLinks(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"items":[{"login":"alice","avatar_url":"https://avatars.githubusercontent.com/u/12?v=4","html_url":"https://evil.test/"},{"login":"bob","avatar_url":"https://avatars.githubusercontent.com.evil.test/a"},{"login":"cat","avatar_url":"https://alice@avatars.githubusercontent.com/a"},{"login":"dog","avatar_url":"http://avatars.githubusercontent.com/a"}]}`)
	})
	users, err := SearchUsers(context.Background(), c, "test")
	if err != nil {
		t.Fatal(err)
	}
	if users[0].AvatarURL == "" || users[0].HTMLURL != "https://github.com/alice" {
		t.Fatalf("unexpected safe result: %+v", users[0])
	}
	for _, u := range users[1:] {
		if u.AvatarURL != "" || !strings.HasPrefix(u.HTMLURL, "https://github.com/") {
			t.Fatalf("unsafe user: %+v", u)
		}
	}
}

func TestSearchRateLimitReturnsWithoutRetry(t *testing.T) {
	calls := 0
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	start := time.Now()
	_, err := SearchUsers(context.Background(), c, "test")
	status, ok := err.(*HTTPStatusError)
	if !ok || status.StatusCode != 429 || status.RetryAfter != time.Hour || calls != 1 || time.Since(start) > 2*time.Second {
		t.Fatalf("%v, calls=%d", err, calls)
	}
}
