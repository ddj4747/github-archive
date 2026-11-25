package github

import (
	"context"
	"errors"
)

// AuthenticatedUserEvents fetches the authenticated user's activity feed.
// GitHub may include private events when the token belongs to login.
func AuthenticatedUserEvents(ctx context.Context, c *Client, login, cursor string) ([]Event, string, error) {
	if !validSegment(login) {
		return nil, "", errors.New("invalid user target")
	}
	return c.getEvents(ctx, "/users/"+login+"/events?per_page=30", cursor)
}
