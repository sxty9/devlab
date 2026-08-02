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

// A run that committed work but could NOT ship it leaves the durable "Lieferung gescheitert" mark
// (WHAT-1): deliver-dev fails after implement produced commits, so the pull-request stage never
// runs and the ledger must record the failed delivery — with the span, the branch, and the reason.
func TestFailedDeliveryMarkRecorded(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	deps.deploy.deliverErr = func(int) error { return errors.New("delivery not yet set up: no unit installed for this service") }
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) || rf.Failed != 1 {
		t.Fatalf("want one failed repo, got %v", err)
	}

	if len(deps.deliver.openCalls) != 0 {
		t.Fatalf("the failed run never reached the pull-request stage, yet opened a PR: %+v", deps.deliver.openCalls)
	}
	if len(deps.deliver.failedCalls) != 1 {
		t.Fatalf("the committed-but-unshipped work must be marked failed exactly once, got %d", len(deps.deliver.failedCalls))
	}
	fc := deps.deliver.failedCalls[0]
	if fc.DeliveryID == "" || fc.ExecutionID != "exec_test" || fc.Repo != "org/app" {
		t.Fatalf("the failed mark misses its identity: %+v", fc)
	}
	if !strings.HasPrefix(fc.Branch, "fix/") || fc.FromCommit == "" || fc.ToCommit == "" {
		t.Fatalf("the failed mark misses the branch/span: %+v", fc)
	}
	if !strings.Contains(fc.Reason, "deliver-dev") {
		t.Fatalf("the failed mark must name the failing stage, got %q", fc.Reason)
	}
}

// A new order is HELD before it branches when the repository's tip is ANOTHER order's failed
// delivery (WHAT-2): the stack does not grow past a failed layer. The order waits, blocked, with a
// named reason (both ways back spelled out) — it never branches, so it can never omit the tip's work.
func TestHeldBeforeBranchingOnFailedTip(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	// The repo's tip is an EARLIER order's failed delivery; this order does not own it (its preflight
	// finding carries no OpenDelivery), so it must be held.
	deps.deliver.failedTip = &runs.Delivery{ID: "dlv_prev", Repo: "org/app", Branch: "fix/prev-aaa111", FailedReason: "deliver-dev stage failed: stale root script"}
	sink := newFakeSink()

	err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink)
	var rf *ReposFailedError
	if !errors.As(err, &rf) || rf.Blocked != 1 {
		t.Fatalf("a held order must be a BLOCKED repo, got %v", err)
	}

	rp, _ := sink.done("org/app")
	if rp.Succeeded || rp.Block == nil {
		t.Fatalf("the held repo must be blocked and not succeed: %+v", rp)
	}
	// It never branched: no PR path, no base resolution into a delivery, no new failed mark of its own.
	if len(deps.deliver.openCalls) != 0 {
		t.Fatalf("a held order must not open a PR, got %+v", deps.deliver.openCalls)
	}
	if len(deps.deliver.failedCalls) != 0 {
		t.Fatalf("a held order produced no commits, so it marks nothing failed, got %+v", deps.deliver.failedCalls)
	}
	// The implement stage is where it is held, and the reason names the failed tip and both ways back.
	sv, ok := sink.terminal("org/app", model.StageImplement)
	if !ok || sv.State != model.StepFailed {
		t.Fatalf("implement must be the held (blocked) stage: %+v", sv)
	}
	for _, want := range []string{"dlv_prev", "fix/prev-aaa111", "roll it back", "re-running the failed order"} {
		if !strings.Contains(sv.Reason, want) {
			t.Fatalf("the hold reason misses %q:\n%s", want, sv.Reason)
		}
	}
	// The later stages did not run.
	for _, st := range []model.Stage{model.StageDeliverDev, model.StagePublish, model.StagePullRequest} {
		if s, _ := sink.terminal("org/app", st); s.State != model.StepNotExecuted {
			t.Fatalf("%s must be not-executed behind the hold, got %s", st, s.State)
		}
	}
}

// This order's OWN failed tip is NOT a hold — it is the resolve-at-the-tip path: preflight surfaces
// the order's own unsettled delivery as OpenDelivery, the rest path adopts it, and the order
// re-delivers instead of waiting on itself.
func TestOwnFailedTipIsNotHeld(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("org/app")
	own := runs.Delivery{ID: "dlv_own", Repo: "org/app", Branch: "fix/own-aaa111", FromCommit: "b0", ToCommit: "w1", FailedReason: "deliver-dev failed"}
	deps.deliver.failedTip = &own
	deps.findings["org/app"] = preflight.Finding{
		State:        model.TaskImplementedUndelivered,
		OpenDelivery: &own,
		Evidence:     []string{"open delivery dlv_own recorded in the ledger"},
	}
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "org/app"), sink); err != nil {
		t.Fatalf("the order must resolve its own tip, not be held: %v", err)
	}
	rp, _ := sink.done("org/app")
	if !rp.Succeeded {
		t.Fatalf("re-delivering the own failed tip must succeed: %+v", rp)
	}
	if len(deps.deliver.openCalls) != 1 || deps.deliver.openCalls[0].DeliveryID != "dlv_own" {
		t.Fatalf("the rest path must adopt the own delivery and ship it, got %+v", deps.deliver.openCalls)
	}
}
