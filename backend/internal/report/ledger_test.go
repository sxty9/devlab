package report

import (
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/model"
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

// Update is the atomic read-modify-write the explicit resumption rides on: it changes ONE record in
// place under the store's own lock, leaves every other record untouched, and reports honestly when the
// record it was asked for does not exist.
func TestLedgerUpdateChangesOneRecordInPlace(t *testing.T) {
	l := tempLedger(t)
	failed := time.Date(2026, 7, 27, 0, 5, 0, 0, time.UTC)
	blocked := Record{
		Recipient: "alice", Day: "2026-07-26", Status: StatusBlocked, Attempts: 5,
		LastAttempt: &failed, LastError: "mail service down",
		Backoff: &model.Backoff{Class: "transient", Attempts: 5, Reason: "mail service down", LastAt: failed},
	}
	if err := l.Put(blocked); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := l.Put(Record{Recipient: "bob", Day: "2026-07-26", Status: StatusBlocked, Attempts: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok, err := l.Update("alice", "2026-07-26", func(rec *Record) {
		rec.Status, rec.Backoff = StatusFailed, nil
	})
	if err != nil || !ok {
		t.Fatalf("Update: ok=%v err=%v", ok, err)
	}
	if got.Status != StatusFailed || got.Backoff != nil {
		t.Fatalf("the change is not in the returned record: %+v", got)
	}
	// Persisted — and the history the record carried survives the change.
	stored, _, _ := l.Get("alice", "2026-07-26")
	if stored.Status != StatusFailed || stored.Backoff != nil || stored.Attempts != 5 || stored.LastError == "" {
		t.Fatalf("stored record = %+v", stored)
	}
	// The neighbour is untouched.
	if other, _, _ := l.Get("bob", "2026-07-26"); other.Status != StatusBlocked {
		t.Errorf("another recipient's record changed: %+v", other)
	}
	// An unknown key changes nothing and says so.
	if _, ok, err := l.Update("alice", "2026-07-01", func(*Record) { t.Error("mutate must not run for a missing record") }); ok || err != nil {
		t.Errorf("Update on a missing record: ok=%v err=%v", ok, err)
	}
}

// A blocked record survives a restart with its whole backoff episode: class, attempts and times. Without
// that, a restart would silently hand a permanently broken delivery a fresh set of attempts.
func TestLedgerRoundTripsTheBackoffEpisode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daily-reports.json")
	last := time.Date(2026, 7, 27, 0, 5, 0, 0, time.UTC)
	b := model.Backoff{Reason: "mailer: no internal secret configured", Class: "permanent", Attempts: 1, FirstAt: last, LastAt: last}
	if err := NewLedgerAt(path).Put(Record{
		Recipient: "alice", Day: "2026-07-26", Status: StatusBlocked, Attempts: 1, LastAttempt: &last,
		LastError: b.Reason, Backoff: &b,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := NewLedgerAt(path).Get("alice", "2026-07-26")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Status != StatusBlocked || got.Backoff == nil {
		t.Fatalf("record = %+v", got)
	}
	if got.Backoff.Class != "permanent" || got.Backoff.Attempts != 1 || !got.Backoff.LastAt.Equal(last) ||
		got.Backoff.Reason != b.Reason || !got.Backoff.NextAt.IsZero() {
		t.Errorf("the episode did not survive the roundtrip: %+v", got.Backoff)
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
