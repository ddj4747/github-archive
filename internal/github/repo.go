package github

import (
	"context"
	"errors"
)

func RepoEvents(ctx context.Context, c *Client, owner, name, cursor string) ([]Event, string, error) {
	if !validSegment(owner) || !validSegment(name) {
		return nil, "", errors.New("invalid repository target")
	}
	return c.getEvents(ctx, "/repos/"+owner+"/"+name+"/events?per_page=30", cursor)
}

func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
