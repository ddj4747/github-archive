package panel

import (
	"net/http"
	"strings"
)

type filterOptions struct {
	EventTypes   []string           `json:"event_types"`
	Users        []string           `json:"users"`
	Repositories []repositoryOption `json:"repositories"`
	HasMore      bool               `json:"has_more"`
	More         map[string]bool    `json:"more"`
}

type repositoryOption struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// Each list is drawn from the archive, including actors and repositories that
// are no longer monitored. Each subquery uses its own page limit.
const optionQuery = `
SELECT 'type', event_type, '' FROM (
  SELECT DISTINCT event_type FROM activity_events WHERE event_type <> '' AND event_type LIKE ?1 ESCAPE '\'
  ORDER BY event_type LIMIT ?2 OFFSET ?3
)
UNION ALL SELECT 'user', actor, '' FROM (
  SELECT DISTINCT login COLLATE NOCASE AS actor FROM activity_events WHERE login <> '' AND login LIKE ?1 ESCAPE '\'
  ORDER BY actor LIMIT ?2 OFFSET ?3
)
UNION ALL SELECT 'repo', owner, name FROM (
  SELECT DISTINCT repo_owner COLLATE NOCASE AS owner, repo_name COLLATE NOCASE AS name
  FROM activity_events WHERE repo_owner <> '' AND repo_name <> ''
    AND (repo_owner || '/' || repo_name LIKE ?1 ESCAPE '\' OR repo_name LIKE ?1 ESCAPE '\')
  ORDER BY owner, name LIMIT ?2 OFFSET ?3
)`

func (p *Server) filterOptions(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	limit, offset := paging(r)
	prefix := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(prefix) > 256 {
		http.Error(w, "filter prefix too long", http.StatusBadRequest)
		return
	}
	pattern := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(prefix) + "%"
	rows, err := p.db.QueryContext(r.Context(), optionQuery, pattern, limit+1, offset)
	if err != nil {
		http.Error(w, "cannot load archive filters", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	result := filterOptions{EventTypes: []string{}, Users: []string{}, Repositories: []repositoryOption{}}
	for rows.Next() {
		var kind, first, second string
		if err := rows.Scan(&kind, &first, &second); err != nil {
			http.Error(w, "cannot load archive filters", http.StatusInternalServerError)
			return
		}
		switch kind {
		case "type":
			result.EventTypes = append(result.EventTypes, first)
		case "user":
			result.Users = append(result.Users, first)
		case "repo":
			result.Repositories = append(result.Repositories, repositoryOption{Owner: first, Name: second})
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "cannot load archive filters", http.StatusInternalServerError)
		return
	}
	result.More = map[string]bool{"event_types": len(result.EventTypes) > limit, "users": len(result.Users) > limit, "repositories": len(result.Repositories) > limit}
	result.HasMore = result.More["event_types"] || result.More["users"] || result.More["repositories"]
	result.EventTypes = result.EventTypes[:min(limit, len(result.EventTypes))]
	result.Users = result.Users[:min(limit, len(result.Users))]
	result.Repositories = result.Repositories[:min(limit, len(result.Repositories))]
	jsonReply(w, result)
}
