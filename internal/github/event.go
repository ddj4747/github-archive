package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Event preserves the complete API object along with the fields needed for indexing.
type Event struct {
	ID, Type, Login, RepoOwner, RepoName string
	At                                   time.Time
	Raw                                  string
	Public                               bool
}

// Since parses newest-first pages and returns events before the saved cursor.
func Since(pages [][]byte, cursor string) ([]Event, string, error) {
	var events []Event
	var newest string
	seenFirst, reached := false, false
	for _, page := range pages {
		var entries []json.RawMessage
		if err := json.Unmarshal(page, &entries); err != nil || entries == nil {
			return nil, "", fmt.Errorf("invalid GitHub events page")
		}
		for _, raw := range entries {
			var item struct {
				ID    string `json:"id"`
				Type  string `json:"type"`
				Actor struct {
					Login string `json:"login"`
				} `json:"actor"`
				Repo struct {
					Name string `json:"name"`
				} `json:"repo"`
				At     time.Time `json:"created_at"`
				Public bool      `json:"public"`
			}
			if len(raw) == 0 || raw[0] != '{' {
				return nil, "", fmt.Errorf("invalid GitHub event")
			}
			if err := json.Unmarshal(raw, &item); err != nil || item.ID == "" {
				return nil, "", fmt.Errorf("invalid GitHub event")
			}
			var owner, name string
			if item.Repo.Name != "" {
				var found bool
				owner, name, found = strings.Cut(item.Repo.Name, "/")
				if !found || owner == "" || name == "" || strings.Contains(name, "/") {
					return nil, "", fmt.Errorf("invalid GitHub event repository")
				}
			}
			if !seenFirst {
				newest, seenFirst = item.ID, true
			}
			if cursor != "" && item.ID == cursor {
				reached = true
			}
			if !reached {
				events = append(events, Event{ID: item.ID, Type: item.Type, Login: item.Actor.Login, RepoOwner: owner, RepoName: name, At: item.At, Raw: string(raw), Public: item.Public})
			}
		}
	}
	return events, newest, nil
}
