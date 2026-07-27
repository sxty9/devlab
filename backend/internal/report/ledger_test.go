package report

import (
	"path/filepath"
	"testing"
	"time"
)

func tempLedger(t *testing.T) *Ledger {
	t.Helper()
	return NewLedgerAt(filepath.Join(t.TempDir(), "daily-reports.json"))
}

func TestLedgerMissingIsEmpty(t *testing.T) {
	l := tempLedger(t)
	recs, err := l.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("want empty, got %d", len(recs))
	}
	if _, ok, _ := l.Get("alice", "2026-07-26"); ok {
		t.Fatal("Get on empty should be !ok")
	}
}

func TestLedgerPutUpsertsByRecipientAndDay(t *testing.T) {
	l := tempLedger(t)
	now := time.Date(2026, 7, 27, 0, 5, 0, 0, time.UTC)

	if err := l.Put(Record{Recipient: "alice", Day: "2026-07-26", Status: StatusFailed, Attempts: 1, LastAttempt: &now, LastError: "boom"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Same (recipient, day) upserts in place, not appends.
	if err := l.Put(Record{Recipient: "alice", Day: "2026-07-26", Status: StatusSent, Attempts: 2, SentAt: &now}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Different day is a distinct record.
	if err := l.Put(Record{Recipient: "alice", Day: "2026-07-25", Status: StatusSent, Attempts: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Different recipient, same day is a distinct record.
	if err := l.Put(Record{Recipient: "bob", Day: "2026-07-26", Status: StatusSent, Attempts: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	recs, _ := l.List()
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d: %+v", len(recs), recs)
	}
	got, ok, _ := l.Get("alice", "2026-07-26")
	if !ok {
		t.Fatal("alice/2026-07-26 missing")
	}
	// The second Put replaced the first entirely (not merged): sent, 2 attempts, error cleared.
	if got.Status != StatusSent || got.Attempts != 2 || got.LastError != "" {
		t.Errorf("upsert did not replace: %+v", got)
	}
}

func TestLedgerPersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daily-reports.json")
	l1 := NewLedgerAt(path)
	sent := time.Date(2026, 7, 27, 0, 5, 0, 0, time.UTC)
	if err := l1.Put(Record{Recipient: "alice", Day: "2026-07-26", Status: StatusSent, SentAt: &sent}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// A fresh Ledger over the same file (a process restart) sees the sealed day.
	l2 := NewLedgerAt(path)
	got, ok, _ := l2.Get("alice", "2026-07-26")
	if !ok || got.Status != StatusSent {
		t.Fatalf("restart did not see sealed day: ok=%v rec=%+v", ok, got)
	}
}
