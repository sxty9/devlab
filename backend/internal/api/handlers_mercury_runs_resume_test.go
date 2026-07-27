package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// newResumeExec builds a minimal executor over a fresh, empty results directory, in the given safety mode.
func newResumeExec(t *testing.T, mode string) *runExecutor {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(t.TempDir(), "res"))
	return &runExecutor{s: &Server{runResults: runs.NewResults()}, mode: mode}
}

// saveHusk persists an unfinished (or finished) execution with the given completed repos. Save stamps
// UpdatedAt to now, so the husk is worked-recently and thus inside the stranded window.
func saveHusk(t *testing.T, x *runExecutor, runID, resultID, mode string, doneRepos []string, finished bool) {
	t.Helper()
	res := runs.Result{RunID: runID, ResultID: resultID, Mode: mode, StartedAt: time.Now().Add(-10 * time.Minute)}
	for _, r := range doneRepos {
		res.Repos = append(res.Repos, runs.RepoResult{Repo: r, OK: true, PRUrl: "https://gh/" + r})
	}
	if finished {
		res.FinishedAt = time.Now().UTC()
	}
	if err := x.s.runResults.Save(res); err != nil {
		t.Fatal(err)
	}
}

// TestResumeOrNewContinuesStrandedExecution is req 1 + req 6a/6b: a re-trigger after an interruption
// continues the SAME execution (same id) rather than minting a new one, and the repositories the prior
// attempt already finished stay done — so they are never re-worked into a duplicate PR.
func TestResumeOrNewContinuesStrandedExecution(t *testing.T) {
	x := newResumeExec(t, "pr")
	run := runs.Run{ID: "run_a", Name: "A", Type: runs.TypeTodo}
	saveHusk(t, x, "run_a", "res_old", "pr", []string{"o/x", "o/y"}, false)

	res, resuming := x.resumeOrNew(run)
	if !resuming {
		t.Fatal("a stranded execution must be continued, not started fresh")
	}
	if res.ResultID != "res_old" {
		t.Fatalf("resume must keep the SAME execution id: got %q want res_old", res.ResultID)
	}
	done := res.DoneRepos()
	if !done["o/x"] || !done["o/y"] {
		t.Fatalf("already-finished repos must stay done, got %v", done)
	}
}

// TestResumeOrNewStartsFreshWithoutHusk: with nothing interrupted, a new execution is minted.
func TestResumeOrNewStartsFreshWithoutHusk(t *testing.T) {
	x := newResumeExec(t, "pr")
	res, resuming := x.resumeOrNew(runs.Run{ID: "run_b", Name: "B"})
	if resuming {
		t.Fatal("nothing to resume — must start fresh")
	}
	if res.ResultID == "" || res.Mode != "pr" {
		t.Fatalf("fresh execution not minted correctly: %+v", res)
	}
}

// TestResumeOrNewReapsModeMismatch is req 3: the standing conditions stay in force. A husk from a
// DIFFERENT safety mode is not continued — it is reaped (closed out as failed) and a fresh one starts.
func TestResumeOrNewReapsModeMismatch(t *testing.T) {
	x := newResumeExec(t, "pr")
	run := runs.Run{ID: "run_c", Name: "C"}
	saveHusk(t, x, "run_c", "res_report", "report", []string{"o/x"}, false)

	res, resuming := x.resumeOrNew(run)
	if resuming || res.ResultID == "res_report" {
		t.Fatalf("a report-mode husk must not be continued in pr mode: resuming=%v id=%q", resuming, res.ResultID)
	}
	h, ok, _ := x.s.runResults.Get("run_c", "res_report")
	if !ok || h.FinishedAt.IsZero() || h.OK {
		t.Fatalf("mode-mismatched husk must be reaped (finished + failed): %+v ok=%v", h, ok)
	}
}

// TestPlanResumePreviewsContinuation is req 2: a trigger can learn — synchronously and read-only — that it
// will CONTINUE, which execution, and how far it got, without disturbing the husk.
func TestPlanResumePreviewsContinuation(t *testing.T) {
	x := newResumeExec(t, "pr")
	run := runs.Run{ID: "run_d", Name: "D"}
	saveHusk(t, x, "run_d", "res_old", "pr", []string{"o/x"}, false)

	plan := x.PlanResume(run, false)
	if plan.Action != runs.ResumeContinue {
		t.Fatalf("plan.Action = %q, want resume", plan.Action)
	}
	if plan.ResultID != "res_old" || plan.ReposDone != 1 {
		t.Fatalf("plan must name the execution and its progress: %+v", plan)
	}
	if plan.Reason == "" {
		t.Error("plan must explain the decision (req 2)")
	}
	if h, _, _ := x.s.runResults.Get("run_d", "res_old"); !h.FinishedAt.IsZero() {
		t.Error("a read-only preview must NOT finish the husk")
	}
}

