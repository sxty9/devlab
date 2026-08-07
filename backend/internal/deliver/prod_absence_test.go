package deliver

import (
	"context"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// openDelivery is one delivery still in flight — committed on its branch, its pull request not yet
// merged. It is "future work": the standard branch already carries mergeable content, and this
// delivery says only that MORE is coming, not what production should carry today.
func openDelivery(t *testing.T, ledger *runs.DeliveryStore, id, repo string) {
	t.Helper()
	if err := ledger.Put(runs.Delivery{
		ID: id, Repo: repo, Branch: "fix/x-" + id, CreatedAt: t0, ExecutionID: "exec_" + id,
	}); err != nil {
		t.Fatal(err)
	}
}

// failedDelivery is one delivery whose dev step failed — its work is on its branch but never merged,
// and the failure concerns THAT unmerged content, not the merged state production must carry.
func failedDelivery(t *testing.T, ledger *runs.DeliveryStore, id, repo string) {
	t.Helper()
	failed := t0.Add(time.Hour)
	if err := ledger.Put(runs.Delivery{
		ID: id, Repo: repo, Branch: "fix/x-" + id, CreatedAt: t0, ExecutionID: "exec_" + id, FailedAt: &failed,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestReconcileProd_MissingIsDeliveredDespiteOpenDelivery is test (a), THE DECISIVE case: a landscape
// service is MISSING from production AND its repository has an open (in-flight) delivery. An absence is
// not repaired by future work — the standard branch is delivered to production NOW, not held back. What
// is delivered is the standard-branch tip, never the open delivery's content.
func TestReconcileProd_MissingIsDeliveredDespiteOpenDelivery(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	// o/contax belongs to the landscape, production has never carried it, and it has an open delivery
	// (future work) — exactly the 2026-08-07 shape that kept six services off a freshly rebuilt host.
	openDelivery(t, ledger, "dlv_open", "o/contax")
	roster := []string{"o/contax"}
	prod := &fakeProd{
		tips: map[string]string{"o/contax": "std111111"},
		out:  map[string]ProdOutcome{"o/contax": {Running: true, Commit: "std111111"}},
	}

	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}

	// The missing service was delivered — an open delivery did NOT hold the absent service off production.
	if len(prod.calls) != 1 || prod.calls[0] != "o/contax" {
		t.Fatalf("a MISSING service must be delivered even with an open delivery, got calls=%v", prod.calls)
	}
	// What was delivered is the standard-branch tip (DeployProd ships origin/<default>), and the pool now
	// records THAT commit — never the open delivery's content.
	if rec, ok, _ := ps.Get("o/contax"); !ok || rec.Commit != "std111111" {
		t.Fatalf("the delivered state must be the standard-branch tip, got %+v ok=%v", rec, ok)
	}
	// It was NAMED as missing, not silently skipped.
	if got := missingNotices(t, n); len(got) != 1 || got[0] != "o/contax" {
		t.Fatalf("the missing service must be named as a prod-missing disturbance, got %v", got)
	}
	// The open delivery is untouched — it is still open, still future work.
	if d, _, _ := ledger.ByID("dlv_open"); !d.OpenState() {
		t.Fatal("the reconciliation must not alter the open delivery")
	}
}

// TestReconcileProd_DriftWaitsWhileDeliveryInFlight is test (b): production runs the service but at an
// OLDER state, and a delivery is in flight. Here waiting is right — the delivery will bring the newer
// content, so a send now would ship a state the delivery is about to supersede.
func TestReconcileProd_DriftWaitsWhileDeliveryInFlight(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	// Production ran o/hostek before at OLD; the standard branch has since moved to NEW, and a delivery
	// is in flight (open) to carry the change.
	prodDeployedDelivery(t, ledger, "dlv_1", "o/hostek", "old000000")
	_ = ps.Put(runs.ProdRecord{Repo: "o/hostek", Commit: "old000000", DeployedAt: t0})
	openDelivery(t, ledger, "dlv_open", "o/hostek")

	roster := []string{"o/hostek"}
	prod := &fakeProd{
		tips: map[string]string{"o/hostek": "new111111"},
		out:  map[string]ProdOutcome{"o/hostek": {Running: true, Commit: "new111111"}},
	}
	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}

	// A drift with a delivery in flight is left to the chain — no send, no drift notice this pass.
	if len(prod.calls) != 0 {
		t.Fatalf("a drift with an in-flight delivery must wait, got calls=%v", prod.calls)
	}
	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("a drift the chain is still driving raises no notice this pass, got %+v", notes)
	}
}

// TestReconcileProd_CurrentWithOpenDeliveryIsSilent is test (c): production already carries the
// standard-branch tip — even with an open delivery, there is no drift, so no message and no send.
func TestReconcileProd_CurrentWithOpenDeliveryIsSilent(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	prodDeployedDelivery(t, ledger, "dlv_1", "o/prizm", "same00000")
	_ = ps.Put(runs.ProdRecord{Repo: "o/prizm", Commit: "same00000", DeployedAt: t0})
	openDelivery(t, ledger, "dlv_open", "o/prizm")

	roster := []string{"o/prizm"}
	prod := &fakeProd{tips: map[string]string{"o/prizm": "same00000"}}
	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 0 {
		t.Fatalf("production already at the standard branch must trigger no send, got calls=%v", prod.calls)
	}
	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("production already at the standard branch must raise no notice, got %+v", notes)
	}
}

