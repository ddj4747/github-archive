package model

import "time"

type ActivityEvent struct {
	Scope, EventID, Type, Login, RepoOwner, RepoName string
	At                                               time.Time
	Raw                                              string
}
type RefChange struct{ Owner, Name, RefName, OldSHA, NewSHA, Kind string }

const (
	Create      = "create"
	FastForward = "fast_forward"
	ForcePush   = "force_push"
	Delete      = "delete"
)

func RepoEventsFeed(owner, name string) string { return "repo-events:" + owner + "/" + name }
func UserEventsFeed(login string) string       { return "user-events:" + login }
func RefsFeed(owner, name string) string       { return "refs:" + owner + "/" + name }
func RepoScope(owner, name string) string      { return "repo:" + owner + "/" + name }
func UserScope(login string) string            { return "user:" + login }
