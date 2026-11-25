package poll

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"githubarchive/internal/github"
	"githubarchive/internal/mirror"
	"githubarchive/internal/model"
	"githubarchive/internal/repolylog"
	"githubarchive/internal/store"
)

var fetchMirror = mirror.Fetch
var readSnapshot = mirror.ReadSnapshot
var diffRefs = mirror.Diff
var writeSnapshot = mirror.WriteSnapshot
var repoEvents = github.RepoEvents

func PollRepo(ctx context.Context, dataRoot, token string, repo model.Repository, db *store.Store, client *github.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := repo.RepoDir(dataRoot)
	if err != nil {
		return err
	}
	mirrorPath, err := repo.MirrorPath(dataRoot)
	if err != nil {
		return err
	}
	remote := fmt.Sprintf("https://github.com/%s/%s.git", repo.Owner, repo.Name)
	changes, snapshot, gitErr := pollGitRefs(ctx, dir, mirrorPath, remote, token, repo)
	if err := ctx.Err(); err != nil {
		return err
	}
	cursor, err := db.GetCursor(model.RepoEventsFeed(repo.Owner, repo.Name))
	if err != nil {
		return err
	}
	events, newest, apiErr := repoEvents(ctx, client, repo.Owner, repo.Name, cursor)
	if apiErr != nil {
		events, newest = nil, ""
		var deferred *github.PollDeferredError
		if errors.As(apiErr, &deferred) {
			apiErr = nil
		}
		if gitErr != nil {
			return errors.Join(gitErr, apiErr)
		}
	}
	log, err := repolylog.Open(dir)
	if err != nil {
		return err
	}
	defer log.Close()
	insertedEvents := make([]bool, len(events))
	insertedChanges := make([]bool, len(changes))
	err = db.InTx(func(tx *store.Tx) error {
		for i, event := range events {
			inserted, err := tx.InsertEvent(model.ActivityEvent{Scope: model.RepoScope(repo.Owner, repo.Name), EventID: event.ID, Type: event.Type, Login: event.Login, RepoOwner: event.RepoOwner, RepoName: event.RepoName, At: event.At, Raw: event.Raw})
			if err != nil {
				return err
			}
			insertedEvents[i] = inserted
		}
		for i, change := range changes {
			inserted, err := tx.InsertRefChange(change)
			if err != nil {
				return err
			}
			insertedChanges[i] = inserted
		}
		if newest != "" {
			if err := tx.SetCursor(model.RepoEventsFeed(repo.Owner, repo.Name), newest); err != nil {
				return err
			}
		}
		if gitErr == nil {
			return tx.SetCursor(model.RefsFeed(repo.Owner, repo.Name), time.Now().UTC().Format(time.RFC3339Nano))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if gitErr == nil {
		gitErr = writeSnapshot(dir, snapshot)
	}
	for i, event := range events {
		if insertedEvents[i] {
			if err := log.Append(redact(fmt.Sprintf("event id=%s type=%s", event.ID, event.Type), token)); err != nil {
				return errors.Join(gitErr, apiErr, err)
			}
		}
	}
	for i, change := range changes {
		if insertedChanges[i] {
			if err := log.Append(redact(fmt.Sprintf("ref=%s kind=%s old=%s new=%s", change.RefName, change.Kind, change.OldSHA, change.NewSHA), token)); err != nil {
				return errors.Join(gitErr, apiErr, err)
			}
		}
	}
	return errors.Join(gitErr, apiErr)
}

func pollGitRefs(ctx context.Context, dir, mirrorPath, remote, token string, repo model.Repository) ([]model.RefChange, map[string]string, error) {
	if err := fetchMirror(ctx, mirrorPath, remote, token); err != nil {
		return nil, nil, err
	}
	previous, err := readSnapshot(dir)
	if err != nil {
		return nil, nil, err
	}
	return diffRefs(ctx, mirrorPath, remote, token, repo, previous)
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}