// TestPlanResumeExplicitFreshDiscards is req 4 + req 6c: an explicit fresh start discards the interrupted
// execution VISIBLY — the husk is reaped and marked as discarded, the plan names it (and any open PR it
// left), and a subsequent execution starts fresh instead of continuing the discarded one.
func TestPlanResumeExplicitFreshDiscards(t *testing.T) {
	x := newResumeExec(t, "pr")
	run := runs.Run{ID: "run_e", Name: "E"}
	saveHusk(t, x, "run_e", "res_old", "pr", []string{"o/x", "o/y"}, false)

	plan := x.PlanResume(run, true)
	if plan.Action != runs.ResumeFresh {
		t.Fatalf("explicit fresh must not continue: %+v", plan)
	}
	if plan.Discarded != "res_old" {
		t.Fatalf("plan must name the discarded execution: %+v", plan)
	}
	if plan.DiscardedPRUrl == "" {
		t.Error("plan should surface the open PR the discarded execution left (req 5 spirit)")
	}
	// The discarded husk is reaped: finished, failed, and visibly marked.
	h, ok, _ := x.s.runResults.Get("run_e", "res_old")
	if !ok || h.FinishedAt.IsZero() || h.OK {
		t.Fatalf("discarded husk must be reaped (finished + failed): %+v ok=%v", h, ok)
	}
	if n := len(h.Repos); n == 0 || !strings.Contains(h.Repos[n-1].Error, "discarded") {
		t.Errorf("discarded husk must carry a visible marker, got %+v", h.Repos)
	}
	// And the next execution starts fresh — the discarded husk is not resumed.
	if _, resuming := x.resumeOrNew(run); resuming {
		t.Error("a discarded execution must not be resumed afterwards")
	}
}

// TestAdoptPriorDeliveryReusesOpenPR is req 5: when a prior attempt of the SAME execution already opened a
// pull request for a repo (recorded in the ledger) but its repo-result was lost to a crash, the resume
// adopts that pull request instead of implementing again and opening a duplicate. A different execution
// has nothing to adopt — it would legitimately open its own.
func TestAdoptPriorDeliveryReusesOpenPR(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS_DELIVERIES", filepath.Join(t.TempDir(), "d.json"))
	x := &runExecutor{s: &Server{deliveries: runs.NewDeliveryStore()}, mode: "pr"}
	if err := x.s.deliveries.Add(runs.Delivery{
		ID: "dlv_1", RunID: "run_a", ResultID: "res_1", Repo: "o/x",
		DevBranch: "mercury-dev", ToCommit: "abc123", PRUrl: "https://gh/pr/7", BaseBranch: "main",
		Status: runs.DeliveryOpen,
	}); err != nil {
		t.Fatal(err)
	}
	run := runs.Run{ID: "run_a"}

	rr, ok := x.adoptPriorDelivery(run, &runs.Result{RunID: "run_a", ResultID: "res_1"}, "o/x", "o/x")
	if !ok {
		t.Fatal("the same execution's already-open PR must be adopted (req 5)")
	}
	if rr.PRUrl != "https://gh/pr/7" || rr.DeliveryID != "dlv_1" || rr.PRBase != "main" || rr.DevCommit != "abc123" {
		t.Fatalf("adopted repo-result must carry the existing PR/delivery: %+v", rr)
	}
	if !rr.OK || len(rr.Steps) != 1 || rr.Steps[0].Status != runs.StepOK {
		t.Fatalf("adopted repo must be a clean, succeeded chain: %+v", rr)
	}

	// A different execution of the same run has nothing to adopt — the guard must not fire (it would
	// otherwise wrongly suppress a legitimate, separate delivery).
	if _, ok := x.adoptPriorDelivery(run, &runs.Result{RunID: "run_a", ResultID: "res_2"}, "o/x", "o/x"); ok {
		t.Error("must NOT adopt a delivery across a different execution")
	}
	// No ledger at all → no-op (tests / report mode).
	bare := &runExecutor{s: &Server{}, mode: "pr"}
	if _, ok := bare.adoptPriorDelivery(run, &runs.Result{RunID: "run_a", ResultID: "res_1"}, "o/x", "o/x"); ok {
		t.Error("no delivery ledger must be a safe no-op")
	}
}
