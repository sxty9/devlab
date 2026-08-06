package deliver

import (
	"context"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// prodDeployedDelivery is a delivery that reached production before — the state that makes the
// reconciliation EXPECT production to run this repository, and lets it tell a silent standard-branch
// drift from a repository production never ran.
func prodDeployedDelivery(t *testing.T, ledger *runs.DeliveryStore, id, repo, commit string) {
	t.Helper()
	merged := t0.Add(time.Hour)
	dep := t0.Add(2 * time.Hour)
	if err := ledger.Put(runs.Delivery{
		ID: id, Repo: repo, Branch: "fix/x-" + id, CreatedAt: t0,
		MergedAt: &merged, ToCommit: commit, ProdDeployedAt: &dep,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestReconcileProd_DriftIsNamedAndBroughtCurrent is test (a): the standard branch advances past
// what production carries with NO delivery to account for it — the exact 2026-08-04 gap. The
// reconciliation must NAME it (a disturbance the user sees) and bring production up over the existing
// production send.
func TestReconcileProd_DriftIsNamedAndBroughtCurrent(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	// The repository was delivered to production before at commit OLD, so the pool records OLD.
	prodDeployedDelivery(t, ledger, "dlv_1", "o/x", "old000000")
	if err := ps.Put(runs.ProdRecord{Repo: "o/x", Commit: "old000000", DeployedAt: t0}); err != nil {
		t.Fatal(err)
	}

	// The standard branch has since advanced to NEW (a hand-merged pull request the ledger never saw),
	// and the re-send proves the service running at NEW.
	prod := &fakeProd{
		tips: map[string]string{"o/x": "new111111"},
		out:  map[string]ProdOutcome{"o/x": {Running: true, Commit: "new111111"}},
	}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}

	// The drift was NAMED — a prod-drift disturbance the user sees.
	notes, _ := n.List()
	var drift *runs.Notice
	for i := range notes {
		if notes[i].Kind == runs.NoticeProdDrift {
			drift = &notes[i]
		}
	}
	if drift == nil {
		t.Fatalf("a silent standard-branch drift must be named as a prod-drift notice, got %+v", notes)
	}
	if !runs.IsDisturbance(runs.NoticeProdDrift) {
		t.Fatal("a prod-drift notice must be a disturbance so it reaches the user")
	}

	// Production was brought current over the existing send, and the pool now records the new commit.
	if len(prod.calls) != 1 || prod.calls[0] != "o/x" {
		t.Fatalf("the drift must be healed over the existing production send, got calls=%v", prod.calls)
	}
	rec, ok, _ := ps.Get("o/x")
	if !ok || rec.Commit != "new111111" {
		t.Fatalf("after healing, the pool must record production carries the new commit, got %+v ok=%v", rec, ok)
	}
}

// TestReconcileProd_EvenIsSilent is test (b): production carries exactly the standard-branch tip —
// there is no drift, so there must be NO message and NO unnecessary send.
func TestReconcileProd_EvenIsSilent(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	prodDeployedDelivery(t, ledger, "dlv_1", "o/x", "same00000")
	if err := ps.Put(runs.ProdRecord{Repo: "o/x", Commit: "same00000", DeployedAt: t0}); err != nil {
		t.Fatal(err)
	}

	prod := &fakeProd{tips: map[string]string{"o/x": "same00000"}}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}

	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("production carrying the standard branch must raise no notice, got %+v", notes)
	}
	if len(prod.calls) != 0 {
		t.Fatalf("production carrying the standard branch must trigger no send, got calls=%v", prod.calls)
	}
}

// TestReconcileProd_UnknownCarriedIsHealed is the bootstrap case: production ran this repository
// before but the pool has NO record of which commit it carries (a stale host the mechanism has never
// measured — exactly what existed on 2026-08-04). An unknown state is not assumed current: it is
// named and brought up to the standard branch.
func TestReconcileProd_UnknownCarriedIsHealed(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	prodDeployedDelivery(t, ledger, "dlv_1", "o/x", "old000000") // no ps.Put — the pool is empty

	prod := &fakeProd{
		tips: map[string]string{"o/x": "tip222222"},
		out:  map[string]ProdOutcome{"o/x": {Running: true, Commit: "tip222222"}},
	}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}

	if notes, _ := n.List(); len(notes) != 1 || notes[0].Kind != runs.NoticeProdDrift {
		t.Fatalf("an unknown production state must be named as a prod-drift notice, got %+v", notes)
	}
	if rec, ok, _ := ps.Get("o/x"); !ok || rec.Commit != "tip222222" {
		t.Fatalf("the bootstrap heal must record the carried commit, got %+v ok=%v", rec, ok)
	}
}

// TestReconcileProd_SkipsRepoWithPendingWork pins that the reconciliation leaves a repository the
// chain is still driving alone: a merged delivery that still OWES production is handled by the booked
// loop, so it is not a silent drift and gets no second send from the reconciliation.
func TestReconcileProd_SkipsRepoWithPendingWork(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	// One prod-deployed delivery (production runs the repo) AND one still owing production.
	prodDeployedDelivery(t, ledger, "dlv_1", "o/x", "old000000")
	_ = ps.Put(runs.ProdRecord{Repo: "o/x", Commit: "old000000", DeployedAt: t0})
	merged := t0.Add(3 * time.Hour)
	_ = ledger.Put(runs.Delivery{ID: "dlv_2", Repo: "o/x", Branch: "fix/x-2", CreatedAt: t0.Add(2 * time.Hour), MergedAt: &merged})

	// The owing delivery ships; the reconciliation must NOT also fire for o/x this pass.
	prod := &fakeProd{
		tips: map[string]string{"o/x": "new111111"},
		out:  map[string]ProdOutcome{"o/x": {Running: true, Commit: "new111111"}},
	}
	if err := MaintainProd(context.Background(), prod, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 1 {
		t.Fatalf("the booked loop ships once; the reconciliation must not add a second send, got calls=%v", prod.calls)
	}
	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("a repository the chain is still driving is no silent drift — no notice, got %+v", notes)
	}
}
