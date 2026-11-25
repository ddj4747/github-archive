package poll

import (
	"context"

	"githubarchive/internal/github"
	"githubarchive/internal/model"
	"githubarchive/internal/store"
)

var authenticatedUserEvents = github.AuthenticatedUserEvents

// PollAuthenticatedUser is independent of the public feed and its cursor.
func PollAuthenticatedUser(ctx context.Context, dataRoot, token string, user model.User, db *store.Store, client *github.Client) error {
	return pollUserFeed(ctx, dataRoot, token, user, db, client, "user-authenticated-events:"+user.Login, authenticatedUserEvents)
}
