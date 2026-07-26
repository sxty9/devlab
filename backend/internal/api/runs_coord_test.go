package api

import (
	"math"
	"testing"
	"time"
)

// The spend ceiling and the subscription-limit pause apply to all concurrent runs TOGETHER (in Summe,
// nicht je Lauf). These exercise the shared coordinator a single runExecutor hands to every run goroutine.

func TestAggregateSpendSumsConcurrentRunsAndClears(t *testing.T) {
	x := &runExecutor{}

	// Two runs live at once contribute to one shared pool — the ceiling sees their SUM, not either alone.
	x.addSpend(3.0) // run A, first repo
	x.addSpend(4.0) // run B, first repo
	if got := x.aggregateSpend(); math.Abs(got-7.0) > 1e-9 {
		t.Fatalf("aggregate spend = %.4f, want 7.0 (both runs summed)", got)
	}

	// A finishing run removes its own contribution, so the pool reflects only runs still live — a
	// carried-over run therefore resumes against a pool cleared of its finished siblings.
	x.addSpend(-3.0) // run A exits, having contributed 3.0
	if got := x.aggregateSpend(); math.Abs(got-4.0) > 1e-9 {
		t.Fatalf("aggregate spend after run A exits = %.4f, want 4.0", got)
	}

	// Float drift can never push the pool below zero.
	x.addSpend(-999.0)
	if got := x.aggregateSpend(); got != 0 {
		t.Fatalf("aggregate spend clamped = %.4f, want 0", got)
	}
}

func TestSharedUsageLimitKeepsLatestReset(t *testing.T) {
	x := &runExecutor{}
	if !x.limitedUntil().IsZero() {
		t.Fatal("no limit noted yet — limitedUntil should be zero")
	}

	early := time.Now().Add(10 * time.Minute)
	late := time.Now().Add(30 * time.Minute)

	x.noteLimit(late)
	x.noteLimit(early) // an earlier reset must not shorten the shared pause
	if got := x.limitedUntil(); !got.Equal(late) {
		t.Fatalf("shared limit = %s, want the later reset %s", got, late)
	}
}

func TestResumeAtForPrefersReportedReset(t *testing.T) {
	// A reported, still-future reset wins (with a small cushion past it).
	reset := time.Now().Add(20 * time.Minute)
	got := resumeAtFor(repoSignal{limited: true, hasReset: true, resetAt: reset})
	if got.Before(reset) {
		t.Fatalf("resumeAtFor = %s, must be at or after the reported reset %s", got, reset)
	}

	// With no reported reset, fall back to the fixed backoff from now.
	before := time.Now()
	got = resumeAtFor(repoSignal{limited: true})
	if got.Before(before.Add(limitBackoff() - time.Second)) {
		t.Fatalf("resumeAtFor without a reset = %s, want ~now+backoff (%s)", got, limitBackoff())
	}
}
