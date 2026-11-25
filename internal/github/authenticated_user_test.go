package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAuthenticatedUserEvents(t *testing.T) {
	var paths []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		fmt.Fprint(w, `[{"id":"3","type":"PushEvent","public":false,"repo":{"name":"alice/secret"}},{"id":"2","public":true}]`)
	})
	events, newest, err := AuthenticatedUserEvents(context.Background(), c, "alice", "2")
	if err != nil || newest != "3" || len(events) != 1 || events[0].Public || !strings.Contains(events[0].Raw, `"public":false`) {
		t.Fatalf("events=%+v newest=%q error=%v", events, newest, err)
	}
	if len(paths) != 1 || paths[0] != "/users/alice/events?per_page=30" {
		t.Fatalf("unexpected paths %v", paths)
	}
}
