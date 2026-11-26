package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidation(t *testing.T) {
	t.Setenv("TEST_GITHUB_ARCHIVE_TOKEN", "token")
	valid := "token_env: TEST_GITHUB_ARCHIVE_TOKEN\ndata_root: relative-data\npoll_interval: 9m\nrepositories:\n  - owner: A\n    name: b\nusers:\n  - login: abc\n    poll_interval: 0\n"
	load := func(s string) (Config, error) {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
		return Load(p)
	}
	c, err := load(valid)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.DataRoot) || c.Repositories[0].PollInterval != 9*time.Minute || c.Users[0].PollInterval != 9*time.Minute {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.Users[0].DiscoverRepositories {
		t.Fatal("repository discovery must default to false")
	}
	if c.Users[0].AuthenticatedEvents {
		t.Fatal("authenticated events must default to false")
	}
	exclude := strings.Replace(valid, "login: abc", "login: abc\n    exclude_repositories: [alpha, beta]", 1)
	c, err = load(exclude)
	if err != nil || len(c.Users[0].ExcludeRepositories) != 2 || c.Users[0].ExcludeRepositories[0] != "alpha" {
		t.Fatalf("excluded repositories: %+v %v", c.Users, err)
	}
	for _, v := range []struct {
		input string
		want  bool
	}{{"true", true}, {"false", false}} {
		configured := strings.Replace(valid, "login: abc", "login: abc\n    discover_repositories: "+v.input, 1)
		c, err := load(configured)
		if err != nil || c.Users[0].DiscoverRepositories != v.want {
			t.Fatalf("discovery %s: %+v, %v", v.input, c, err)
		}
	}
	for _, v := range []struct {
		input string
		want  bool
	}{{"true", true}, {"false", false}} {
		configured := strings.Replace(valid, "login: abc", "login: abc\n    authenticated_events: "+v.input, 1)
		c, err := load(configured)
		if err != nil || c.Users[0].AuthenticatedEvents != v.want {
			t.Fatalf("authenticated events %s: %+v, %v", v.input, c, err)
		}
	}
	for name, s := range map[string]string{
		"unknown":             valid + "extra: true\n",
		"nested unknown":      strings.Replace(valid, "name: b", "name: b\n    surprise: yes", 1),
		"literal token":       valid + "bad: ghp_secret\n",
		"numeric duration":    strings.Replace(valid, "9m", "300", 1),
		"negative duration":   strings.Replace(valid, "9m", "-1s", 1),
		"duplicate":           valid + "  - login: ABC\n",
		"traversal":           strings.Replace(valid, "owner: A", "owner: ..", 1),
		"invalid discovery":   strings.Replace(valid, "login: abc", "login: abc\n    discover_repositories: invalid", 1),
		"invalid auth events": strings.Replace(valid, "login: abc", "login: abc\n    authenticated_events: invalid", 1),
		"invalid exclusion":   strings.Replace(valid, "login: abc", "login: abc\n    exclude_repositories: ['../bad']", 1),
		"duplicate exclusion": strings.Replace(valid, "login: abc", "login: abc\n    exclude_repositories: [same, SAME]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(s); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadDoesNotRequireToken(t *testing.T) {
	t.Setenv("ARCHIVE_PANEL_TOKEN_UNSET", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("token_env: ARCHIVE_PANEL_TOKEN_UNSET\ndata_root: data\nusers:\n  - login: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadForEditing(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("data_root: data\nusers:\n  - login: alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.TokenEnv != "GITHUB_TOKEN" {
		t.Fatalf("default token environment: %+v, %v", got, err)
	}
}
