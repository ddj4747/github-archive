package poll

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"githubarchive/internal/github"
	"githubarchive/internal/model"
	"githubarchive/internal/repolylog"
	"githubarchive/internal/store"
)

var runRepo = PollRepo
var runUser = PollUser
var runAuthenticatedUser = PollAuthenticatedUser
var runDiscovery = PollDiscovery

// Bound a single target so a stalled Git process cannot block the rest forever.
const targetTimeout = 30 * time.Minute

// Run visits targets serially, including after a target fails or panics.
func Run(ctx context.Context, dataRoot string, repos []model.Repository, users []model.User, token string, db *store.Store, client *github.Client) error {
	type target struct {
		interval time.Duration
		last     time.Time
		dir      func() (string, error)
		poll     func(context.Context) error
	}
	targets := make([]target, 0, len(repos)+len(users))
	configuredRepos := make(map[string]bool, len(repos))
	for _, r := range repos {
		r := r
		configuredRepos[strings.ToLower(r.Owner+"/"+r.Name)] = true
		targets = append(targets, target{interval: r.PollInterval, dir: func() (string, error) { return r.RepoDir(dataRoot) }, poll: func(pollCtx context.Context) error { return runRepo(pollCtx, dataRoot, token, r, db, client) }})
	}
	for _, u := range users {
		u := u
		targets = append(targets, target{interval: u.PollInterval, dir: func() (string, error) { return u.UserDir(dataRoot) }, poll: func(pollCtx context.Context) error { return runUser(pollCtx, dataRoot, token, u, db, client) }})
		if u.AuthenticatedEvents {
			targets = append(targets, target{interval: u.PollInterval, dir: func() (string, error) { return u.UserDir(dataRoot) }, poll: func(pollCtx context.Context) error {
				return runAuthenticatedUser(pollCtx, dataRoot, token, u, db, client)
			}})
		}
	}
	for i := range targets {
		if targets[i].interval <= 0 {
			targets[i].interval = model.DefaultPollInterval
		}
	}
	type discoveryTarget struct {
		user       model.User
		excluded   map[string]bool
		interval   time.Duration
		last       time.Time
		more       bool
		queueRetry time.Time
	}
	var discoveries []discoveryTarget
	for _, u := range users {
		if u.DiscoverRepositories {
			interval := u.PollInterval
			if interval <= 0 {
				interval = model.DefaultPollInterval
			}
			excluded := make(map[string]bool, len(u.ExcludeRepositories))
			for _, name := range u.ExcludeRepositories {
				excluded[strings.ToLower(name)] = true
			}
			discoveries = append(discoveries, discoveryTarget{user: u, interval: interval, excluded: excluded})
		}
	}
	if len(targets) == 0 {
		<-ctx.Done()
		return nil
	}
	for {
		for i := range targets {
			if ctx.Err() != nil {
				return nil
			}
			t := &targets[i]
			if !Due(t.last, t.interval, time.Now()) {
				continue
			}
			runTarget(ctx, t.dir, token, t.poll)
			t.last = time.Now()
		}
		// Scan a page per user, then poll at most one queued repository per user.
		// Re-entering the outer loop gives already configured targets a chance to run.
		for i := range discoveries {
			if ctx.Err() != nil {
				return nil
			}
			d := &discoveries[i]
			dir := func() (string, error) { return d.user.UserDir(dataRoot) }
			if d.more || Due(d.last, d.interval, time.Now()) {
				d.more = false
				runTarget(ctx, dir, token, func(pollCtx context.Context) error {
					more, err := runDiscovery(pollCtx, d.user, db, client)
					if err == nil {
						d.more = more
					}
					return err
				})
				d.last = time.Now()
			}
			if ctx.Err() != nil {
				return nil
			}
			if time.Now().Before(d.queueRetry) {
				continue
			}
			repo, last, found, err := db.NextDiscovered(d.user.Login)
			if err != nil {
				logFailure(dir, token, err.Error())
				d.queueRetry = time.Now().Add(d.interval)
				continue
			}
			if !found || !Due(last, d.interval, time.Now()) {
				continue
			}
			if !configuredRepos[strings.ToLower(repo.Owner+"/"+repo.Name)] && !d.excluded[strings.ToLower(repo.Name)] {
				repo.PollInterval = d.interval
				runTarget(ctx, func() (string, error) { return repo.RepoDir(dataRoot) }, token, func(pollCtx context.Context) error { return runRepo(pollCtx, dataRoot, token, repo, db, client) })
			}
			if ctx.Err() != nil {
				return nil
			}
			if err := db.MarkDiscoveredPolled(repo.Owner, repo.Name, time.Now()); err != nil {
				logFailure(dir, token, err.Error())
				d.queueRetry = time.Now().Add(d.interval)
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		now := time.Now()
		next := targets[0].last.Add(targets[0].interval)
		for i := 1; i < len(targets); i++ {
			if due := targets[i].last.Add(targets[i].interval); due.Before(next) {
				next = due
			}
		}
		for i := range discoveries {
			d := &discoveries[i]
			due := d.last.Add(d.interval)
			if d.more {
				due = now
			}
			if due.Before(next) {
				next = due
			}
			if now.Before(d.queueRetry) {
				if d.queueRetry.Before(next) {
					next = d.queueRetry
				}
				continue
			}
			_, last, found, err := db.NextDiscovered(d.user.Login)
			if err != nil {
				logFailure(func() (string, error) { return d.user.UserDir(dataRoot) }, token, err.Error())
				d.queueRetry = now.Add(d.interval)
				if d.queueRetry.Before(next) {
					next = d.queueRetry
				}
			} else if found {
				due = last.Add(d.interval)
				if last.IsZero() {
					due = now
				}
				if due.Before(next) {
					next = due
				}
			}
		}
		if err := Wait(ctx, next.Sub(now)); err != nil {
			return nil
		}
	}
}

func runTarget(ctx context.Context, dir func() (string, error), token string, poll func(context.Context) error) {
	pollCtx, cancel := context.WithTimeout(ctx, targetTimeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			logFailure(dir, token, fmt.Sprintf("panic: %v", p))
		}
	}()
	if err := poll(pollCtx); err != nil && ctx.Err() == nil {
		var deferred *github.PollDeferredError
		if !errors.As(err, &deferred) {
			logFailure(dir, token, err.Error())
		}
	}
}

func logFailure(dir func() (string, error), token, message string) {
	if token != "" {
		message = strings.ReplaceAll(message, token, "[redacted]")
	}
	path, err := dir()
	if err != nil {
		return
	}
	log, err := repolylog.Open(path)
	if err != nil {
		return
	}
	defer log.Close()
	_ = log.Append("error: " + message)
}
