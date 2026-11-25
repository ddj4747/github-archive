package panel

import (
	"encoding/json"
	"net/http"
)

type historyChange struct {
	RefName string `json:"ref_name"`
	Kind    string `json:"kind"`
	Side    string `json:"side"`
}

func (p *Server) annotateHistory(r *http.Request, commits []historyCommit, indices map[string]int) error {
	if len(commits) == 0 {
		return nil
	}
	ids := make([]string, 0, len(commits))
	for _, commit := range commits {
		ids = append(ids, commit.SHA)
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	rows, err := p.db.QueryContext(r.Context(), `
WITH matches(sha, ref_name, kind, side) AS (
  SELECT old_sha, ref_name, kind, 'before' FROM json_each(?1)
  CROSS JOIN ref_changes WHERE owner=?2 COLLATE NOCASE AND name=?3 COLLATE NOCASE
    AND old_sha=value AND kind IN ('force_push','delete')
  UNION ALL
  SELECT new_sha, ref_name, kind, 'after' FROM json_each(?1)
  CROSS JOIN ref_changes WHERE owner=?2 COLLATE NOCASE AND name=?3 COLLATE NOCASE
    AND new_sha=value AND kind IN ('force_push','delete')
), ranked AS (
  SELECT *, ROW_NUMBER() OVER (PARTITION BY sha ORDER BY kind,ref_name,side) AS rank
  FROM (SELECT DISTINCT * FROM matches)
)
SELECT sha,ref_name,kind,side FROM ranked WHERE rank <= 9 ORDER BY sha,rank
`, string(encoded), q.Get("owner"), q.Get("name"))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sha string
		var change historyChange
		if err := rows.Scan(&sha, &change.RefName, &change.Kind, &change.Side); err != nil {
			return err
		}
		commit := &commits[indices[sha]]
		if len(commit.Changes) == 8 {
			commit.ChangesTruncated = true
		} else {
			commit.Changes = append(commit.Changes, change)
		}
	}
	return rows.Err()
}
