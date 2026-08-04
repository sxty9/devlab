package deliver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// fakeProd is the fixture ProdDeployer: it records the repos it was asked to deliver and answers a
// scripted outcome/error per repo. tips answers DefaultBranchTip per repo (empty = "cannot measure",
// the safe default that keeps the reconciliation from acting in tests that do not exercise it).
type fakeProd struct {
	mu    sync.Mutex
	calls []string
	out   map[string]ProdOutcome
	err   map[string]error
	tips  map[string]string
}

func (f *fakeProd) DeployProd(_ context.Context, repo string) (ProdOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, repo)
	return f.out[repo], f.err[repo]
}

func (f *fakeProd) DefaultBranchTip(_ context.Context, repo string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tips[repo], nil
}

// mergedDelivery is one merged delivery of an execution — the state a delivery reaches AFTER its PR
// merges, when it still owes the production step.
func mergedDelivery(t *testing.T, ledger *runs.DeliveryStore, id, repo, execID string) {
	t.Helper()
	merged := t0.Add(time.Hour)
	if err := ledger.Put(runs.Delivery{
		ID: id, Repo: repo, Branch: "fix/x-" + id, CreatedAt: t0, ExecutionID: execID, MergedAt: &merged,
	}); err != nil {
		t.Fatal(err)
	}
}

// endedExecution is one execution whose chain ran to the end — it has an end stamp, so only the
// delivery settlement keeps it out of the history (B-8 + WHAT-1).
func endedExecution(t *testing.T, res *runs.ResultStore, execID string) {
	t.Helper()
	end := t0.Add(90 * time.Minute)
	if err := res.Put(runs.Result{ID: execID, RunID: "run_1", Kind: model.KindTodo, StartedAt: t0, EndedAt: &end}); err != nil {
		t.Fatal(err)
	}
}

func completed(t *testing.T, ledger *runs.DeliveryStore, res *runs.ResultStore, execID string) bool {
	t.Helper()
	r, ok, err := res.Get(execID)
	if err != nil || !ok {
		t.Fatalf("get result: ok=%v err=%v", ok, err)
	}
	all, err := ledger.All()
	if err != nil {
		t.Fatal(err)
	}
	return runs.ExecutionCompleted(r, all)
}

// TestMaintainProd_HistorizesOnlyAfterProd is WHAT-7(a): a task is done — and historizes — only
// once its production delivery has succeeded, never on the merge alone.
func TestMaintainProd_HistorizesOnlyAfterProd(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	mergedDelivery(t, ledger, "dlv_1", "o/x", "exec_1")
	endedExecution(t, res, "exec_1")

	// Merged but not yet in production: the execution is NOT history-ready.
	if completed(t, ledger, res, "exec_1") {
		t.Fatal("a merged-but-not-delivered-to-production execution must not be history-ready")
	}

	prod := &fakeProd{out: map[string]ProdOutcome{"o/x": {Running: true}}}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}

	d, _, _ := ledger.ByID("dlv_1")
	if d.ProdDeployedAt == nil {
		t.Fatal("a proven-running production delivery must stamp ProdDeployedAt")
	}
	if !d.Settled() {
		t.Fatal("a merged, production-delivered delivery must be settled")
	}
	if !completed(t, ledger, res, "exec_1") {
		t.Fatal("once production succeeded, the execution must be history-ready")
	}
	r, _, _ := res.Get("exec_1")
	if r.MergedAt == nil || !r.MergedAt.Equal(*d.ProdDeployedAt) {
		t.Fatalf("the settle time must be the production instant %v, got %v", d.ProdDeployedAt, r.MergedAt)
	}
}

