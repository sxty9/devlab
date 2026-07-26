package api

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

func tempRunStore(t *testing.T) *runs.Store {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(t.TempDir(), "history"))
	return runs.NewStore()
}

// rollbackExecutor wires an executor with temp stores and the git/GitHub sides stubbed, so the rollback
// DECISION logic runs deterministically.
func rollbackExecutor(t *testing.T, mode string) (*runExecutor, *runs.DeliveryStore, *runs.PRStore, *runs.Store) {
	t.Helper()
	deliv := tempDeliveryStore(t)
	prs := tempPRStore(t)
	rstore := tempRunStore(t)
	x := &runExecutor{
		s:              &Server{runs: rstore, deliveries: deliv, runPRs: prs},
		mode:           mode,
		tokenUser:      "owner",
		autoMergeAfter: time.Hour,
		tokenFn:        func(string) (string, error) { return "tok", nil },
	}
	return x, deliv, prs, rstore
}

var t0 = time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)

// TestRollbackConflictRaisesTodo is req 12 + 13c: when later work builds on the delivery, the counter-
// booking conflicts and the tool makes NO guess — it raises a concrete ToDo (targeting the repo, naming
// the delivery and the later work) instead of discarding anything, and leaves the delivery un-reverted.
func TestRollbackConflictRaisesTodo(t *testing.T) {
	x, deliv, _, rstore := rollbackExecutor(t, "pr")
	_ = deliv.Add(runs.Delivery{ID: "d1", Repo: "o/x", Branch: "mercury-run/d1", DevBranch: "mercury-dev",
		FromCommit: "aaaaaaa", ToCommit: "bbbbbbb", PRNumber: 5, Status: runs.DeliveryOpen, RunName: "Axiom Sweep", CreatedAt: t0})
	_ = deliv.Add(runs.Delivery{ID: "d2", Repo: "o/x", Branch: "mercury-run/d2", PRNumber: 6, Status: runs.DeliveryOpen, RunName: "Follow-up", CreatedAt: t0.Add(time.Minute)})

	x.getPRFn = func(context.Context, string, string, int) (github.PullRequest, error) {
		return github.PullRequest{State: "open"}, nil
	}
	x.counterBookFn = func(context.Context, string, runs.Delivery, string) (counterBookResult, error) {
		return counterBookResult{conflicted: true, defaultBranch: "main"}, nil
	}

	out, err := x.RollbackDelivery(context.Background(), "d1", "tester")
	if err != nil {
		t.Fatalf("RollbackDelivery: %v", err)
	}
	if !out.Conflict || out.TodoID == "" || out.LaterOpen != 1 {
		t.Fatalf("expected conflict with a ToDo and 1 later-open, got %+v", out)
	}
	if out.Reverted {
		t.Errorf("a conflicting rollback must NOT mark the delivery reverted")
	}
	// The delivery stays un-reverted (the ToDo will counter-book it by hand).
	if d, _, _ := deliv.Get("d1"); d.Status != runs.DeliveryOpen {
		t.Errorf("delivery status = %q, want still open", d.Status)
	}
	// A concrete ToDo was created, targeting the repo and naming both deliveries.
	all, _ := rstore.List()
	var todo *runs.Run
	for i := range all {
		if all[i].ID == out.TodoID {
			todo = &all[i]
		}
	}
	if todo == nil {
		t.Fatalf("no ToDo %s created", out.TodoID)
	}
	if !todo.IsTodo() || !todo.Enabled || todo.DueAt == nil {
		t.Errorf("rollback ToDo must be an enabled, due todo, got %+v", todo)
	}
	if len(todo.Targets) != 1 || todo.Targets[0].Repo != "x" {
		t.Errorf("ToDo must target repo x, got %+v", todo.Targets)
	}
	if !strings.Contains(todo.Task, "d1") || !strings.Contains(todo.Task, "d2") {
		t.Errorf("ToDo task must name the delivery and the later work, got:\n%s", todo.Task)
	}
	if !strings.Contains(strings.ToLower(todo.Task), "not rewrite history") {
		t.Errorf("ToDo must forbid history rewriting, got:\n%s", todo.Task)
	}
}

// TestRollbackOpenClosesPR is req 11 (open direction): a clean counter-booking of a still-open delivery
// closes its PR with a justification and marks the delivery reverted — no reversing PR.
func TestRollbackOpenClosesPR(t *testing.T) {
	x, deliv, _, _ := rollbackExecutor(t, "pr")
	_ = deliv.Add(runs.Delivery{ID: "d1", Repo: "o/x", Branch: "mercury-run/d1", DevBranch: "mercury-dev",
		FromCommit: "aaaaaaa", ToCommit: "bbbbbbb", PRNumber: 5, Status: runs.DeliveryOpen, RunName: "R1", CreatedAt: t0})

	var closed, commented int
	x.getPRFn = func(context.Context, string, string, int) (github.PullRequest, error) {
		return github.PullRequest{State: "open", Merged: false}, nil
	}
	x.counterBookFn = func(context.Context, string, runs.Delivery, string) (counterBookResult, error) {
		return counterBookResult{before: "aaaaaaa", after: "ccccccc", defaultBranch: "main"}, nil
	}
	x.commentPRFn = func(context.Context, string, string, int, string) error { commented++; return nil }
	x.closePRFn = func(context.Context, string, string, int) error { closed++; return nil }

	out, err := x.RollbackDelivery(context.Background(), "d1", "tester")
	if err != nil {
		t.Fatalf("RollbackDelivery: %v", err)
	}
	if !out.Reverted || out.ClosedPR != 5 || out.ReversalPRUrl != "" {
		t.Fatalf("expected reverted + closed PR 5 + no reversal, got %+v", out)
	}
	if closed != 1 || commented != 1 {
		t.Errorf("expected the PR commented once and closed once, got comment=%d close=%d", commented, closed)
	}
	d, _, _ := deliv.Get("d1")
	if d.Status != runs.DeliveryReverted || d.RevertedAt == nil || d.RevertedBy != "tester" {
		t.Errorf("delivery not marked reverted: %+v", d)
	}
	// No reversing delivery was created (nothing to re-run through the chain for an unmerged delivery).
	if list, _ := deliv.ListForRepo("o/x"); len(list) != 1 {
		t.Errorf("open rollback must not add a reversing delivery, got %d", len(list))
	}
}

