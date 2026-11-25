package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"githubarchive/internal/model"
	"gopkg.in/yaml.v3"
)

type Config struct {
	TokenEnv     string
	PollInterval time.Duration
	DataRoot     string
	Repositories []model.Repository
	Users        []model.User
}
type duration time.Duration

func (d *duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || (n.Tag != "!!str" && !(n.Tag == "!!int" && n.Value == "0")) {
		return fmt.Errorf("interval must be a duration string")
	}
	if n.Value == "0" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil || v < 0 {
		return fmt.Errorf("invalid poll interval")
	}
	*d = duration(v)
	return nil
}

type repoConfig struct {
	Owner        string   `yaml:"owner"`
	Name         string   `yaml:"name"`
	PollInterval duration `yaml:"poll_interval"`
}
type userConfig struct {
	Login                string   `yaml:"login"`
	PollInterval         duration `yaml:"poll_interval"`
	DiscoverRepositories bool     `yaml:"discover_repositories"`
	AuthenticatedEvents  bool     `yaml:"authenticated_events"`
	ExcludeRepositories  []string `yaml:"exclude_repositories"`
}
type fileConfig struct {
	TokenEnv     string       `yaml:"token_env"`
	PollInterval duration     `yaml:"poll_interval"`
	DataRoot     string       `yaml:"data_root"`
	Repositories []repoConfig `yaml:"repositories"`
	Users        []userConfig `yaml:"users"`
}

func hasToken(n *yaml.Node) bool {
	if n.Kind == yaml.ScalarNode {
		for _, prefix := range []string{"ghp_", "github_pat_", "gho_", "ghu_", "ghs_", "ghr_"} {
			if strings.Contains(n.Value, prefix) {
				return true
			}
		}
	}
	for _, c := range n.Content {
		if hasToken(c) {
			return true
		}
	}
	return false
}

func Load(path string) (Config, error) {
	return load(path, true)
}

// LoadForEditing validates a panel configuration without needing the secret
// token in the panel process. The archiver itself always uses Load.
func LoadForEditing(path string) (Config, error) {
	return load(path, false)
}

func load(path string, requireToken bool) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var node yaml.Node
	if err = yaml.Unmarshal(b, &node); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	if hasToken(&node) {
		return Config{}, fmt.Errorf("configuration contains a token")
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var f fileConfig
	if err = dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	var extra yaml.Node
	if err = dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("multiple configuration documents")
		}
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	if strings.TrimSpace(f.TokenEnv) == "" || (requireToken && strings.TrimSpace(os.Getenv(f.TokenEnv)) == "") {
		return Config{}, fmt.Errorf("token environment variable is missing or empty")
	}
	if strings.TrimSpace(f.DataRoot) == "" {
		return Config{}, fmt.Errorf("data_root is required")
	}
	root, err := filepath.Abs(f.DataRoot)
	if err != nil {
		return Config{}, fmt.Errorf("invalid data_root: %w", err)
	}
	interval := time.Duration(f.PollInterval)
	if interval == 0 {
		interval = model.DefaultPollInterval
	}
	c := Config{TokenEnv: f.TokenEnv, DataRoot: root, PollInterval: interval}
	if len(f.Repositories) == 0 && len(f.Users) == 0 {
		return Config{}, fmt.Errorf("at least one target is required")
	}
	seen := make(map[string]bool)
	for _, r := range f.Repositories {
		if !model.ValidSegment(r.Owner) || !model.ValidSegment(r.Name) {
			return Config{}, fmt.Errorf("invalid repository")
		}
		key := strings.ToLower(r.Owner + "/" + r.Name)
		if seen[key] {
			return Config{}, fmt.Errorf("duplicate repository")
		}
		seen[key] = true
		d := time.Duration(r.PollInterval)
		if d == 0 {
			d = interval
		}
		c.Repositories = append(c.Repositories, model.Repository{Owner: r.Owner, Name: r.Name, PollInterval: d})
	}
	seen = make(map[string]bool)
	for _, u := range f.Users {
		if !model.ValidSegment(u.Login) {
			return Config{}, fmt.Errorf("invalid user")
		}
		key := strings.ToLower(u.Login)
		if seen[key] {
			return Config{}, fmt.Errorf("duplicate user")
		}
		seen[key] = true
		d := time.Duration(u.PollInterval)
		if d == 0 {
			d = interval
		}
		excluded := make(map[string]bool, len(u.ExcludeRepositories))
		for _, name := range u.ExcludeRepositories {
			key := strings.ToLower(name)
			if !model.ValidSegment(name) || excluded[key] {
				return Config{}, fmt.Errorf("invalid or duplicate excluded repository")
			}
			excluded[key] = true
		}
		c.Users = append(c.Users, model.User{Login: u.Login, PollInterval: d, DiscoverRepositories: u.DiscoverRepositories, AuthenticatedEvents: u.AuthenticatedEvents, ExcludeRepositories: u.ExcludeRepositories})
	}
	return c, nil
}
