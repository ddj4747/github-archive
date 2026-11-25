package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"githubarchive/internal/model"
)

func TestArchiveTransactionsAndPersistence(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	e := model.ActivityEvent{Scope: "repo:a/b", EventID: "1", At: time.Now().UTC(), Raw: "{}"}
	failed := errors.New("abort")
	err = s.InTx(func(tx *Tx) error {
		if _, err := tx.InsertEvent(e); err != nil {
			return err
		}
		if err := tx.SetCursor("feed", "1"); err != nil {
			return err
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatalf("rollback: %v", err)
	}
	if c, err := s.GetCursor("feed"); err != nil || c != "" {
		t.Fatalf("cursor %q %v", c, err)
	}
	if rows, err := s.ListEvents(e.Scope, 10); err != nil || len(rows) != 0 {
		t.Fatalf("rows %v %v", rows, err)
	}
	err = s.InTx(func(tx *Tx) error {
		if inserted, err := tx.InsertEvent(e); err != nil || !inserted {
			return fmt.Errorf("insert: %v", err)
		}
		if inserted, err := tx.InsertEvent(e); err != nil || inserted {
			return fmt.Errorf("duplicate: %v", err)
		}
		if _, err := tx.InsertRefChange(model.RefChange{Owner: "a", Name: "b", RefName: "refs/heads/main", NewSHA: "sha", Kind: model.Create}); err != nil {
			return err
		}
		return tx.SetCursor("feed", "1")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if c, err := s.GetCursor("feed"); err != nil || c != "1" {
		t.Fatalf("persisted cursor %q %v", c, err)
	}
	if rows, err := s.ListEvents(e.Scope, 0); err != nil || len(rows) != 1 || rows[0].EventID != "1" {
		t.Fatalf("persisted rows %v %v", rows, err)
	}
	if rows, err := s.ListEvents("other", 1000); err != nil || len(rows) != 0 {
		t.Fatalf("scope rows %v %v", rows, err)
	}
	var old, next string
	if err := s.db.QueryRow(`SELECT old_sha,new_sha FROM ref_changes WHERE owner='a'`).Scan(&old, &next); err != nil || old != "" || next != "sha" {
		t.Fatalf("persisted refs %q %q: %v", old, next, err)
	}
	for i := 0; i < 105; i++ {
		_, err = s.InsertEvent(model.ActivityEvent{Scope: "limited", EventID: fmt.Sprint(i), At: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
	}
	if rows, err := s.ListEvents("limited", 101); err != nil || len(rows) != 100 {
		t.Fatalf("limit: %d %v", len(rows), err)
	}
}

func TestHeartbeatPersistsLastSeenAfterStop(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if seen, err := s.Heartbeat(); err != nil || !seen.IsZero() {
		t.Fatalf("initial heartbeat %v %v", seen, err)
	}
	want := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.MarkHeartbeat(want); err != nil {
		t.Fatal(err)
	}
	if err := s.StopHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if seen, err := s.Heartbeat(); err != nil || !seen.Equal(want) {
		t.Fatalf("heartbeat %v %v", seen, err)
	}
	if state, err := s.GetCursor(WorkerStateFeed); err != nil || state != "stopped" {
		t.Fatalf("state %q %v", state, err)
	}
}
