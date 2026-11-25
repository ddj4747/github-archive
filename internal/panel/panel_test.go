package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"githubarchive/internal/config"
	"githubarchive/internal/model"
	"githubarchive/internal/store"
)

func request(t *testing.T, p *Server, method, path, host, origin, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	r.Host = host
	r.RemoteAddr = "127.0.0.1:54321"
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func TestDockerBridgeGuard(t *testing.T) {
	if ValidateListen("0.0.0.0:8765") == nil {
		t.Fatal("ordinary mode accepted wildcard")
	}
	if err := ValidateDockerListen("0.0.0.0:8765"); err != nil {
		t.Fatal(err)
	}
	if ValidateDockerListen("192.168.1.2:8765") == nil {
		t.Fatal("Docker mode accepted arbitrary interface")
	}
	allowed := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		name, remote, host, origin, method string
		docker                             bool
		want                               int
	}{
		{"local", "127.0.0.1:1", "localhost:8765", "", "GET", false, 204},
		{"bridge disabled", "172.20.0.1:1", "localhost:8765", "", "GET", false, 403},
		{"bridge enabled", "172.20.0.1:1", "localhost:8765", "", "GET", true, 204},
		{"bridge write", "172.20.0.1:1", "localhost:8765", "http://localhost:8765", "PUT", true, 204},
		{"public peer", "8.8.8.8:1", "localhost:8765", "", "GET", true, 403},
		{"hostile host", "172.20.0.1:1", "evil.example", "", "GET", true, 403},
		{"hostile origin", "172.20.0.1:1", "localhost:8765", "http://evil.example", "PUT", true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://localhost:8765/", nil)
			r.RemoteAddr = tc.remote
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			(&Server{allowBridge: tc.docker}).guard(allowed).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}

func TestProxyOriginIsExplicitAndExact(t *testing.T) {
	for _, bad := range []string{"", "http://archive.example.com", "https://archive.example.com/", "https://archive.example.com:444", "https://archive.example.com?", "https://user@archive.example.com", "https://127.0.0.1", "https://localhost", "https://archive.example.com.evil/path"} {
		if ValidateTrustedOrigin(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := ValidateTrustedOrigin("https://archive.example.com"); err != nil {
		t.Fatal(err)
	}
	server := &Server{allowBridge: true, trustedOrigin: "https://archive.example.com"}
	handler := server.guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		origin string
		want   int
	}{{"https://archive.example.com", 204}, {"https://archive.example.com.evil", 403}, {"http://archive.example.com", 403}, {"https://archive.example.com:443", 403}, {"http://localhost:8765", 204}, {"", 403}} {
		r := httptest.NewRequest("PUT", "http://localhost:8765/", nil)
		r.RemoteAddr = "172.20.0.2:1234"
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("origin %q: got %d", tc.origin, w.Code)
		}
	}
}

func TestStatsWorkerHeartbeat(t *testing.T) {
	root := t.TempDir()
	dbRoot := filepath.Join(root, "data")
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("token_env: UNUSED\ndata_root: '"+filepath.ToSlash(dbRoot)+"'\nusers:\n  - login: user\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	check := func(running bool, hasTime bool) {
		t.Helper()
		w := request(t, p, "GET", "/api/stats", "localhost:8765", "", "")
		var stats struct {
			Running  bool   `json:"worker_running"`
			LastSeen string `json:"worker_last_seen"`
		}
		if w.Code != 200 {
			t.Fatalf("stats: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
			t.Fatal(err)
		}
		if stats.Running != running || (stats.LastSeen != "") != hasTime {
			t.Fatalf("worker stats: %+v", stats)
		}
	}
	check(false, false)
	if err := s.MarkHeartbeat(time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	check(false, true)
	if err := s.MarkHeartbeat(time.Now()); err != nil {
		t.Fatal(err)
	}
	check(true, true)
	if err := s.StopHeartbeat(); err != nil {
		t.Fatal(err)
	}
	check(false, true)
}
func TestPanelConfigArchiveAndGuards(t *testing.T) {
	t.Setenv("PANEL_MISSING_TOKEN", "")
	root := t.TempDir()
	dbRoot := filepath.Join(root, "data")
	path := filepath.Join(root, "config.yaml")
	body := "token_env: PANEL_MISSING_TOKEN\npoll_interval: 5m\ndata_root: " + dbRoot + "\nrepositories:\n  - owner: octocat\n    name: hi\nusers:\n  - login: hubot\n    discover_repositories: true\n    exclude_repositories: [skip]\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.InsertEvent(model.ActivityEvent{Scope: "user:hubot", EventID: "=formula", Type: "PushEvent", Login: "hubot", At: time.Now(), Raw: `{"public":true}`}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InsertRefChange(model.RefChange{Owner: "octocat", Name: "hi", RefName: "refs/heads/main", OldSHA: "", NewSHA: "abc", Kind: "create"}); err != nil {
		t.Fatal(err)
	}
	if err = s.InTx(func(tx *store.Tx) error {
		for _, name := range []string{"HI", "other", "third"} {
			if err := tx.UpsertDiscovered("octocat", name, "hubot"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	w := request(t, p, "GET", "/api/config", "localhost:8765", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var v configView
	if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Users) != 1 || len(v.Users[0].ExcludeRepositories) != 1 {
		t.Fatalf("view %+v", v)
	}
	if v.Users[0].PollInterval != "" || v.Repositories[0].PollInterval != "" {
		t.Fatalf("inherited intervals lost: %+v", v)
	}
	w = request(t, p, "GET", "/api/targets?limit=1&offset=1", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"other"`) || !strings.Contains(w.Body.String(), `"has_more":true`) {
		t.Fatalf("targets: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/targets?q=OCTOCAT%2FOTH&status=discovered&user_q=HUB", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"other"`) || !strings.Contains(w.Body.String(), `"status":"discovered"`) || !strings.Contains(w.Body.String(), `"login":"hubot"`) || strings.Contains(w.Body.String(), `"name":"third"`) {
		t.Fatalf("target filters: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/recent-ref-changes?repo_owner=OCTOCAT&repo_name=HI", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"detected_at":`) || !strings.Contains(w.Body.String(), "refs/heads/main") {
		t.Fatalf("timestamped ref changes: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/stats", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"repositories":3`) {
		t.Fatalf("stats: %d %s", w.Code, w.Body.String())
	}
	for _, c := range []struct{ host, origin string }{{"evil.example:8765", ""}, {"localhost:8765", "http://evil.example:8765"}} {
		w = request(t, p, "PUT", "/api/config", c.host, c.origin, "{}")
		if w.Code != 403 {
			t.Fatalf("guard: %d", w.Code)
		}
	}
	w = request(t, p, "GET", "/api/events?scope=user%3Ahubot&limit=1", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "=formula") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/export/events?format=csv", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "'=formula") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/refs?owner=OCTOCAT", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "refs/heads/main") {
		t.Fatal(w.Code, w.Body.String())
	}
	v.TokenEnv = "ignored"
	v.DataRoot = "ignored"
	v.PollInterval = "1s"
	v.Users[0].ExcludeRepositories = append(v.Users[0].ExcludeRepositories, "other")
	raw, _ := json.Marshal(v)
	w = request(t, p, "PUT", "/api/config", "localhost:8765", "http://localhost:8765", string(raw))
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	c, err := config.LoadForEditing(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.TokenEnv != "PANEL_MISSING_TOKEN" || c.DataRoot != dbRoot || c.PollInterval != 5*time.Minute || len(c.Users[0].ExcludeRepositories) != 2 {
		t.Fatalf("saved %+v", c)
	}
	w = request(t, p, "GET", "/api/targets?status=paused&user_q=unmatched", "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"other"`) || !strings.Contains(w.Body.String(), `"status":"paused"`) || !strings.Contains(w.Body.String(), `"users":[]`) {
		t.Fatalf("paused filters: %d %s", w.Code, w.Body.String())
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(updated, []byte("poll_interval: 5m0s")) {
		t.Fatalf("inherited target intervals became explicit: %s", updated)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v.Users[0].Login = "../bad"
	raw, _ = json.Marshal(v)
	w = request(t, p, "PUT", "/api/config", "localhost:8765", "http://localhost:8765", string(raw))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid update changed config", err)
	}
	ctxReq := httptest.NewRequest("GET", "http://localhost/api/events", nil)
	ctx, cancel := context.WithCancel(ctxReq.Context())
	cancel()
	ctxReq = ctxReq.WithContext(ctx)
	ctxReq.RemoteAddr = "127.0.0.1:1"
	w = httptest.NewRecorder()
	p.ServeHTTP(w, ctxReq)
	_, _ = io.ReadAll(w.Result().Body)
}

func TestValidateListen(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8765", "example.com:8765", "localhost:8765"} {
		if ValidateListen(addr) == nil {
			t.Fatal(addr)
		}
	}
	if err := ValidateListen("127.0.0.1:8765"); err != nil {
		t.Fatal(err)
	}
}
