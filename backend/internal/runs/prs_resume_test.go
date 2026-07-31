package runs

import (
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/model"
)

func blockedPR(repo string, n int, reason string) PendingPR {
	return PendingPR{
		Repo: repo, Number: n, URL: "u", CreatedAt: time.Now(), MergeBy: time.Now(),
		Blocked: true, BlockedReason: reason, BlockedAt: time.Now(),
		Backoff: &model.Backoff{Attempts: 4, Reason: reason},
	}
}

// A blocked pull request waits for a person to say "try again" — that is what replaces endless
// retrying (K-5). Until this operation existed the state had no way out at all: 63 of 64 tracked
// entries stood blocked on 2026-07-31 by reads that never reached GitHub, and nothing in the system
// could release them.
func TestTheExplicitResumeIsTheWayOutOfTheBlockedState(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS_PRS", filepath.Join(t.TempDir(), "prs.json"))
	s := NewPRStore(nil)
	for _, pr := range []PendingPR{
		blockedPR("org/a", 1, "read failed"),
		blockedPR("org/b", 2, "rate limit"),
		{Repo: "org/c", Number: 3, URL: "u", MergeBy: time.Now()}, // never blocked
	} {
		if err := s.Add(pr); err != nil {
			t.Fatal(err)
		}
	}

	// One named entry: exactly that one is released.
	freed, err := s.ResumeBlocked("org/a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(freed) != 1 || freed[0].Repo != "org/a" {
		t.Fatalf("want exactly org/a released, got %+v", freed)
	}
	all, _ := s.List()
	for _, p := range all {
		switch p.Repo {
		case "org/a":
			if p.Blocked || p.BlockedReason != "" || !p.BlockedAt.IsZero() {
				t.Errorf("org/a still carries its blockade: %+v", p)
			}
			// The spent retry episode goes with it — the next attempt starts fresh, it does not
			// continue a series that is already exhausted.
			if p.Backoff != nil {
				t.Errorf("org/a kept its spent retry state: %+v", p.Backoff)
			}
		case "org/b":
			if !p.Blocked {
				t.Error("org/b was released although it was not named")
			}
		}
	}

	// No argument: everything still blocked is released.
	freed, err = s.ResumeBlocked("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(freed) != 1 || freed[0].Repo != "org/b" {
		t.Fatalf("want org/b released, got %+v", freed)
	}
	all, _ = s.List()
	for _, p := range all {
		if p.Blocked {
			t.Errorf("%s #%d is still blocked", p.Repo, p.Number)
		}
	}

	// It merges nothing and removes nothing — all three entries are still tracked.
	if len(all) != 3 {
		t.Fatalf("the pool lost entries: %d of 3", len(all))
	}

	// Pressing it again is harmless and says so by releasing nothing.
	freed, err = s.ResumeBlocked("", 0)
	if err != nil || len(freed) != 0 {
		t.Fatalf("a second release must free nothing, got %d (%v)", len(freed), err)
	}
}

// An untracked pull request is not invented by naming it.
func TestResumingAnUnknownPullRequestChangesNothing(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS_PRS", filepath.Join(t.TempDir(), "prs.json"))
	s := NewPRStore(nil)
	if err := s.Add(blockedPR("org/a", 1, "read failed")); err != nil {
		t.Fatal(err)
	}
	freed, err := s.ResumeBlocked("org/zzz", 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(freed) != 0 {
		t.Fatalf("an unknown pull request was reported as released: %+v", freed)
	}
	all, _ := s.List()
	if len(all) != 1 || !all[0].Blocked {
		t.Fatalf("the pool changed: %+v", all)
	}
}
