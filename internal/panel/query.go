package panel

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"githubarchive/internal/config"
	"githubarchive/internal/model"
	"githubarchive/internal/store"
)

type eventRow struct {
	Scope     string `json:"scope"`
	EventID   string `json:"event_id"`
	Type      string `json:"event_type"`
	Login     string `json:"login"`
	RepoOwner string `json:"repo_owner"`
	RepoName  string `json:"repo_name"`
	At        string `json:"at"`
	Raw       string `json:"raw"`
}
type refRow struct {
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	RefName    string `json:"ref_name"`
	OldSHA     string `json:"old_sha"`
	NewSHA     string `json:"new_sha"`
	Kind       string `json:"kind"`
	DetectedAt string `json:"detected_at"`
	TimelineAt string `json:"timeline_at,omitempty"`
	TimeSource string `json:"time_source,omitempty"`
}

func paging(r *http.Request) (int, int) {
	return pageBounds(r.URL.Query().Get("limit"), r.URL.Query().Get("offset"))
}

func pageBounds(limit, offset string) (int, int) {
	l, _ := strconv.Atoi(limit)
	if l < 1 || l > 100 {
		l = 100
	}
	o, _ := strconv.Atoi(offset)
	if o < 0 || o > 1000000000 {
		o = 0
	}
	return l, o
}
func eventQuery(r *http.Request) (string, []any, error) {
	dates, err := parseActivityDates(r.URL.Query())
	if err != nil {
		return "", nil, err
	}
	q := `SELECT scope,event_id,event_type,login,repo_owner,repo_name,at,raw FROM activity_events WHERE 1=1`
	args := []any{}
	if v := r.URL.Query().Get("scope"); v != "" {
		q += ` AND scope=?`
		args = append(args, v)
	}
	if v := r.URL.Query().Get("type"); v != "" {
		q += ` AND event_type=?`
		args = append(args, v)
	}
	for _, column := range []string{"login", "repo_owner", "repo_name"} {
		if v := r.URL.Query().Get(column); v != "" {
			q += " AND " + column + " = ? COLLATE NOCASE"
			args = append(args, v)
		}
	}
	if v := r.URL.Query().Get("q"); v != "" {
		q += ` AND (event_id LIKE ? OR login LIKE ? OR repo_owner LIKE ? OR repo_name LIKE ? OR raw LIKE ?)`
		for i := 0; i < 5; i++ {
			args = append(args, "%"+v+"%")
		}
	}
	clause, args := dates.filter("at", args)
	order := " ORDER BY julianday(at) " + dates.direction + ", event_id " + dates.direction + ", scope"
	return q + clause + order, args, nil
}

const refClock = "COALESCE(o.detected_at,e.at,g.committed_at)"
const refSelect = `SELECT c.owner,c.name,c.ref_name,c.old_sha,c.new_sha,c.kind,COALESCE(o.detected_at,''),
	COALESCE(` + refClock + `,''),
	CASE WHEN o.detected_at IS NOT NULL THEN 'observation'
	     WHEN e.at IS NOT NULL THEN 'github_push'
	     WHEN g.committed_at IS NOT NULL THEN 'tip_commit' ELSE '' END
	FROM ref_changes c LEFT JOIN ref_observations o USING(owner,name,ref_name,old_sha,new_sha)
	LEFT JOIN ref_tip_dates g ON g.owner=c.owner AND g.name=c.name AND g.sha=c.new_sha
	LEFT JOIN activity_events e ON e.rowid=(
		SELECT a.rowid FROM activity_events a
		WHERE o.detected_at IS NULL AND c.new_sha<>'' AND a.event_type='PushEvent'
		  AND a.repo_owner=c.owner COLLATE NOCASE AND a.repo_name=c.name COLLATE NOCASE
		  AND julianday(a.at) IS NOT NULL
		  AND json_extract(CASE WHEN json_valid(a.raw) THEN a.raw END,'$.payload.ref')=c.ref_name
		  AND COALESCE(json_extract(CASE WHEN json_valid(a.raw) THEN a.raw END,'$.payload.head'),
		               json_extract(CASE WHEN json_valid(a.raw) THEN a.raw END,'$.payload.after'))=c.new_sha
		  AND json_extract(CASE WHEN json_valid(a.raw) THEN a.raw END,'$.payload.before')=
		      CASE WHEN c.old_sha='' THEN substr('0000000000000000000000000000000000000000000000000000000000000000',1,length(c.new_sha)) ELSE c.old_sha END
		ORDER BY julianday(a.at),a.event_id,a.scope LIMIT 1
	)`

const refTieOrder = "c.owner,c.name,c.ref_name,c.old_sha,c.new_sha"

