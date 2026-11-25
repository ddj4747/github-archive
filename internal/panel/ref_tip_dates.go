package panel

import (
	"context"
	"strings"
	"time"

	"githubarchive/internal/model"
)

// indexRefTipDates indexes available commit dates for historical rows. They
// remain separate from actual ref observation times.
func (p *Server) indexRefTipDates(ctx context.Context) {
	const batchSize = 128
	type tip struct {
		rowid int64
		repo  model.Repository
		sha   string
	}
	var after int64
	for ctx.Err() == nil {
		rows, err := p.db.QueryContext(ctx, `
			SELECT c.rowid,c.owner,c.name,c.new_sha FROM ref_changes c
			LEFT JOIN ref_observations o USING(owner,name,ref_name,old_sha,new_sha)
			LEFT JOIN ref_tip_dates g ON g.owner=c.owner AND g.name=c.name AND g.sha=c.new_sha
			WHERE c.rowid>? AND c.new_sha<>'' AND o.detected_at IS NULL AND g.sha IS NULL
			ORDER BY c.rowid LIMIT ?`, after, batchSize)
		if err != nil {
			return
		}
		tips := make([]tip, 0, batchSize)
		for rows.Next() {
			var t tip
			if err := rows.Scan(&t.rowid, &t.repo.Owner, &t.repo.Name, &t.sha); err != nil {
				rows.Close()
				return
			}
			tips = append(tips, t)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || len(tips) == 0 {
			return
		}
		after = tips[len(tips)-1].rowid
		paths := make(map[model.Repository]string)
		seen := make(map[tip]bool)
		for _, t := range tips {
			t.rowid = 0
			if ctx.Err() != nil {
				return
			}
			if seen[t] || !validObjectID(t.sha) {
				continue
			}
			seen[t] = true
			path, found := paths[t.repo]
			if !found {
				path, _ = p.mirrorDirFor(t.repo)
				paths[t.repo] = path
			}
			if path == "" {
				continue
			}
			readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			raw, err := localOutput(readCtx, path, 128, "show", "-s", "--no-show-signature", "--format=%cI", t.sha+"^{commit}", "--")
			cancel()
			if err != nil {
				continue
			}
			at, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
			if err == nil {
				_, _ = p.db.ExecContext(ctx, `INSERT INTO ref_tip_dates VALUES (?,?,?,?) ON CONFLICT DO NOTHING`,
					t.repo.Owner, t.repo.Name, t.sha, at.UTC().Format(time.RFC3339))
			}
		}
	}
}
