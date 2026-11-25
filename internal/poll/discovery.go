package poll

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"githubarchive/internal/github"
	"githubarchive/internal/model"
	"githubarchive/internal/store"
)

var discoverRepositories = github.DiscoverRepositories

// PollDiscovery saves a page of discovered repositories and its continuation
// atomically. A non-empty continuation means another page is ready to fetch.
func PollDiscovery(ctx context.Context, user model.User, db *store.Store, client *github.Client) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	feed := "repo-discovery:" + user.Login
	cursor, err := db.GetCursor(feed)
	if err != nil {
		return false, err
	}
	repos, next, err := discoverRepositories(ctx, client, user.Login, cursor)
	if err != nil {
		// An installation token may not support /user/repos. Restart the public
		// scan next time instead of getting permanently stuck on that endpoint.
		var status *github.HTTPStatusError
		if strings.HasPrefix(cursor, "visible:") && errors.As(err, &status) && status.RetryAfter == 0 && (status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden || status.StatusCode == http.StatusNotFound) {
			if resetErr := db.SetCursor(feed, ""); resetErr != nil {
				return false, errors.Join(err, resetErr)
			}
		}
		return false, err
	}
	err = db.InTx(func(tx *store.Tx) error {
		for _, repo := range repos {
			if err := tx.UpsertDiscovered(repo.Owner, repo.Name, user.Login); err != nil {
				return err
			}
		}
		return tx.SetCursor(feed, next)
	})
	if err != nil {
		return false, err
	}
	return next != "", nil
}