// TestMaintainProd_FailureDoesNotHistorizeAndKeepsStackValid is WHAT-7(b): a failed production
// delivery does NOT historize, reports itself, retries by itself, and — crucially — leaves the
// merged layer valid so the stack built on top of it does not collapse (WHAT-3).
func TestMaintainProd_FailureDoesNotHistorizeAndKeepsStackValid(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	mergedDelivery(t, ledger, "dlv_1", "o/x", "exec_1")
	endedExecution(t, res, "exec_1")

	prod := &fakeProd{err: map[string]error{"o/x": errors.New("receiver unreachable")}}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil); err == nil {
		t.Fatal("a failed production send must surface its error")
	}

	d, _, _ := ledger.ByID("dlv_1")
	// Not historized: still owes production, not settled, execution stays open.
	if d.ProdDeployedAt != nil || d.Settled() {
		t.Fatal("a failed production send must not settle the delivery")
	}
	if completed(t, ledger, res, "exec_1") {
		t.Fatal("a failed production send must keep the execution out of the history")
	}
	if !d.NeedsProd() {
		t.Fatal("a failed production send must keep owing production (it retries)")
	}
	// Retries, never given up on: the backoff is recorded and never blocks (self-ending obstacle).
	if d.ProdBackoff == nil || d.ProdBackoff.Attempts != 1 {
		t.Fatalf("a failed production send must record a growing backoff, got %+v", d.ProdBackoff)
	}
	if d.ProdFailedReason == "" {
		t.Fatal("a failed production send must name why")
	}
	// The stack is untouched: the merged layer stays merged, is not "open" (so it never re-blocks the
	// PR stack), and is not a FAILED dev tip (a production failure is not a dev-delivery failure), so
	// FailedTip reads the tip as sound — nothing built on top collapses.
	if d.MergedAt == nil || d.OpenState() {
		t.Fatal("a production failure must not disturb the merged/settled dev state")
	}
	if d.Failed() {
		t.Fatal("a production failure must NOT read as a failed dev delivery (that would block the stack)")
	}
	if tip, err := FailedTip(ledger, "o/x"); err != nil || tip != nil {
		t.Fatalf("the dev tip must stay sound after a production failure, got tip=%+v err=%v", tip, err)
	}
	// It reports itself — a disturbance the user sees.
	notes, _ := n.List()
	if len(notes) != 1 || notes[0].Kind != runs.NoticeProdUndelivered {
		t.Fatalf("a production failure must raise exactly one prod-undelivered notice, got %+v", notes)
	}

	// Retried, never given up on: with the backoff window elapsed and the receiver back, the next pass
	// re-attempts (attempts advance) and, on success, finally historizes.
	d2, _, _ := ledger.ByID("dlv_1")
	d2.ProdBackoff.NextAt = t0 // in the past → due again
	_ = ledger.Put(d2)
	prod.err = nil
	prod.out = map[string]ProdOutcome{"o/x": {Running: true}}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil); err != nil {
		t.Fatalf("MaintainProd retry: %v", err)
	}
	if !completed(t, ledger, res, "exec_1") {
		t.Fatal("the retried, now-successful production delivery must historize the task")
	}
}

// TestMaintainProd_NotAService is WHAT-1: a repository proven to be no service needs no production,
// so its merged delivery settles at once — not-applicable follows from a PROVEN property, never a
// missing configuration.
func TestMaintainProd_NotAService(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	mergedDelivery(t, ledger, "dlv_1", "o/lib", "exec_1")
	endedExecution(t, res, "exec_1")

	prod := &fakeProd{out: map[string]ProdOutcome{"o/lib": {NotApplicable: true, Evidence: "no cmd/ daemon and no service manifest"}}}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	d, _, _ := ledger.ByID("dlv_1")
	if !d.ProdNotApplicable || !d.Settled() {
		t.Fatalf("a no-service repo must settle as not-applicable, got %+v", d)
	}
	if !completed(t, ledger, res, "exec_1") {
		t.Fatal("a not-applicable production step historizes the task")
	}
	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("a proven not-applicable step raises no disturbance, got %+v", notes)
	}
}

// TestMaintainProd_SkipsUnmerged pins that the production pass touches ONLY merged deliveries — an
// open delivery (PR still waiting) is never shipped to production.
func TestMaintainProd_SkipsUnmerged(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	_ = ledger.Put(runs.Delivery{ID: "dlv_open", Repo: "o/x", Branch: "fix/o-1", CreatedAt: t0, ExecutionID: "exec_1"})
	prod := &fakeProd{out: map[string]ProdOutcome{"o/x": {Running: true}}}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 0 {
		t.Fatalf("an unmerged delivery must not be shipped to production, got calls=%v", prod.calls)
	}
}
