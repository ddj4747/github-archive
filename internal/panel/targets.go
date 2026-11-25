package panel

import (
	"encoding/json"
	"net/http"
	"strings"

	"githubarchive/internal/config"
)

const targetRepositories = `
WITH explicit AS (
	SELECT json_extract(value,'$.owner') AS owner,json_extract(value,'$.name') AS name,CAST(key AS INTEGER) AS position
	FROM json_each(?1)
), people AS (
	SELECT json_extract(value,'$.login') AS login,json_extract(value,'$.discover_repositories') AS discover,
	       json_extract(value,'$.exclude_repositories') AS exclusions
	FROM json_each(?2)
), targets AS (
	SELECT owner,name,'configured' AS source,'' AS last_polled,'configured' AS status,0 AS group_id,position
	FROM explicit
	UNION ALL
	SELECT d.owner,d.name,d.source_login,d.last_polled,
	       CASE WHEN p.login IS NULL THEN 'archived'
	            WHEN NOT p.discover OR EXISTS (SELECT 1 FROM json_each(p.exclusions) WHERE value=d.name COLLATE NOCASE) THEN 'paused'
	            ELSE 'discovered' END,1,0
	FROM discovered_repositories d LEFT JOIN people p ON p.login=d.source_login COLLATE NOCASE
	WHERE NOT EXISTS (SELECT 1 FROM explicit e WHERE e.owner=d.owner COLLATE NOCASE AND e.name=d.name COLLATE NOCASE)
	UNION ALL
	SELECT min(c.owner),min(c.name),'','', 'archived',2,0
	FROM ref_changes c
	WHERE NOT EXISTS (SELECT 1 FROM explicit e WHERE e.owner=c.owner COLLATE NOCASE AND e.name=c.name COLLATE NOCASE)
	  AND NOT EXISTS (SELECT 1 FROM discovered_repositories d WHERE d.owner=c.owner COLLATE NOCASE AND d.name=c.name COLLATE NOCASE)
	GROUP BY lower(c.owner),lower(c.name)
)
SELECT owner,name,source,last_polled,status,
       COALESCE((SELECT cursor FROM poll_cursors WHERE feed='refs:'||owner||'/'||name),'')
FROM targets
WHERE instr(lower(owner||'/'||name),?3)>0 AND (?4='' OR status=?4)
ORDER BY group_id,position,owner COLLATE NOCASE,name COLLATE NOCASE LIMIT ?5 OFFSET ?6`

func (p *Server) targets(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	q := r.URL.Query()
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	userSearch := strings.ToLower(strings.TrimSpace(q.Get("user_q")))
	status := q.Get("status")
	switch status {
	case "", "configured", "discovered", "paused", "archived":
	default:
		http.Error(w, "unknown target status", http.StatusBadRequest)
		return
	}
	if len(search) > 256 || len(userSearch) > 256 {
		http.Error(w, "target search is too long", http.StatusBadRequest)
		return
	}
	c, err := config.LoadForEditing(p.configPath)
	if err != nil {
		http.Error(w, "cannot read configuration", http.StatusInternalServerError)
		return
	}
	targets := view(c)
	repoJSON, _ := json.Marshal(targets.Repositories)
	userJSON, _ := json.Marshal(targets.Users)
	limit, offset := paging(r)
	rows, err := p.db.QueryContext(r.Context(), targetRepositories, string(repoJSON), string(userJSON), search, status, limit+1, offset)
	if err != nil {
		http.Error(w, "could not read targets", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type repoTarget struct {
		Owner      string `json:"owner"`
		Name       string `json:"name"`
		Source     string `json:"source"`
		LastPolled string `json:"last_polled"`
		LastSynced string `json:"last_synced"`
		Status     string `json:"status"`
	}
	repos := []repoTarget{}
	for rows.Next() {
		var target repoTarget
		if err := rows.Scan(&target.Owner, &target.Name, &target.Source, &target.LastPolled, &target.Status, &target.LastSynced); err != nil {
			http.Error(w, "could not read targets", http.StatusInternalServerError)
			return
		}
		repos = append(repos, target)
	}
	if rows.Err() != nil {
		http.Error(w, "could not read targets", http.StatusInternalServerError)
		return
	}
	type userTarget struct {
		Login         string `json:"login"`
		Discover      bool   `json:"discover_repositories"`
		Authenticated bool   `json:"authenticated_events"`
	}
	users := []userTarget{}
	userLimit, userOffset := pageBounds(q.Get("user_limit"), q.Get("user_offset"))
	for _, u := range c.Users {
		if strings.Contains(strings.ToLower(u.Login), userSearch) {
			if userOffset > 0 {
				userOffset--
				continue
			}
			users = append(users, userTarget{u.Login, u.DiscoverRepositories, u.AuthenticatedEvents})
			if len(users) > userLimit {
				break
			}
		}
	}
	jsonReply(w, struct {
		Repositories  []repoTarget `json:"repositories"`
		Users         []userTarget `json:"users"`
		HasMore       bool         `json:"has_more"`
		UsersHaveMore bool         `json:"users_have_more"`
	}{repos[:min(limit, len(repos))], users[:min(userLimit, len(users))], len(repos) > limit, len(users) > userLimit})
}
