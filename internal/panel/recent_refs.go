package panel

import "net/http"

func (p *Server) recentRefChanges(w http.ResponseWriter, r *http.Request) {
	if !onlyGet(w, r) {
		return
	}
	limit, offset := paging(r)
	filters := r.URL.Query()
	dates, err := parseActivityDates(filters)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	owner, name := filters.Get("repo_owner"), filters.Get("repo_name")
	clause, args := dates.filter(refClock, []any{owner, owner, name, name})
	args = append(args, limit+1, offset)
	rows, err := p.db.QueryContext(r.Context(), refSelect+`
		WHERE (o.detected_at IS NOT NULL OR c.kind IN ('force_push','delete'))
		  AND (?='' OR c.owner=? COLLATE NOCASE) AND (?='' OR c.name=? COLLATE NOCASE)`+
		clause+` ORDER BY julianday(`+refClock+`) `+dates.direction+` NULLS LAST,`+refTieOrder+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		http.Error(w, "could not read ref changes", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []refRow{}
	for rows.Next() {
		item, err := scanRef(rows)
		if err != nil {
			http.Error(w, "could not read ref changes", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		http.Error(w, "could not read ref changes", http.StatusInternalServerError)
		return
	}
	jsonReply(w, struct {
		Items   []refRow `json:"items"`
		HasMore bool     `json:"has_more"`
	}{items[:min(limit, len(items))], len(items) > limit})
}
