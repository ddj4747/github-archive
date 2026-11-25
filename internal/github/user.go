package github

import (
	"context"
	"errors"
)

func UserEvents(ctx context.Context, c *Client, login, cursor string) ([]Event, string, error) {
	if !validSegment(login) {
		return nil, "", errors.New("invalid user target")
	}
	return c.getEvents(ctx, "/users/"+login+"/events/public?per_page=30", cursor)
}
