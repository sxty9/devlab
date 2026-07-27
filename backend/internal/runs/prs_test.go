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

// TestPRStoreUpdate pins the atomic read-modify-write primitive: it locates the tracked PR, applies the
// caller's mutation, persists it (round-tripping the new block fields), and reports found=false without
// saving for an untracked PR.
func TestPRStoreUpdate(t *testing.T) {
	s := newTempPRStore(t)
	if err := s.Add(PendingPR{Repo: "o/svc", Number: 5}); err != nil {
		t.Fatal(err)
	}

	// A miss neither mutates nor saves.
	called := false
	found, err := s.Update("o/svc", 999, func(*PendingPR) { called = true })
	if err != nil {
		t.Fatal(err)
	}
	if found || called {
		t.Fatalf("Update on an untracked PR must be a no-op (found=%v, called=%v)", found, called)
	}

	// A hit mutates and persists — the block fields survive the round-trip.
	at := time.Now().UTC().Truncate(time.Second)
	found, err = s.Update("o/svc", 5, func(p *PendingPR) {
		p.DeployAttempts++
		p.Blocked = true
		p.BlockedReason = "Dienst »svc« ist im Ziel »prod« nicht eingerichtet"
		p.BlockedAt = at
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Update on a tracked PR must report found=true")
	}

	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 tracked PR, got %d", len(got))
	}
	p := got[0]
	if p.DeployAttempts != 1 || !p.Blocked || p.BlockedReason == "" || !p.BlockedAt.Equal(at) {
		t.Fatalf("block state not persisted: %+v", p)
	}

	// Update accumulates across calls (a later attempt increments again).
	if _, err := s.Update("o/svc", 5, func(p *PendingPR) { p.DeployAttempts++ }); err != nil {
		t.Fatal(err)
	}
	got, _ = s.List()
	if got[0].DeployAttempts != 2 {
		t.Fatalf("DeployAttempts should accumulate to 2, got %d", got[0].DeployAttempts)
	}
}