// TestRollbackMergedCreatesReversingPR is req 11 (merged direction): a merged delivery's counter-booking
// becomes a reversing delivery with its own stacked PR (so main — and prod on merge — is reversed too).
func TestRollbackMergedCreatesReversingPR(t *testing.T) {
	x, deliv, prs, _ := rollbackExecutor(t, "full")
	_ = deliv.Add(runs.Delivery{ID: "d1", RunID: "run_x", ResultID: "res_1", Repo: "o/x", Branch: "mercury-run/run_x/d1",
		DevBranch: "mercury-dev", FromCommit: "aaaaaaa", ToCommit: "bbbbbbb", PRNumber: 6, Status: runs.DeliveryMerged, RunName: "R1", CreatedAt: t0})

	x.getPRFn = func(context.Context, string, string, int) (github.PullRequest, error) {
		return github.PullRequest{Merged: true, State: "closed"}, nil
	}
	x.counterBookFn = func(_ context.Context, _ string, _ runs.Delivery, reversalBranch string) (counterBookResult, error) {
		if !strings.HasPrefix(reversalBranch, "mercury-run/run_x/revert-") {
			t.Errorf("reversal branch = %q, want a stacked revert branch", reversalBranch)
		}
		return counterBookResult{before: "bbbbbbb", after: "ddddddd", defaultBranch: "main"}, nil
	}
	var createdBase string
	x.createPRFn = func(_ context.Context, _, _, head, base, _, _ string) (github.PullRequest, error) {
		createdBase = base
		return github.PullRequest{Number: 77, HTMLURL: "http://pr/77", State: "open"}, nil
	}

	out, err := x.RollbackDelivery(context.Background(), "d1", "tester")
	if err != nil {
		t.Fatalf("RollbackDelivery: %v", err)
	}
	if !out.Reverted || out.ReversalPRUrl != "http://pr/77" || out.ReversalDeliveryID == "" {
		t.Fatalf("expected a reversing PR, got %+v", out)
	}
	if createdBase != "main" {
		t.Errorf("reversing PR base = %q, want main (no other open delivery)", createdBase)
	}
	// The reversing delivery is recorded and points back at the original.
	rev, ok, _ := deliv.Get(out.ReversalDeliveryID)
	if !ok || rev.RevertOf != "d1" || rev.Status != runs.DeliveryOpen {
		t.Fatalf("reversing delivery not recorded correctly: %+v ok=%v", rev, ok)
	}
	if rev.FromCommit != "bbbbbbb" || rev.ToCommit != "ddddddd" {
		t.Errorf("reversing delivery range = %s..%s, want bbbbbbb..ddddddd", rev.FromCommit, rev.ToCommit)
	}
	// The reversing PR is tracked for auto-merge, and the original is now reverted.
	if list, _ := prs.List(); len(list) != 1 || list[0].Number != 77 {
		t.Errorf("reversing PR not tracked, got %+v", list)
	}
	if d, _, _ := deliv.Get("d1"); d.Status != runs.DeliveryReverted {
		t.Errorf("original delivery status = %q, want reverted", d.Status)
	}
}

// TestRollbackAlreadyReverted / NotFound pin the guards.
func TestRollbackGuards(t *testing.T) {
	x, deliv, _, _ := rollbackExecutor(t, "pr")
	_ = deliv.Add(runs.Delivery{ID: "d1", Repo: "o/x", Status: runs.DeliveryReverted, CreatedAt: t0})

	out, err := x.RollbackDelivery(context.Background(), "d1", "tester")
	if err != nil || !out.AlreadyReverted || out.Reverted {
		t.Errorf("already-reverted must no-op, got out=%+v err=%v", out, err)
	}
	if _, err := x.RollbackDelivery(context.Background(), "nope", "tester"); !errors.Is(err, errDeliveryNotFound) {
		t.Errorf("unknown delivery must return errDeliveryNotFound, got %v", err)
	}
}

// TestResetRepo pins the explicit way back (req 5): it resolves the repo and dispatches to the reset path.
func TestResetRepo(t *testing.T) {
	x, _, _, _ := rollbackExecutor(t, "full")
	var gotRepo string
	x.resetRepoFn = func(_ context.Context, _ string, repo model.Repo) (string, error) {
		gotRepo = repo.FullName
		return "dev branch reset to main", nil
	}
	logLine, err := x.ResetRepo(context.Background(), "o/x", "tester")
	if err != nil {
		t.Fatalf("ResetRepo: %v", err)
	}
	if gotRepo != "o/x" || logLine != "dev branch reset to main" {
		t.Errorf("ResetRepo dispatched wrong: repo=%q log=%q", gotRepo, logLine)
	}
}
