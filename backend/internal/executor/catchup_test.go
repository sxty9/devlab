package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
)

// behindUnderstack is the preflight measurement of a branch resting N commits behind the tip, with
// the deliveries that landed in between.
func behindUnderstack(behind int, spans ...preflight.DeliverySpan) *preflight.Understack {
	return &preflight.Understack{
		TipBranch: "main", TipCommit: "7175bee", ForkCommit: "f04c0", Behind: behind, Intervening: spans,
	}
}

// resumeRequest is a request that resumes at the implement stage (a paused order being continued) —
// the shape of the "paused, then another order delivered, then resumed" case.
func resumeRequest(repo string) Request {
	req := mkRequest(model.KindTodo, repo)
	req.Doc.Continuation = &model.ContinuationView{Repo: repo, Stage: model.StageImplement}
	return req
}

// Test case 1: a branch already on the tip is not touched — no rebase, no prompt section, and the
// order runs normally.
func TestCatchUpOnTipNoop(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.understack = map[string]*preflight.Understack{"org/app": behindUnderstack(0)}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("an on-tip branch runs normally: %v", err)
	}
	if calls := deps.benches["org/app"].catchUpCalls; len(calls) != 0 {
		t.Errorf("a branch on the tip must not be rebased, got catch-up calls %v", calls)
	}
	if p := lastAgentPrompt(deps, "org/app"); strings.Contains(p, "caught up onto the current stack tip") {
		t.Errorf("no catch-up section belongs in the prompt of an on-tip branch")
	}
}

// Test case 2 + 4: a branch N layers behind is caught up (rebased) before the agent runs, and the
// agent's prompt names from where to where and which deliveries landed in between. Modeled on a
// resume — the paused-then-continued case — so the agent path (not the rest path) runs.
func TestCatchUpBehindCleanRebasesAndTellsTheAgent(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"this task's branch is ahead of the default branch @deadbeef"},
	}
	deps.understack = map[string]*preflight.Understack{"org/app": behindUnderstack(3,
		preflight.DeliverySpan{Title: "Passive comment pool", Branch: "fix/comments-bbb222"},
		preflight.DeliverySpan{Title: "Atomic delivery ledger", Branch: "fix/ledger-ccc333"},
	)}
	deps.benches["org/app"].catchUp = CatchUpInfo{Rebased: true, OldHead: "0old000", NewHead: "1new111"}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, resumeRequest("org/app"), sink); err != nil {
		t.Fatalf("a clean catch-up runs the order normally: %v", err)
	}
	calls := deps.benches["org/app"].catchUpCalls
	if len(calls) != 1 || calls[0] != "main" {
		t.Fatalf("the branch must be caught up onto the tip 'main' exactly once, got %v", calls)
	}
	prompt := lastAgentPrompt(deps, "org/app")
	for _, want := range []string{
		"caught up onto the current stack tip",
		"fix/comments-bbb222", "fix/ledger-ccc333",
		"2 delivery/deliveries landed in between",
		"not that the work is still correct",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the agent prompt must name the catch-up (%q):\n%s", want, prompt)
		}
	}
	// A clean catch-up is NOT a failure: no failed delivery, no block, the repo succeeds.
	rp, _ := sink.done("org/app")
	if !rp.Succeeded || rp.Block != nil {
		t.Errorf("a clean catch-up must not block or fail the repo: %+v", rp)
	}
	if len(deps.deliver.failedCalls) != 0 {
		t.Errorf("a clean catch-up records no failed delivery, got %+v", deps.deliver.failedCalls)
	}
}

// The rest path (an implemented-undelivered ToDo left lying, not resuming) is ALSO caught up before
// it re-delivers: the branch is rebased onto the tip, and the order still ships normally.
func TestCatchUpBehindRestPath(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"this task's branch is ahead of the default branch @deadbeef"},
	}
	deps.understack = map[string]*preflight.Understack{"org/app": behindUnderstack(2)}
	deps.benches["org/app"].catchUp = CatchUpInfo{Rebased: true, OldHead: "0old000", NewHead: "1new111"}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("the rest path with a caught-up branch ships normally: %v", err)
	}
	if calls := deps.benches["org/app"].catchUpCalls; len(calls) != 1 {
		t.Fatalf("the rest path must catch the branch up too, got %v", calls)
	}
	// The agent never ran (rest path), so no prompt section — but the delivery still went out.
	if len(deps.agentCalls) != 0 {
		t.Errorf("the rest path runs no agent, got %d agent call(s)", len(deps.agentCalls))
	}
	if len(deps.deliver.openCalls) != 1 {
		t.Errorf("the caught-up rest path must still open its PR, got %+v", deps.deliver.openCalls)
	}
}

