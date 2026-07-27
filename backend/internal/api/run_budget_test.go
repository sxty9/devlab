package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// TestEffectiveTimeBudget pins the resolution the executor applies per repo: a run's own choice wins
// and reports as a deviation; "0" is a deliberate no-cap; an unset run FOLLOWS the service default (and
// tracks a later change to it, because it is resolved live rather than copied).
func TestEffectiveTimeBudget(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	x := &runExecutor{s: &Server{config: runs.NewConfigStore()}}

	// Unset → the built-in three-hour default, not flagged as a deviation.
	if d, over := x.effectiveTimeBudget(runs.Run{}); d != runs.DefaultTimeBudgetFallback || over {
		t.Fatalf("unset: got %v/override=%v, want 3h/false", d, over)
	}
	// Explicit value overrides and is a deviation.
	if d, over := x.effectiveTimeBudget(runs.Run{TimeBudget: "30m"}); d != 30*time.Minute || !over {
		t.Fatalf("override: got %v/override=%v, want 30m/true", d, over)
	}
	// "0" is a deliberate no-cap (and still a deviation from the default).
	if d, over := x.effectiveTimeBudget(runs.Run{TimeBudget: "0"}); d != 0 || !over {
		t.Fatalf("no-cap: got %v/override=%v, want 0/true", d, over)
	}

	// Change the service default → an unset run follows it (referenced, not copied).
	if err := x.s.config.Set(runs.Config{DefaultTimeBudget: "6h"}); err != nil {
		t.Fatalf("Set default: %v", err)
	}
	if d, over := x.effectiveTimeBudget(runs.Run{}); d != 6*time.Hour || over {
		t.Fatalf("follows changed default: got %v/override=%v, want 6h/false", d, over)
	}
	// A run WITH its own choice ignores the changed default.
	if d, _ := x.effectiveTimeBudget(runs.Run{TimeBudget: "30m"}); d != 30*time.Minute {
		t.Fatalf("override ignores default: got %v, want 30m", d)
	}
}

// TestBudgetTimeoutMessagesAreHonest pins that a timeout is named as an exceeded budget with its value,
// not a raw technical error, and that the report note points at what was achieved.
func TestBudgetTimeoutMessagesAreHonest(t *testing.T) {
	err := budgetTimeoutError(3 * time.Hour)
	if !strings.Contains(err, "Zeitbudget") || !strings.Contains(err, "3h") {
		t.Errorf("repo error should name the budget: %q", err)
	}
	if strings.Contains(strings.ToLower(err), "signal") || strings.Contains(strings.ToLower(err), "killed") {
		t.Errorf("repo error must not read as a raw kill: %q", err)
	}
	note := budgetTimeoutNote(90 * time.Minute)
	if !strings.Contains(note, "1h30m") {
		t.Errorf("note should carry the compact budget: %q", note)
	}
}
