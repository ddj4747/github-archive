package poll

import (
	"context"
	"fmt"

	"githubarchive/internal/github"
	"githubarchive/internal/model"
	"githubarchive/internal/repolylog"
	"githubarchive/internal/store"
)

var userEvents = github.UserEvents

func PollUser(ctx context.Context, dataRoot, token string, user model.User, db *store.Store, client *github.Client) error {
	return pollUserFeed(ctx, dataRoot, token, user, db, client, model.UserEventsFeed(user.Login), userEvents)
}

func pollUserFeed(ctx context.Context, dataRoot, token string, user model.User, db *store.Store, client *github.Client, feed string, fetch func(context.Context, *github.Client, string, string) ([]github.Event, string, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := user.UserDir(dataRoot)
	if err != nil {
		return err
	}
	cursor, err := db.GetCursor(feed)
	if err != nil {
		return err
	}
	events, newest, err := fetch(ctx, client, user.Login, cursor)
	if err != nil {
		return err
	}
	log, err := repolylog.Open(dir)
	if err != nil {
		return err
	}
	defer log.Close()
	insertedEvents := make([]bool, len(events))
	err = db.InTx(func(tx *store.Tx) error {
		for i, event := range events {
			inserted, err := tx.InsertEvent(model.ActivityEvent{Scope: model.UserScope(user.Login), EventID: event.ID, Type: event.Type, Login: event.Login, RepoOwner: event.RepoOwner, RepoName: event.RepoName, At: event.At, Raw: event.Raw})
			if err != nil {
				return err
			}
			insertedEvents[i] = inserted
		}
		if newest != "" {
			return tx.SetCursor(feed, newest)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i, event := range events {
		if insertedEvents[i] {
			if err := log.Append(redact(fmt.Sprintf("event id=%s type=%s", event.ID, event.Type), token)); err != nil {
				return err
			}
		}
	}
	return nil
}
