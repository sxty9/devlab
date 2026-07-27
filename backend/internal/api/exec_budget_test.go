package api

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// repoBudget: a run's own choice overrides the service default; an empty choice FOLLOWS the live default
// (so a later default change is tracked, not copied); an explicit "0" is a deliberate no-cap pass.
func TestRepoBudget(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "")
	st := runs.NewSettings()
	x := &runExecutor{s: &Server{settings: st}}

	if d, lbl := x.repoBudget(runs.Run{}); d != 3*time.Hour || lbl != "3h" {
		t.Fatalf("empty budget = (%s,%q), want (3h,3h)", d, lbl)
	}
	if d, lbl := x.repoBudget(runs.Run{TimeBudget: "90m"}); d != 90*time.Minute || lbl != "1h30m" {
		t.Fatalf("own 90m = (%s,%q), want (1h30m,1h30m)", d, lbl)
	}
	if d, lbl := x.repoBudget(runs.Run{TimeBudget: "0"}); d != 0 || lbl != "0" {
		t.Fatalf("own no-cap = (%s,%q), want (0,0)", d, lbl)
	}

	// Change the service default: a run without its own choice tracks it; a run WITH a choice does not.
	if err := st.Set(runs.Config{DefaultTimeBudget: "5h"}); err != nil {
		t.Fatal(err)
	}
	if d, lbl := x.repoBudget(runs.Run{}); d != 5*time.Hour || lbl != "5h" {
		t.Fatalf("after default change, empty budget = (%s,%q), want (5h,5h)", d, lbl)
	}
	if d, _ := x.repoBudget(runs.Run{TimeBudget: "90m"}); d != 90*time.Minute {
		t.Fatalf("an explicit choice must not track the default: got %s", d)
	}
}

// repoErr names a time-budget stop honestly — no technical stage prefix, carrying the exceeded value —
// while every other failure keeps its stage prefix. The budget error is recognised even when wrapped.
func TestRepoErr(t *testing.T) {
	be := repoErr("implement", budgetExceeded{3 * time.Hour})
	if strings.HasPrefix(be, "implement:") {
		t.Fatalf("a budget stop must not carry a technical stage prefix: %q", be)
	}
	if !strings.Contains(be, "Time budget exceeded") || !strings.Contains(be, "3h") {
		t.Fatalf("a budget stop must name the budget and its value: %q", be)
	}
	if plain := repoErr("implement", errors.New("boom")); plain != "implement: boom" {
		t.Fatalf("an ordinary failure = %q, want stage-prefixed", plain)
	}
	if wrapped := repoErr("analyze", fmt.Errorf("outer: %w", budgetExceeded{2 * time.Hour})); strings.HasPrefix(wrapped, "analyze:") || !strings.Contains(wrapped, "2h") {
		t.Fatalf("a wrapped budget error must still be recognised: %q", wrapped)
	}
}

// timedOut preserves the streamed transcript (what the pass reached) and appends the honest budget note —
// unlike fail(), which replaces the log with the raw error. That is the "name what was achieved" the
// axioms require of a budget-terminated pass.
func TestAgentStepTimedOutPreservesTranscript(t *testing.T) {
	rr := &runs.RepoResult{}
	ag := beginAgentStep(rr, &liveSaver{do: func() {}}, "implement")
	ag.onProgress([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"scaffolded the service"}]}}`))
	ag.timedOut(budgetTimedOutNote(3 * time.Hour))

	step := rr.Steps[len(rr.Steps)-1]
	if step.Running || step.OK {
		t.Fatalf("a timed-out step must be finished and not-OK: running=%v ok=%v", step.Running, step.OK)
	}
	if !strings.Contains(step.Log, "scaffolded the service") {
		t.Fatalf("the transcript (what was achieved) must be preserved: %q", step.Log)
	}
	if !strings.Contains(step.Log, "Time budget exceeded") || !strings.Contains(step.Log, "3h") {
		t.Fatalf("the honest budget note must be present: %q", step.Log)
	}
}
