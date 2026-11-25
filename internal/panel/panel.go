// Package panel serves a local interface to an existing archive.
package panel

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"githubarchive/internal/config"
	"githubarchive/internal/store"
	"gopkg.in/yaml.v3"
	"modernc.org/sqlite"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	configPath, root string
	db               *sql.DB
	mu               sync.Mutex
	handler          http.Handler
	allowBridge      bool
	trustedOrigin    string
	stopRefIndex     context.CancelFunc
	refIndexDone     chan struct{}
}

type repository struct {
	Owner        string `json:"owner" yaml:"owner"`
	Name         string `json:"name" yaml:"name"`
	PollInterval string `json:"poll_interval" yaml:"poll_interval,omitempty"`
}
type user struct {
	Login               string   `json:"login" yaml:"login"`
	PollInterval        string   `json:"poll_interval" yaml:"poll_interval,omitempty"`
	Discover            bool     `json:"discover_repositories" yaml:"discover_repositories,omitempty"`
	Authenticated       bool     `json:"authenticated_events" yaml:"authenticated_events,omitempty"`
	ExcludeRepositories []string `json:"exclude_repositories" yaml:"exclude_repositories,omitempty"`
}
type configView struct {
	TokenEnv     string       `json:"token_env"`
	PollInterval string       `json:"poll_interval"`
	DataRoot     string       `json:"data_root"`
	Repositories []repository `json:"repositories"`
	Users        []user       `json:"users"`
}

// Open initializes the panel against the same SQLite archive as the worker.
func Open(path string) (*Server, error) {
	return open(path, false, "")
}

// OpenDocker permits requests from private bridge addresses as well as loopback.
// Container peers on the bridge are trusted; publish the port only on loopback.
func OpenDocker(path string) (*Server, error) {
	return open(path, true, "")
}

// OpenDockerWithOrigin allows authenticated reverse-proxy requests from exactly
// one HTTPS browser origin. The proxy must protect every route.
func OpenDockerWithOrigin(path, origin string) (*Server, error) {
	if err := ValidateTrustedOrigin(origin); err != nil {
		return nil, err
	}
	return open(path, true, origin)
}

// ValidateTrustedOrigin accepts an HTTPS origin with a DNS hostname only.
func ValidateTrustedOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Port() != "" || net.ParseIP(u.Hostname()) != nil {
		return errors.New("trusted origin must be https:// followed by a DNS hostname")
	}
	host := u.Hostname()
	if len(host) > 253 || !strings.Contains(host, ".") {
		return errors.New("trusted origin must use a DNS hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid trusted origin hostname")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("invalid trusted origin hostname")
			}
		}
	}
	return nil
}

func open(path string, allowBridge bool, trustedOrigin string) (*Server, error) {
	c, err := config.LoadForEditing(path)
	if err != nil {
		return nil, err
	}
	s, err := store.Open(c.DataRoot)
	if err != nil {
		return nil, err
	}
	if err = s.Close(); err != nil {
		return nil, err
	}
	sqlite.StrictPragmas(true)
	db, err := sql.Open("sqlite", filepath.Join(c.DataRoot, "archive.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	p := &Server{configPath: path, root: c.DataRoot, db: db, allowBridge: allowBridge, trustedOrigin: trustedOrigin}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", p.configHandler)
	mux.HandleFunc("/api/events", p.events)
	mux.HandleFunc("/api/filter-options", p.filterOptions)
	mux.HandleFunc("/api/event-state", p.eventState)
	mux.HandleFunc("/api/ref-state", p.refState)
	mux.HandleFunc("/api/repository/commit-bundle", p.commitBundle)
	mux.HandleFunc("/api/refs", p.refs)
	mux.HandleFunc("/api/recent-ref-changes", p.recentRefChanges)
	mux.HandleFunc("/api/targets", p.targets)
	mux.HandleFunc("/api/log", p.log)
	mux.HandleFunc("/api/search/users", p.searchUsers)
	mux.HandleFunc("/api/repository/history", p.repositoryHistory)
	mux.HandleFunc("/api/repository/tree", p.repositoryTree)
	mux.HandleFunc("/api/repository/file", p.repositoryFile)
	mux.HandleFunc("/api/repository/snapshot", p.repositorySnapshot)
	mux.HandleFunc("/api/stats", p.stats)
	mux.HandleFunc("/api/export/events", p.exportEvents)
	mux.HandleFunc("/api/export/refs", p.exportRefs)
	mux.HandleFunc("/api/repository/bundle", p.bundle)
	files, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(files)))
	p.handler = p.guard(mux)
	ctx, cancel := context.WithCancel(context.Background())
	p.stopRefIndex, p.refIndexDone = cancel, make(chan struct{})
	go func() {
		defer close(p.refIndexDone)
		p.indexRefTipDates(ctx)
	}()
	return p, nil
}