func refQuery(r *http.Request) (string, []any, error) {
	values := r.URL.Query()
	dates, err := parseActivityDates(values)
	if err != nil {
		return "", nil, err
	}
	q := refSelect + " WHERE 1=1"
	args := []any{}
	repo := strings.TrimSpace(values.Get("repository"))
	for _, key := range []string{"owner", "name", "kind"} {
		if v := values.Get(key); v != "" {
			if key == "name" && strings.Contains(v, "/") && repo == "" {
				repo = v
				continue
			}
			if key == "kind" && v == "rewrites" {
				q += ` AND c.kind IN ('force_push','delete')`
				continue
			}
			q += " AND c." + key + `=? COLLATE NOCASE`
			args = append(args, v)
		}
	}
	if ref := values.Get("ref"); ref != "" {
		q += " AND instr(lower(c.ref_name), lower(?)) > 0"
		args = append(args, ref)
	}
	if repo != "" {
		if owner, name, qualified := strings.Cut(repo, "/"); qualified {
			q += " AND c.owner = ? COLLATE NOCASE AND c.name = ? COLLATE NOCASE"
			args = append(args, owner, name)
		} else {
			q += " AND instr(lower(c.name), lower(?)) > 0"
			args = append(args, repo)
		}
	}
	clause, args := dates.filter(refClock, args)
	if clause != "" && values.Get("include_undated") == "1" {
		// The generated conditions share a leading AND. Keep them grouped so
		// undated rows still have to match the repository, ref and kind.
		clause = " AND (" + refClock + " IS NULL OR (" + strings.TrimPrefix(clause, " AND ") + "))"
	}
	order := refTieOrder
	if values.Get("sort") != "" || dates.from != "" || dates.before != "" {
		order = "julianday(" + refClock + ") " + dates.direction + " NULLS LAST, " +
			"CASE WHEN " + refClock + " IS NULL THEN c.rowid END " + dates.direction + ", " + order
	}
	return q + clause + " ORDER BY " + order, args, nil
}
func scanEvent(rows *sql.Rows) (eventRow, error) {
	var e eventRow
	err := rows.Scan(&e.Scope, &e.EventID, &e.Type, &e.Login, &e.RepoOwner, &e.RepoName, &e.At, &e.Raw)
	return e, err
}
func scanRef(rows *sql.Rows) (refRow, error) {
	var e refRow
	err := rows.Scan(&e.Owner, &e.Name, &e.RefName, &e.OldSHA, &e.NewSHA, &e.Kind, &e.DetectedAt, &e.TimelineAt, &e.TimeSource)
	return e, err
}
func (p *Server) events(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	q, a, err := eventQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	l, o := paging(r)
	a = append(a, l+1, o)
	rows, err := p.db.QueryContext(r.Context(), q+` LIMIT ? OFFSET ?`, a...)
	if err != nil {
		http.Error(w, "archive query failed", 500)
		return
	}
	defer rows.Close()
	items := []eventRow{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			http.Error(w, "archive query failed", 500)
			return
		}
		items = append(items, e)
	}
	if rows.Err() != nil {
		http.Error(w, "archive query failed", 500)
		return
	}
	more := len(items) > l
	if more {
		items = items[:l]
	}
	jsonReply(w, struct {
		Items   []eventRow `json:"items"`
		HasMore bool       `json:"has_more"`
	}{items, more})
}
func (p *Server) refs(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	q, a, err := refQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	l, o := paging(r)
	a = append(a, l+1, o)
	rows, err := p.db.QueryContext(r.Context(), q+` LIMIT ? OFFSET ?`, a...)
	if err != nil {
		http.Error(w, "archive query failed", 500)
		return
	}
	defer rows.Close()
	items := []refRow{}
	for rows.Next() {
		e, err := scanRef(rows)
		if err != nil {
			http.Error(w, "archive query failed", 500)
			return
		}
		items = append(items, e)
	}
	if rows.Err() != nil {
		http.Error(w, "archive query failed", 500)
		return
	}
	more := len(items) > l
	if more {
		items = items[:l]
	}
	jsonReply(w, struct {
		Items   []refRow `json:"items"`
		HasMore bool     `json:"has_more"`
	}{items, more})
}

