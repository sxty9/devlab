package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/model"
)

func newTempPRStore(t *testing.T) *PRStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_PRS", filepath.Join(t.TempDir(), "prs.json"))
	return NewPRStore(nil)
}

// TestPRStoreUpdate pins the atomic passive-pool primitive (C F4): a miss leaves the pool
// untouched and reports not-found; a hit applies the caller's mutation and persists it; a
// second Update accumulates on the persisted state (so a block counter can grow attempt by
// attempt).
func TestPRStoreUpdate(t *testing.T) {
	s := newTempPRStore(t)
	if err := s.Add(PendingPR{Repo: "o/x", Number: 7, URL: "u", DeliveryID: "dlv_1"}); err != nil {
		t.Fatal(err)
	}

	// Miss: untracked PR → found=false, mutate not called, nothing saved.
	called := false
	found, err := s.Update("o/x", 999, func(*PendingPR) { called = true })
	if err != nil || found || called {
		t.Fatalf("miss should be found=false without calling mutate; got found=%v called=%v err=%v", found, called, err)
	}

	// Hit: mutate + persist. The block fields round-trip through List().
	now := time.Now().UTC().Truncate(time.Second)
	found, err = s.Update("o/x", 7, func(p *PendingPR) {
		p.Backoff = &model.Backoff{Reason: "merge refused", Class: "transient", Attempts: 1, FirstAt: now, LastAt: now, NextAt: now.Add(time.Minute)}
		p.Blocked = true
		p.BlockedReason = "delivery is not set up"
		p.BlockedAt = now
	})
	if err != nil || !found {
		t.Fatalf("hit should be found=true; got found=%v err=%v", found, err)
	}
	prs, _ := s.List()
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	p := prs[0]
	if !p.Blocked || p.BlockedReason != "delivery is not set up" || !p.BlockedAt.Equal(now) {
		t.Errorf("block fields did not round-trip: %+v", p)
	}
	if p.Backoff == nil || p.Backoff.Attempts != 1 || p.Backoff.Reason != "merge refused" {
		t.Errorf("backoff did not round-trip: %+v", p.Backoff)
	}
	if p.DeliveryID != "dlv_1" {
		t.Errorf("DeliveryID did not round-trip: %+v", p)
	}

	// Second Update accumulates on the persisted value (indivisible read-modify-write).
	if _, err := s.Update("o/x", 7, func(p *PendingPR) { p.Backoff.Attempts++ }); err != nil {
		t.Fatal(err)
	}
	prs, _ = s.List()
	if prs[0].Backoff.Attempts != 2 {
		t.Errorf("Backoff.Attempts should accumulate to 2, got %d", prs[0].Backoff.Attempts)
	}

	// Clearing the block is the explicit-resume path: the caller's closure decides.
	if _, err := s.Update("o/x", 7, func(p *PendingPR) {
		p.Blocked, p.BlockedReason, p.BlockedAt, p.Backoff = false, "", time.Time{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	prs, _ = s.List()
	if prs[0].Blocked || prs[0].Backoff != nil {
		t.Errorf("explicit resume must clear the block state, got %+v", prs[0])
	}
}

// TestPRStoreAddRemove pins dedup (repo+number) and removal.
func TestPRStoreAddRemove(t *testing.T) {
	s := newTempPRStore(t)
	if err := s.Add(PendingPR{Repo: "o/x", Number: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(PendingPR{Repo: "o/x", Number: 7, URL: "dup"}); err != nil {
		t.Fatal(err)
	}
	prs, _ := s.List()
	if len(prs) != 1 || prs[0].URL != "" {
		t.Fatalf("Add must dedupe by repo+number (first wins), got %+v", prs)
	}
	if err := s.Remove("o/x", 7); err != nil {
		t.Fatal(err)
	}
	if prs, _ := s.List(); len(prs) != 0 {
		t.Fatalf("Remove must drop the record, got %+v", prs)
	}
}

// TestPRStoreLegacyTolerance: an old stored file (no deliveryId, no backoff/block fields)
// still loads — zero values, never an error.
func TestPRStoreLegacyTolerance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prs.json")
	t.Setenv("DEVLAB_MERCURY_RUNS_PRS", path)
	legacy := `{"prs":[{"repo":"o/x","number":3,"url":"u","runId":"run_a","createdAt":"2026-07-01T00:00:00Z","mergeBy":"2026-08-01T00:00:00Z","lastChecked":"2026-07-02T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewPRStore(nil)
	prs, err := s.List()
	if err != nil || len(prs) != 1 {
		t.Fatalf("legacy load: %v %+v", err, prs)
	}
	p := prs[0]
	if p.DeliveryID != "" || p.Blocked || p.Backoff != nil {
		t.Errorf("legacy record must carry zero values for the new fields: %+v", p)
	}
	if p.RunID != "run_a" || p.Number != 3 {
		t.Errorf("legacy fields lost: %+v", p)
	}
}