// TestReconcileProd_FailedDeliveryDoesNotHoldBackMergedState is test (d): a MISSING service whose
// repository has a FAILED delivery is still delivered — the failed attempt concerns its own unmerged
// content, not the merged standard branch. A failed delivery holds nothing back.
func TestReconcileProd_FailedDeliveryDoesNotHoldBackMergedState(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	failedDelivery(t, ledger, "dlv_failed", "o/icaly")
	roster := []string{"o/icaly"}
	prod := &fakeProd{
		tips: map[string]string{"o/icaly": "std222222"},
		out:  map[string]ProdOutcome{"o/icaly": {Running: true, Commit: "std222222"}},
	}
	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 1 || prod.calls[0] != "o/icaly" {
		t.Fatalf("a failed delivery must not hold the merged standard branch off production, got calls=%v", prod.calls)
	}
	if got := missingNotices(t, n); len(got) != 1 || got[0] != "o/icaly" {
		t.Fatalf("the missing service must be named, got %v", got)
	}
	// The failed delivery is untouched — it is still the failed dev tip, its own matter.
	if d, _, _ := ledger.ByID("dlv_failed"); !d.Failed() {
		t.Fatal("the reconciliation must not alter the failed delivery")
	}
}

// TestReconcileProd_DriftWithOnlyFailedDeliveryIsHealed pins the "moves" nuance of case (b): a drift
// held back only by a FAILED (not moving) delivery is NOT waited on — a stuck delivery is no reason to
// keep production on an older state. Production runs o/remshel at OLD, the standard branch is at NEW,
// and the only other delivery is a failed one.
func TestReconcileProd_DriftWithOnlyFailedDeliveryIsHealed(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	prodDeployedDelivery(t, ledger, "dlv_1", "o/remshel", "old000000")
	_ = ps.Put(runs.ProdRecord{Repo: "o/remshel", Commit: "old000000", DeployedAt: t0})
	failedDelivery(t, ledger, "dlv_failed", "o/remshel")

	roster := []string{"o/remshel"}
	prod := &fakeProd{
		tips: map[string]string{"o/remshel": "new333333"},
		out:  map[string]ProdOutcome{"o/remshel": {Running: true, Commit: "new333333"}},
	}
	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 1 || prod.calls[0] != "o/remshel" {
		t.Fatalf("a drift held back only by a failed (stuck) delivery must be healed, got calls=%v", prod.calls)
	}
	if rec, ok, _ := ps.Get("o/remshel"); !ok || rec.Commit != "new333333" {
		t.Fatalf("the healed drift must record the new carried commit, got %+v ok=%v", rec, ok)
	}
}
