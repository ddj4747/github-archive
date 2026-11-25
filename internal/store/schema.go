package store

import "database/sql"

// Apply creates the append-only archive schema, if needed.
func Apply(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS activity_events (scope TEXT NOT NULL, event_id TEXT NOT NULL, event_type TEXT NOT NULL, login TEXT NOT NULL, repo_owner TEXT NOT NULL, repo_name TEXT NOT NULL, at TEXT NOT NULL, raw TEXT NOT NULL, PRIMARY KEY(scope, event_id))`,
		`CREATE INDEX IF NOT EXISTS activity_events_by_type ON activity_events(event_type)`,
		`CREATE INDEX IF NOT EXISTS activity_events_by_time ON activity_events(julianday(at),event_id,scope)`,
		`CREATE INDEX IF NOT EXISTS activity_events_by_actor ON activity_events(login COLLATE NOCASE)`,
		`CREATE INDEX IF NOT EXISTS activity_events_by_repo ON activity_events(repo_owner COLLATE NOCASE,repo_name COLLATE NOCASE)`,
		`CREATE TABLE IF NOT EXISTS ref_changes (owner TEXT NOT NULL, name TEXT NOT NULL, ref_name TEXT NOT NULL, old_sha TEXT NOT NULL, new_sha TEXT NOT NULL, kind TEXT NOT NULL, PRIMARY KEY(owner, name, ref_name, old_sha, new_sha))`,
		`CREATE INDEX IF NOT EXISTS ref_changes_rewrites_before ON ref_changes(owner COLLATE NOCASE,name COLLATE NOCASE,old_sha) WHERE kind IN ('force_push','delete')`,
		`CREATE INDEX IF NOT EXISTS ref_changes_rewrites_after ON ref_changes(owner COLLATE NOCASE,name COLLATE NOCASE,new_sha) WHERE kind IN ('force_push','delete')`,
		`CREATE TABLE IF NOT EXISTS ref_observations (
			owner TEXT NOT NULL, name TEXT NOT NULL, ref_name TEXT NOT NULL, old_sha TEXT NOT NULL, new_sha TEXT NOT NULL,
			detected_at TEXT NOT NULL,
			PRIMARY KEY(owner,name,ref_name,old_sha,new_sha)
		)`,
		`CREATE INDEX IF NOT EXISTS ref_observations_by_time ON ref_observations(detected_at DESC)`,
		`CREATE TABLE IF NOT EXISTS ref_tip_dates (
			owner TEXT NOT NULL, name TEXT NOT NULL, sha TEXT NOT NULL, committed_at TEXT NOT NULL,
			PRIMARY KEY(owner,name,sha)
		)`,
		`CREATE TRIGGER IF NOT EXISTS record_ref_observation AFTER INSERT ON ref_changes
			BEGIN INSERT INTO ref_observations VALUES (NEW.owner,NEW.name,NEW.ref_name,NEW.old_sha,NEW.new_sha,strftime('%Y-%m-%dT%H:%M:%fZ','now')); END`,
		`CREATE TABLE IF NOT EXISTS poll_cursors (feed TEXT NOT NULL PRIMARY KEY, cursor TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS discovered_repositories (owner TEXT NOT NULL COLLATE NOCASE, name TEXT NOT NULL COLLATE NOCASE, source_login TEXT NOT NULL COLLATE NOCASE, last_polled TEXT NOT NULL, PRIMARY KEY(owner, name))`,
		`CREATE INDEX IF NOT EXISTS discovered_repositories_schedule ON discovered_repositories(source_login, last_polled, owner, name)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
