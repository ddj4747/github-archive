package mirror

import (
	"fmt"
	"strings"
)

// ValidateGitHubRemote accepts only canonical HTTPS repository URLs.
func ValidateGitHubRemote(remote string) error {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(remote, prefix) || !strings.HasSuffix(remote, ".git") {
		return fmt.Errorf("invalid GitHub remote")
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(remote, prefix), ".git"), "/")
	if len(parts) != 2 || !segment(parts[0]) || !segment(parts[1]) {
		return fmt.Errorf("invalid GitHub remote")
	}
	return nil
}

func segment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
