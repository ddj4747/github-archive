package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefDateFiltersAndIndependentTargetPages(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config.yaml")
	body := "token_env: UNUSED\ndata_root: '" + filepath.ToSlash(filepath.Join(root, "data")) + "'\nrepositories:\n"
	for _, name := range []string{"one", "two", "three", "four", "five", "six"} {
		body += "  - owner: octocat\n    name: " + name + "\n"
	}
	body += "users: [\n"
	for _, login := range []string{"person1", "person2", "person3", "person4", "person5", "person6"} {
		body += "  {login: " + login + "},\n"
	}
	body += "]\n"
	if err := os.WriteFile(cfg, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	w := request(t, p, "GET", "/api/targets?limit=5&offset=5&user_limit=5", "localhost:8765", "", "")
	var targets struct {
		Repositories  []repository
		Users         []user
		HasMore       bool `json:"has_more"`
		UsersHaveMore bool `json:"users_have_more"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &targets) != nil || len(targets.Repositories) != 1 || targets.Repositories[0].Name != "six" || targets.HasMore || len(targets.Users) != 5 || !targets.UsersHaveMore {
		t.Fatalf("repository second page and people first page: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/targets?limit=5&user_limit=5&user_offset=5&user_q=PERSON", "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &targets) != nil || len(targets.Repositories) != 5 || !targets.HasMore || len(targets.Users) != 1 || targets.Users[0].Login != "person6" || targets.UsersHaveMore {
		t.Fatalf("repository first page and people second page: %d %s", w.Code, w.Body.String())
	}

	_, err = p.db.Exec(`INSERT INTO ref_changes VALUES
		('octocat','one','refs/heads/feature-new','old1','new1','force_push'),
		('octocat','one','refs/heads/feature-old','old2','new2','force_push'),
		('octocat','one','refs/heads/feature-undated','old3','','delete'),
		('octocat','one','refs/heads/main','','new4','create');
		DELETE FROM ref_observations WHERE old_sha='old3';
		UPDATE ref_observations SET detected_at='2026-11-22T12:00:00.000Z' WHERE old_sha='old1';
		UPDATE ref_observations SET detected_at='2026-11-21T12:00:00.000Z' WHERE old_sha='old2';`)
	if err != nil {
		t.Fatal(err)
	}
	var refs struct{ Items []refRow }
	w = request(t, p, "GET", "/api/refs?ref=feature&sort=oldest", "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &refs) != nil || len(refs.Items) != 3 || refs.Items[0].OldSHA != "old2" || refs.Items[1].OldSHA != "old1" || refs.Items[2].DetectedAt != "" {
		t.Fatalf("ref order, including undated last: %d %s", w.Code, w.Body.String())
	}
	filters := "owner=OCTOCAT&name=ONE&ref=feature&kind=force_push&sort=newest&from=2026-11-22T00:00:00Z&before=2026-11-23T00:00:00Z"
	w = request(t, p, "GET", "/api/refs?"+filters, "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &refs) != nil || len(refs.Items) != 1 || refs.Items[0].OldSHA != "old1" {
		t.Fatalf("ref date range: %d %s", w.Code, w.Body.String())
	}
	w = request(t, p, "GET", "/api/export/refs?format=csv&"+filters, "localhost:8765", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "detected_at") || !strings.Contains(w.Body.String(), "2026-11-22T12:00:00.000Z") || strings.Contains(w.Body.String(), "old2") {
		t.Fatalf("ref export date range: %d %s", w.Code, w.Body.String())
	}
	for _, query := range []string{
		"repository=OCTOCAT%2FONE&ref=FEATURE",
		"name=OCTOCAT%2FONE&ref=feature",
		"repository=ON&ref=feature",
	} {
		w = request(t, p, "GET", "/api/refs?"+query, "localhost:8765", "", "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &refs) != nil || len(refs.Items) != 3 {
			t.Fatalf("repository and branch search %s: %d %s", query, w.Code, w.Body.String())
		}
	}
	query := "repository=octocat%2Fone&ref=feature&sort=newest&from=2026-11-22T00:00:00Z&before=2026-11-23T00:00:00Z"
	w = request(t, p, "GET", "/api/refs?"+query+"&include_undated=1", "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &refs) != nil || len(refs.Items) != 2 || refs.Items[0].OldSHA != "old1" || refs.Items[1].OldSHA != "old3" {
		t.Fatalf("date range with undated changes: %d %s", w.Code, w.Body.String())
	}
	_, err = p.db.Exec(`INSERT INTO ref_changes VALUES ('octocat','one','refs/heads/feature-legacy','old4','','delete');
		DELETE FROM ref_observations WHERE old_sha='old4';`)
	if err != nil {
		t.Fatal(err)
	}
	w = request(t, p, "GET", "/api/refs?kind=delete&sort=newest", "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &refs) != nil || len(refs.Items) != 2 || refs.Items[0].OldSHA != "old4" {
		t.Fatalf("newest archived record first when dates are unknown: %d %s", w.Code, w.Body.String())
	}
	_, err = p.db.Exec(`INSERT INTO ref_changes VALUES
		('octocat','two','refs/heads/main','start','target','force_push'),
		('octocat','two','refs/heads/other','','fallback','create');
		DELETE FROM ref_observations WHERE name='two';
		INSERT INTO ref_tip_dates VALUES ('octocat','two','target','2026-11-01T12:00:00Z'),
			('octocat','two','fallback','2026-11-05T12:00:00Z');
		INSERT INTO activity_events VALUES
			('repo:octocat/two','40','PushEvent','Ada','octocat','two','2026-11-09T12:00:00Z',
			'{"payload":{"ref":"refs/heads/main","before":"start","head":"target"}}'),
			('repo:octocat/two','41','PushEvent','Ada','octocat','two','2026-11-04T12:00:00Z',
			'{"payload":{"ref":"refs/heads/main","before":"target","head":"start"}}');`)
	if err != nil {
		t.Fatal(err)
	}
	w = request(t, p, "GET", "/api/refs?name=two&sort=oldest&from=2026-11-04T00:00:00Z", "localhost:8765", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &refs) != nil || len(refs.Items) != 2 ||
		refs.Items[0].TimeSource != "tip_commit" || refs.Items[1].TimeSource != "github_push" ||
		refs.Items[1].TimelineAt != "2026-11-09T12:00:00Z" || refs.Items[1].DetectedAt != "" {
		t.Fatalf("ref chronology from available evidence: %d %s", w.Code, w.Body.String())
	}
}

func TestArchivedFilterOptionsAndExports(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config.yaml")
	body := "token_env: UNUSED\ndata_root: '" + filepath.ToSlash(filepath.Join(root, "data")) + "'\nusers:\n  - login: watcher\n"
	if err := os.WriteFile(cfg, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	_, err = p.db.Exec(`INSERT INTO activity_events VALUES
		('user:watcher','1','PushEvent','Ada','Org','a_b','2026-10-01','{}'),
		('repo:Org/a_b','1','PushEvent','ada','ORG','A_B','2026-10-01','{}'),
		('user:watcher','2','WatchEvent','Ben','Org','acb','2026-10-01','{}'),
		('user:watcher','3','WatchEvent','Ada','Org','acb','2026-10-01','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	options := func(query string) filterOptions {
		t.Helper()
		w := request(t, p, "GET", "/api/filter-options?"+query, "localhost:8765", "", "")
		var v filterOptions
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("options: %d %s", w.Code, w.Body.String())
		}
		return v
	}
	if v := options(""); len(v.EventTypes) != 2 || len(v.Users) != 2 || len(v.Repositories) != 2 {
		t.Fatalf("expected distinct archive values, got %+v", v)
	}
	if v := options("limit=1"); !v.HasMore || !v.More["users"] || len(v.Users) != 1 {
		t.Fatalf("first options page: %+v", v)
	}
	if v := options("limit=1&offset=1"); v.HasMore || len(v.Users) != 1 || v.Users[0] != "Ben" {
		t.Fatalf("last options page: %+v", v)
	}
	if v := options("q=a_"); len(v.Repositories) != 1 || v.HasMore {
		t.Fatalf("literal prefix: %+v", v)
	}
	query := "scope=user%3Awatcher&type=PushEvent&login=ADA&repo_owner=oRg&repo_name=A_B"
	w := request(t, p, "GET", "/api/events?"+query, "localhost:8765", "", "")
	var page struct{ Items []eventRow }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].EventID != "1" {
		t.Fatal("combined event filters did not match the saved row")
	}
	w = request(t, p, "GET", "/api/export/events?format=json&"+query, "localhost:8765", "", "")
	var exported []eventRow
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &exported) != nil || len(exported) != 1 || exported[0] != page.Items[0] {
		t.Fatal("filtered export differs from activity view")
	}

	// The chosen calendar day is conveyed by two instants. Events saved with
	// different UTC offsets should be filtered and sorted by the actual time.
	_, err = p.db.Exec(`INSERT INTO activity_events VALUES
		('date-test','a','PushEvent','Ada','Org','dates','2026-11-20T23:05:00Z','{}'),
		('date-test','b','PushEvent','Ada','Org','dates','2026-11-20T22:45:30-01:00','{}'),
		('date-test','c','PushEvent','Ada','Org','dates','2026-11-21T23:00:00Z','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	bounds := "scope=date-test&from=2026-11-20T23:00:00Z&before=2026-11-21T23:00:00Z"
	for _, tc := range []struct{ order, first, second string }{
		{"oldest", "a", "b"},
		{"newest", "b", "a"},
	} {
		w := request(t, p, "GET", "/api/events?"+bounds+"&sort="+tc.order, "localhost:8765", "", "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 2 || page.Items[0].EventID != tc.first || page.Items[1].EventID != tc.second {
			t.Fatalf("event date order %s: %d %s", tc.order, w.Code, w.Body.String())
		}
		w = request(t, p, "GET", "/api/export/events?format=json&"+bounds+"&sort="+tc.order, "localhost:8765", "", "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &exported) != nil || len(exported) != 2 || exported[0] != page.Items[0] || exported[1] != page.Items[1] {
			t.Fatalf("date filtered export %s: %d %s", tc.order, w.Code, w.Body.String())
		}
	}
	w = request(t, p, "GET", "/api/events?from=2026-11-22T00:00:00Z&before=2026-11-20T00:00:00Z", "localhost:8765", "", "")
	if w.Code != 400 {
		t.Fatalf("accepted inverted date range: %d", w.Code)
	}
}
