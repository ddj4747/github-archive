package store

import (
	"errors"
	"testing"
	"time"
)

func TestDiscoveredQueue(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	if err := s.InTx(func(tx *Tx) error {
		if err := tx.UpsertDiscovered("Owner", "Discarded", "Alice"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if _, _, ok, err := s.NextDiscovered("alice"); err != nil || ok {
		t.Fatalf("rolled back row: %v %v", ok, err)
	}
	if err := s.InTx(func(tx *Tx) error {
		if err := tx.UpsertDiscovered("Owner", "One", "Alice"); err != nil {
			return err
		}
		return tx.UpsertDiscovered("Owner", "Two", "Alice")
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDiscoveredPolled("OWNER", "ONE", time.Date(2024, 1, 1, 1, 0, 0, 500, time.FixedZone("offset", 3600))); err != nil {
		t.Fatal(err)
	}
	if err := s.InTx(func(tx *Tx) error { return tx.UpsertDiscovered("owner", "one", "Bob") }); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.NextDiscovered("Bob"); err != nil || ok {
		t.Fatalf("source overwritten: %v %v", ok, err)
	}
	r, at, ok, err := s.NextDiscovered("aLiCe")
	if err != nil || !ok || r.Name != "Two" || !at.IsZero() {
		t.Fatalf("never polled: %+v %v %v %v", r, at, ok, err)
	}
	if err := s.MarkDiscoveredPolled("owner", "two", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	r, at, ok, err = s.NextDiscovered("ALICE")
	if err != nil || !ok || r.Name != "Two" || at.Nanosecond() != 0 {
		t.Fatalf("oldest: %+v %v %v %v", r, at, ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, at, ok, err = s.NextDiscovered("Alice")
	if err != nil || !ok || r.Name != "Two" || !at.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("reopen: %+v %v %v %v", r, at, ok, err)
	}
}