// ValidateDockerListen permits the container wildcard only in explicit Docker mode.
func ValidateDockerListen(addr string) error {
	if ValidateListen(addr) == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host != "0.0.0.0" {
		return errors.New("Docker listen address must be 0.0.0.0 or a loopback IP")
	}
	return nil
}
func (p *Server) Close() error {
	if p.stopRefIndex != nil {
		p.stopRefIndex()
		<-p.refIndexDone
	}
	return p.db.Close()
}
func (p *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { p.handler.ServeHTTP(w, r) }

// ValidateListen permits only literal loopback addresses (not wildcard hosts).
func ValidateListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("invalid listen address")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address must be a loopback IP")
	}
	return nil
}
func validHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	} else if strings.Contains(hostport, ":") {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func (p *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://avatars.githubusercontent.com; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !(ip.IsLoopback() || p.allowBridge && ip.IsPrivate()) || !validHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			o, err := url.Parse(r.Header.Get("Origin"))
			localOrigin := err == nil && o.Scheme == "http" && strings.EqualFold(o.Host, r.Host)
			remoteOrigin := err == nil && p.trustedOrigin != "" && strings.EqualFold(o.String(), p.trustedOrigin)
			if err != nil || !(localOrigin || remoteOrigin) || o.User != nil || o.Path != "" || o.RawQuery != "" || o.ForceQuery || o.Fragment != "" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func onlyGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return false
	}
	return true
}

func view(c config.Config) configView {
	v := configView{TokenEnv: c.TokenEnv, PollInterval: c.PollInterval.String(), DataRoot: c.DataRoot, Repositories: []repository{}, Users: []user{}}
	for _, r := range c.Repositories {
		v.Repositories = append(v.Repositories, repository{r.Owner, r.Name, ""})
	}
	for _, u := range c.Users {
		exclusions := append([]string{}, u.ExcludeRepositories...)
		v.Users = append(v.Users, user{u.Login, "", u.DiscoverRepositories, u.AuthenticatedEvents, exclusions})
	}
	return v
}
func (p *Server) configView() (configView, error) {
	c, err := config.LoadForEditing(p.configPath)
	if err != nil {
		return configView{}, err
	}
	b, err := os.ReadFile(p.configPath)
	if err != nil {
		return configView{}, err
	}
	var raw struct {
		Repositories []repository `yaml:"repositories"`
		Users        []user       `yaml:"users"`
	}
	if err = yaml.Unmarshal(b, &raw); err != nil {
		return configView{}, err
	}
	v := view(c)
	for i := range v.Repositories {
		if i < len(raw.Repositories) {
			v.Repositories[i].PollInterval = raw.Repositories[i].PollInterval
		}
	}
	for i := range v.Users {
		if i < len(raw.Users) {
			v.Users[i].PollInterval = raw.Users[i].PollInterval
		}
	}
	return v, nil
}
func (p *Server) configHandler(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Method == http.MethodGet {
		v, err := p.configView()
		if err != nil {
			http.Error(w, "cannot read configuration", 500)
			return
		}
		jsonReply(w, v)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" && !strings.HasPrefix(ct, "application/json;") {
		http.Error(w, "JSON required", 415)
		return
	}
	var v configView
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		http.Error(w, "invalid configuration", 400)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, "invalid configuration", 400)
		return
	}
	if err := p.saveTargets(v); err != nil {
		http.Error(w, "invalid configuration", 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (p *Server) saveTargets(v configView) error {
	b, err := os.ReadFile(p.configPath)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(b, &doc); err != nil {
		return err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("invalid config")
	}
	m := doc.Content[0]
	for _, entry := range []struct {
		key   string
		value any
	}{{"repositories", v.Repositories}, {"users", v.Users}} {
		var n yaml.Node
		if err = n.Encode(entry.value); err != nil {
			return err
		}
		found := false
		for i := 0; i < len(m.Content); i += 2 {
			if m.Content[i].Value == entry.key {
				m.Content[i+1] = &n
				found = true
				break
			}
		}
		if !found {
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: entry.key}, &n)
		}
	}
	b, err = yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.configPath)
	f, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if _, err = config.LoadForEditing(name); err != nil {
		return err
	}
	return os.Rename(name, p.configPath)
}
