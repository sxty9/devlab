package runs

import (
	"path/filepath"
	"testing"
	"time"
)

func newTempPRStore(t *testing.T) *PRStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_PRS", filepath.Join(t.TempDir(), "prs.json"))
	return NewPRStore()
}

// TestPRStoreUpdate pins the atomic passive-pool primitive: a miss leaves the pool untouched and reports
// not-found; a hit applies the caller's mutation and persists it; a second Update accumulates on the
// persisted state (so the block counter can grow attempt by attempt).
func TestPRStoreUpdate(t *testing.T) {
	s := newTempPRStore(t)
	if err := s.Add(PendingPR{Repo: "o/x", Number: 7, URL: "u"}); err != nil {
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
		p.DeployAttempts++
		p.Blocked = true
		p.BlockedReason = "nicht eingerichtet"
		p.BlockedAt = now
	})
	if err != nil || !found {
		t.Fatalf("hit should be found=true; got found=%v err=%v", found, err)
	}
	prs, _ := s.List()
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	if p := prs[0]; !p.Blocked || p.DeployAttempts != 1 || p.BlockedReason != "nicht eingerichtet" || !p.BlockedAt.Equal(now) {
		t.Errorf("block fields did not round-trip: %+v", p)
	}

	// Second Update accumulates on the persisted value.
	if _, err := s.Update("o/x", 7, func(p *PendingPR) { p.DeployAttempts++ }); err != nil {
		t.Fatal(err)
	}
	prs, _ = s.List()
	if prs[0].DeployAttempts != 2 {
		t.Errorf("DeployAttempts should accumulate to 2, got %d", prs[0].DeployAttempts)
	}
}