// A branch with a RECORDED open delivery is left as it stands — that is a re-delivery of a fixed
// span, not weiterbauen, and rebasing it would rewrite an already-opened PR's branch.
func TestCatchUpSkippedForRecordedDelivery(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	open := runs.Delivery{ID: "dlv_open", Repo: "org/app", Branch: "fix/app-aaa111", FromCommit: "b0", ToCommit: "w1"}
	deps.findings["org/app"] = preflight.Finding{
		State: model.TaskImplementedUndelivered, OpenDelivery: &open,
		Evidence: []string{"open delivery dlv_open recorded in the ledger"},
	}
	deps.understack = map[string]*preflight.Understack{"org/app": behindUnderstack(2)}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("re-delivering a recorded span runs normally: %v", err)
	}
	if calls := deps.benches["org/app"].catchUpCalls; len(calls) != 0 {
		t.Errorf("a recorded open delivery must not be rebased, got %v", calls)
	}
}

// Test case 3: a catch-up that CONFLICTS blocks the order with a named reason listing the files, the
// branch stays exactly as it was (no failed delivery mark), and the later stages do not run.
func TestCatchUpConflictBlocks(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.findings["org/app"] = preflight.Finding{
		State:    model.TaskImplementedUndelivered,
		Evidence: []string{"this task's branch is ahead of the default branch @deadbeef"},
	}
	deps.understack = map[string]*preflight.Understack{"org/app": behindUnderstack(2)}
	deps.benches["org/app"].catchUp = CatchUpInfo{
		Conflicted: true, ConflictFiles: []string{"backend/app.go", "web/App.tsx"}, OldHead: "0old000", NewHead: "0old000",
	}
	sink := newFakeSink()

	err := Execute(context.Background(), deps, resumeRequest("org/app"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) || rf.Blocked != 1 {
		t.Fatalf("a catch-up conflict must BLOCK the repo, got %v", err)
	}
	rp, _ := sink.done("org/app")
	if rp.Succeeded || rp.Block == nil {
		t.Fatalf("the blocked repo must not succeed and must carry its block: %+v", rp)
	}
	sv, ok := sink.terminal("org/app", model.StageImplement)
	if !ok || sv.State != model.StepFailed {
		t.Fatalf("implement must be the blocked stage: %+v", sv)
	}
	for _, want := range []string{"catch-up blocked", "backend/app.go", "web/App.tsx", "resume this order", "exactly as it was"} {
		if !strings.Contains(sv.Reason, want) {
			t.Errorf("the block reason misses %q:\n%s", want, sv.Reason)
		}
	}
	// The branch stayed as it was: nothing was committed, so nothing is marked a failed delivery, and
	// no PR was opened.
	if len(deps.deliver.failedCalls) != 0 {
		t.Errorf("a conflict leaves the branch untouched — no failed delivery, got %+v", deps.deliver.failedCalls)
	}
	if len(deps.deliver.openCalls) != 0 {
		t.Errorf("a blocked order opens no PR, got %+v", deps.deliver.openCalls)
	}
	for _, st := range []model.Stage{model.StageDeliverDev, model.StagePublish, model.StagePullRequest} {
		if s, _ := sink.terminal("org/app", st); s.State != model.StepNotExecuted {
			t.Errorf("%s must be not-executed behind the block, got %s", st, s.State)
		}
	}
}

// The preflight stage records the understack in its protocol (log) AND on the finding — measured,
// visible, before any rebase.
func TestPreflightRecordsUnderstack(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.understack = map[string]*preflight.Understack{"org/app": behindUnderstack(3,
		preflight.DeliverySpan{Title: "Atomic delivery ledger", Branch: "fix/ledger-ccc333"},
	)}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sv, ok := sink.terminal("org/app", model.StagePreflight)
	if !ok {
		t.Fatal("no terminal preflight view")
	}
	for _, want := range []string{"sits 3 commit(s) behind the current tip main", "landed since: Atomic delivery ledger"} {
		if !strings.Contains(sv.Log, want) {
			t.Errorf("the preflight protocol must name the understack (%q):\n%s", want, sv.Log)
		}
	}
}

// lastAgentPrompt returns the prompt of the last agent call for a repo ("" when none).
func lastAgentPrompt(d *fakeDeps, repo string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := len(d.agentCalls) - 1; i >= 0; i-- {
		if d.agentCalls[i].repo == repo {
			return d.agentCalls[i].prompt
		}
	}
	return ""
}
