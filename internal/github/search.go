package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

type UserSearchResult struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
}

// SearchUsers returns a bounded page of public GitHub user search results.
func SearchUsers(ctx context.Context, client *Client, query string) ([]UserSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 100 {
		return nil, errors.New("enter a search term of at most 100 characters")
	}
	params := url.Values{"q": {query + " type:user"}, "per_page": {"10"}}
	u := client.base.ResolveReference(&url.URL{Path: "/search/users", RawQuery: params.Encode()})
	// Interactive search must return rate limits immediately, without retries.
	body, _, err := client.getPageWithRetries(ctx, u, 0)
	if err != nil {
		return nil, err
	}
	var result struct {
		Items []UserSearchResult `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Items == nil || len(result.Items) > 10 {
		return nil, errors.New("invalid GitHub user search response")
	}
	for i, user := range result.Items {
		if !validSegment(user.Login) {
			return nil, errors.New("invalid GitHub user search response")
		}
		result.Items[i].HTMLURL = "https://github.com/" + user.Login
		avatar, err := url.Parse(user.AvatarURL)
		if err != nil || avatar.Scheme != "https" || avatar.Host != "avatars.githubusercontent.com" || avatar.User != nil || avatar.Fragment != "" || avatar.Opaque != "" || !strings.HasPrefix(avatar.Path, "/") {
			result.Items[i].AvatarURL = ""
		}
	}
	return result.Items, nil
}
