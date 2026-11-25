package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"githubarchive/internal/model"
	"modernc.org/sqlite"
)

type Store struct{ db *sql.DB }
type Tx struct{ tx *sql.Tx }
type queryer interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}

func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(filepath.Join(root, "archive.sqlite"))
	if err != nil {
		return nil, err
	}
	sqlite.StrictPragmas(true)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err = Apply(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func insertEvent(q queryer, e model.ActivityEvent) (bool, error) {
	r, err := q.Exec(`INSERT INTO activity_events (scope,event_id,event_type,login,repo_owner,repo_name,at,raw) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(scope,event_id) DO NOTHING`, e.Scope, e.EventID, e.Type, e.Login, e.RepoOwner, e.RepoName, e.At.Format(time.RFC3339Nano), e.Raw)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}
func insertRef(q queryer, r model.RefChange) (bool, error) {
	result, err := q.Exec(`INSERT INTO ref_changes (owner,name,ref_name,old_sha,new_sha,kind) VALUES (?,?,?,?,?,?) ON CONFLICT(owner,name,ref_name,old_sha,new_sha) DO NOTHING`, r.Owner, r.Name, r.RefName, r.OldSHA, r.NewSHA, r.Kind)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func getCursor(q queryer, feed string) (string, error) {
	var c string
	err := q.QueryRow(`SELECT cursor FROM poll_cursors WHERE feed=?`, feed).Scan(&c)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return c, err
}
func setCursor(q queryer, feed, cursor string) error {
	_, err := q.Exec(`INSERT INTO poll_cursors(feed,cursor) VALUES(?,?) ON CONFLICT(feed) DO UPDATE SET cursor=excluded.cursor`, feed, cursor)
	return err
}
func (s *Store) InsertEvent(e model.ActivityEvent) (bool, error) { return insertEvent(s.db, e) }
func (s *Store) InsertRefChange(r model.RefChange) (bool, error) { return insertRef(s.db, r) }
func (s *Store) GetCursor(feed string) (string, error)           { return getCursor(s.db, feed) }
func (s *Store) SetCursor(feed, cursor string) error             { return setCursor(s.db, feed, cursor) }

const HeartbeatFeed = "service:heartbeat"
const WorkerStateFeed = "service:worker_state"

// MarkHeartbeat records the worker's last successful heartbeat in UTC.
func (s *Store) MarkHeartbeat(at time.Time) error {
	return s.InTx(func(tx *Tx) error {
		if err := tx.SetCursor(HeartbeatFeed, at.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return tx.SetCursor(WorkerStateFeed, "running")
	})
}

// StopHeartbeat retains the last-seen time while marking a clean shutdown.
func (s *Store) StopHeartbeat() error { return s.SetCursor(WorkerStateFeed, "stopped") }

// Heartbeat returns a zero time if the worker has never been seen.
func (s *Store) Heartbeat() (time.Time, error) {
	v, err := s.GetCursor(HeartbeatFeed)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, v)
}
func (t *Tx) InsertEvent(e model.ActivityEvent) (bool, error) { return insertEvent(t.tx, e) }
func (t *Tx) InsertRefChange(r model.RefChange) (bool, error) { return insertRef(t.tx, r) }
func (t *Tx) GetCursor(feed string) (string, error)           { return getCursor(t.tx, feed) }
func (t *Tx) SetCursor(feed, cursor string) error             { return setCursor(t.tx, feed, cursor) }
func (s *Store) InTx(fn func(*Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(&Tx{tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertDiscovered preserves an existing repository's original source and
// polling time. It can be committed atomically with the discovering event.
func (t *Tx) UpsertDiscovered(owner, name, sourceLogin string) error {
	if !model.ValidSegment(owner) || !model.ValidSegment(name) || !model.ValidSegment(sourceLogin) {
		return fmt.Errorf("invalid discovered repository or login")
	}
	_, err := t.tx.Exec(`INSERT INTO discovered_repositories(owner,name,source_login,last_polled) VALUES(?,?,?,'') ON CONFLICT(owner,name) DO NOTHING`, owner, name, sourceLogin)
	return err
}

// NextDiscovered returns the least recently polled repository for a source.
func (s *Store) NextDiscovered(sourceLogin string) (model.Repository, time.Time, bool, error) {
	var repo model.Repository
	var last string
	err := s.db.QueryRow(`SELECT owner,name,last_polled FROM discovered_repositories WHERE source_login=? COLLATE NOCASE ORDER BY last_polled ASC, owner COLLATE NOCASE, name COLLATE NOCASE LIMIT 1`, sourceLogin).Scan(&repo.Owner, &repo.Name, &last)
	if err == sql.ErrNoRows {
		return model.Repository{}, time.Time{}, false, nil
	}
	if err != nil {
		return model.Repository{}, time.Time{}, false, err
	}
	if last == "" {
		return repo, time.Time{}, true, nil
	}
	at, err := time.Parse(time.RFC3339Nano, last)
	if err != nil {
		return model.Repository{}, time.Time{}, false, fmt.Errorf("invalid discovered repository poll time: %w", err)
	}
	return repo, at.UTC(), true, nil
}

// MarkDiscoveredPolled records a UTC timestamp for a case-insensitive key.
func (s *Store) MarkDiscoveredPolled(owner, name string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE discovered_repositories SET last_polled=? WHERE owner=? COLLATE NOCASE AND name=? COLLATE NOCASE`, at.UTC().Format("2006-01-02T15:04:05.000000000Z"), owner, name)
	return err
}
func (s *Store) ListEvents(scope string, limit int) ([]model.ActivityEvent, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT scope,event_id,event_type,login,repo_owner,repo_name,at,raw FROM activity_events WHERE scope=? ORDER BY at DESC,event_id DESC LIMIT ?`, scope, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []model.ActivityEvent
	for rows.Next() {
		var e model.ActivityEvent
		var at string
		if err := rows.Scan(&e.Scope, &e.EventID, &e.Type, &e.Login, &e.RepoOwner, &e.RepoName, &at, &e.Raw); err != nil {
			return nil, err
		}
		e.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, fmt.Errorf("invalid event time: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
