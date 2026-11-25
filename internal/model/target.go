package model

import (
	"fmt"
	"path/filepath"
	"time"
)

const DefaultPollInterval = time.Minute

// ValidSegment accepts a single safe repository or user path segment.
func ValidSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

type Repository struct {
	Owner, Name  string
	PollInterval time.Duration
}
type User struct {
	Login                string
	PollInterval         time.Duration
	DiscoverRepositories bool
	AuthenticatedEvents  bool
	ExcludeRepositories  []string
}

func (r Repository) RepoDir(root string) (string, error) {
	if !ValidSegment(r.Owner) || !ValidSegment(r.Name) {
		return "", fmt.Errorf("invalid repository path segment")
	}
	return filepath.Join(root, "repos", r.Owner, r.Name), nil
}
func (r Repository) MirrorPath(root string) (string, error) {
	dir, err := r.RepoDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mirror.git"), nil
}
func (u User) UserDir(root string) (string, error) {
	if !ValidSegment(u.Login) {
		return "", fmt.Errorf("invalid user path segment")
	}
	return filepath.Join(root, "users", u.Login), nil
}
