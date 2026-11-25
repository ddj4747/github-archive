package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DiscoveredRepository is a repository owned by the requested login.
type DiscoveredRepository struct {
	Owner, Name string
	Private     bool
}

// DiscoverRepositories fetches one bounded page. An empty cursor starts a scan;
// the returned empty cursor means the public and token-visible scans completed.
// Persist the returned cursor only after the repositories have been durably saved.
// The token-visible endpoint may include repositories belonging to other owners;
// those are filtered out rather than expanding the configured account's scope.
func DiscoverRepositories(ctx context.Context, c *Client, login, cursor string) ([]DiscoveredRepository, string, error) {
	if !validSegment(login) {
		return nil, "", errors.New("invalid user target")
	}
	mode, page, err := parseDiscoveryCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	path := "/users/" + login + "/repos"
	query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}, "sort": {"full_name"}, "direction": {"asc"}}
	if mode == "public" {
		query.Set("type", "owner")
	} else {
		path = "/user/repos"
		query.Set("visibility", "all")
	}
	u := c.base.ResolveReference(&url.URL{Path: path, RawQuery: query.Encode()})
	body, header, err := c.getPageManaged(ctx, u)
	if err != nil {
		return nil, "", err
	}
	var values []struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Private bool `json:"private"`
	}
	if err := json.Unmarshal(body, &values); err != nil || values == nil || len(values) > 100 {
		return nil, "", errors.New("invalid GitHub repository page")
	}
	repos := make([]DiscoveredRepository, 0, len(values))
	for _, value := range values {
		if !validSegment(value.Owner.Login) || !validSegment(value.Name) {
			return nil, "", errors.New("invalid GitHub repository entry")
		}
		if strings.EqualFold(login, value.Owner.Login) {
			repos = append(repos, DiscoveredRepository{Owner: value.Owner.Login, Name: value.Name, Private: value.Private})
		}
	}
	next, err := nextLink(header)
	if err != nil {
		return nil, "", err
	}
	if next != "" {
		ref, err := url.Parse(next)
		if err != nil {
			return nil, "", errors.New("invalid pagination link")
		}
		ref = u.ResolveReference(ref)
		nextPage, pageErr := strconv.Atoi(ref.Query().Get("page"))
		if !c.validURL(ref) || ref.Path != path || pageErr != nil || nextPage != page+1 {
			return nil, "", errors.New("invalid pagination destination")
		}
		return repos, fmt.Sprintf("%s:%d", mode, nextPage), nil
	}
	if mode == "public" {
		return repos, "visible:1", nil
	}
	return repos, "", nil
}

func parseDiscoveryCursor(cursor string) (string, int, error) {
	if cursor == "" {
		return "public", 1, nil
	}
	mode, value, ok := strings.Cut(cursor, ":")
	if !ok || (mode != "public" && mode != "visible") {
		return "", 0, errors.New("invalid discovery cursor")
	}
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 || strconv.Itoa(page) != value || page == int(^uint(0)>>1) {
		return "", 0, errors.New("invalid discovery cursor")
	}
	return mode, page, nil
}