func csvSafe(s string) string {
	trimmed := strings.TrimLeft(s, " ")
	if trimmed != "" && strings.ContainsRune("=+-@\t\r\n", rune(trimmed[0])) {
		return "'" + s
	}
	return s
}
func (p *Server) exportEvents(w http.ResponseWriter, r *http.Request) { p.export(w, r, true) }
func (p *Server) exportRefs(w http.ResponseWriter, r *http.Request)   { p.export(w, r, false) }
func (p *Server) export(w http.ResponseWriter, r *http.Request, events bool) {
	if !onlyGet(w, r) {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "json" {
		http.Error(w, "format must be csv or json", 400)
		return
	}
	var q string
	var args []any
	var err error
	label := "refs"
	if events {
		q, args, err = eventQuery(r)
		label = "events"
	} else {
		q, args, err = refQuery(r)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := p.db.QueryContext(r.Context(), q, args...)
	if err != nil {
		http.Error(w, "archive query failed", 500)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Disposition", `attachment; filename="`+label+`.`+format+`"`)
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		c := csv.NewWriter(w)
		if events {
			_ = c.Write([]string{"scope", "event_id", "event_type", "login", "repo_owner", "repo_name", "at", "raw"})
		} else {
			_ = c.Write([]string{"owner", "name", "ref_name", "old_sha", "new_sha", "kind", "detected_at", "timeline_at", "time_source"})
		}
		for rows.Next() {
			var values []string
			if events {
				e, err := scanEvent(rows)
				if err != nil {
					return
				}
				values = []string{e.Scope, e.EventID, e.Type, e.Login, e.RepoOwner, e.RepoName, e.At, e.Raw}
			} else {
				e, err := scanRef(rows)
				if err != nil {
					return
				}
				values = []string{e.Owner, e.Name, e.RefName, e.OldSHA, e.NewSHA, e.Kind, e.DetectedAt, e.TimelineAt, e.TimeSource}
			}
			for i := range values {
				values[i] = csvSafe(values[i])
			}
			if c.Write(values) != nil {
				return
			}
		}
		c.Flush()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("["))
	first := true
	enc := json.NewEncoder(w)
	for rows.Next() {
		if !first {
			_, _ = w.Write([]byte(","))
		}
		first = false
		if events {
			e, err := scanEvent(rows)
			if err != nil {
				return
			}
			if enc.Encode(e) != nil {
				return
			}
		} else {
			e, err := scanRef(rows)
			if err != nil {
				return
			}
			if enc.Encode(e) != nil {
				return
			}
		}
	}
	_, _ = w.Write([]byte("]"))
}
func (p *Server) stats(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	c, err := config.LoadForEditing(p.configPath)
	if err != nil {
		http.Error(w, "cannot read configuration", 500)
		return
	}
	var events, refs, discovered int64
	keys := make([]string, 0, len(c.Repositories))
	for _, x := range c.Repositories {
		keys = append(keys, strings.ToLower(x.Owner+"/"+x.Name))
	}
	encoded, _ := json.Marshal(keys)
	if p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM activity_events`).Scan(&events) != nil || p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM ref_changes`).Scan(&refs) != nil || p.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM discovered_repositories WHERE NOT EXISTS (SELECT 1 FROM json_each(?) WHERE value=lower(owner||'/'||name))`, string(encoded)).Scan(&discovered) != nil {
		http.Error(w, "archive query failed", 500)
		return
	}
	var lastSeen, workerState string
	err = p.db.QueryRowContext(r.Context(), `SELECT cursor FROM poll_cursors WHERE feed=?`, store.HeartbeatFeed).Scan(&lastSeen)
	if err != nil && err != sql.ErrNoRows {
		http.Error(w, "archive query failed", 500)
		return
	}
	err = p.db.QueryRowContext(r.Context(), `SELECT cursor FROM poll_cursors WHERE feed=?`, store.WorkerStateFeed).Scan(&workerState)
	if err != nil && err != sql.ErrNoRows {
		http.Error(w, "archive query failed", 500)
		return
	}
	last, parseErr := time.Parse(time.RFC3339Nano, lastSeen)
	age := time.Since(last)
	running := parseErr == nil && workerState == "running" && age >= 0 && age <= 30*time.Second
	jsonReply(w, map[string]any{"events": events, "refs": refs, "repositories": int64(len(c.Repositories)) + discovered, "users": len(c.Users), "worker_running": running, "worker_last_seen": lastSeen})
}
func (p *Server) bundle(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	repo := model.Repository{Owner: r.URL.Query().Get("owner"), Name: r.URL.Query().Get("name")}
	if _, err := repo.MirrorPath(p.root); err != nil {
		http.Error(w, "invalid repository", 400)
		return
	}
	path, err := p.mirrorDir(r)
	if err != nil {
		http.Error(w, "mirror not found", 404)
		return
	}
	cmd := gitReader(r.Context(), path, "bundle", "create", "-", "--all")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "bundle failed", 500)
		return
	}
	if err = cmd.Start(); err != nil {
		http.Error(w, "bundle failed", 500)
		return
	}
	first := make([]byte, 512)
	n, readErr := pipe.Read(first)
	if n == 0 {
		_ = cmd.Wait()
		http.Error(w, "bundle failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.bundle"`, repo.Owner, repo.Name))
	_, writeErr := w.Write(first[:n])
	if readErr == nil && writeErr == nil {
		_, writeErr = io.Copy(w, pipe)
	}
	if writeErr != nil {
		_ = pipe.Close()
	}
	_ = cmd.Wait()
}
