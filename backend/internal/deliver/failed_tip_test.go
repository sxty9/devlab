package deliver

import (
	"context"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// TestFailedDeliveryBecomesTheTip is the NACHWEIS of the 01.08.2026 hergang: order A commits work
// but its delivery FAILS (no PR ever opened), then order B starts against the same repo. B must NOT
// be handed a base that omits A's work, and the ledger must state A's tip as failed, not absent.
func TestFailedDeliveryBecomesTheTip(t *testing.T) {
	ledger := tempLedger(t)

	// Order A: implement produced commits on its branch, but the delivery failed before any PR — the
	// exact shape of a stale-root-script or failed-dev-delivery run. The chain leaves the mark.
	if err := RecordFailedDelivery(ledger, FailedDeliveryIn{
		DeliveryID: "dlv_a", ExecutionID: "exec_a", Repo: "o/x",
		Branch: "fix/a-aaa111", FromCommit: "base0", ToCommit: "workA", Reason: "deliver-dev stage failed: stale root script",
	}); err != nil {
		t.Fatalf("record failed delivery: %v", err)
	}

	// It is a RECORDED failure, not "no delivery": the ledger holds it, unsettled, and reads failed.
	d, ok, _ := ledger.ByID("dlv_a")
	if !ok || !d.Failed() {
		t.Fatalf("A must be recorded as a failed (unsettled) delivery, got ok=%v %+v", ok, d)
	}
	if !d.OpenState() {
		t.Errorf("a failed delivery must stay UNSETTLED so its execution/ToDo stays open, got %+v", d)
	}

	// The tip of the repo is A's failed delivery.
	tip, err := FailedTip(ledger, "o/x")
	if err != nil {
		t.Fatalf("FailedTip: %v", err)
	}
	if tip == nil || tip.ID != "dlv_a" {
		t.Fatalf("the failed order must be the tip, got %+v", tip)
	}

	// THE BUG, pinned: base determination over the OPEN deliveries returns the default branch — a
	// base that OMITS A's work on fix/a-aaa111. This is exactly the state B used to branch from. The
	// fix is not to hand B this base but to HOLD B before it branches; the holding side is the
	// implement stage, driven by FailedTip above (see the executor test). Here we prove the two facts
	// that make the hold necessary and correct.
	open, _ := ledger.Open("o/x")
	base := NextPRBase(open, "fix/b-bbb222", "main")
	if base != "main" {
		t.Fatalf("guard assumption changed: expected the naive base to be the default branch, got %q", base)
	}
	if base == "fix/a-aaa111" {
		t.Fatalf("a base equal to A's branch would already carry A — the hergang requires it does NOT")
	}
}

// TestFailedDeliveryResolvedAtTheTip: re-running the failed order ships its PR, which CLEARS the mark
// — the tip becomes sound again and no longer holds a following order.
func TestFailedDeliveryResolvedAtTheTip(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	if err := RecordFailedDelivery(ledger, FailedDeliveryIn{
		DeliveryID: "dlv_a", ExecutionID: "exec_a", Repo: "o/x",
		Branch: "fix/a-aaa111", FromCommit: "base0", ToCommit: "workA", Reason: "deliver-dev stage failed",
	}); err != nil {
		t.Fatal(err)
	}

	// The re-run reaches the pull-request stage and opens the PR for the SAME delivery id.
	if _, _, err := OpenOrAdoptPR(context.Background(), gh, ledger, PRIn{
		Repo: "o/x", Head: "fix/a-aaa111", Base: "main", Title: "A", Body: "", DeliveryID: "dlv_a",
	}); err != nil {
		t.Fatalf("re-delivery PR: %v", err)
	}

	d, _, _ := ledger.ByID("dlv_a")
	if d.Failed() || d.FailedAt != nil {
		t.Fatalf("shipping the PR must clear the failed mark, got %+v", d)
	}
	if d.PRNumber == 0 {
		t.Errorf("the recovered delivery must carry its PR number, got %+v", d)
	}
	if tip, _ := FailedTip(ledger, "o/x"); tip != nil {
		t.Errorf("a resolved tip must be sound, got %+v", tip)
	}
}

// TestRollbackFailedDeliveryDismantlesTheTip: the SECOND way back — the failed layer is rolled back,
// its work counter-booked off the dev branch, and the record is settled (no longer failed). The
// existing rollback path carries it; no PR to close, so none is claimed.
func TestRollbackFailedDeliveryDismantlesTheTip(t *testing.T) {
	gh := newFakeGH()
	ledger := tempLedger(t)
	// A sound layer beneath, then A's failed layer on top.
	mergedAt := t0
	_ = ledger.Put(runs.Delivery{ID: "dlv_base", Repo: "o/x", Branch: "fix/base-000", FromCommit: "c0", ToCommit: "base0", PRNumber: 4, CreatedAt: t0, MergedAt: &mergedAt})
	if err := RecordFailedDelivery(ledger, FailedDeliveryIn{
		DeliveryID: "dlv_a", ExecutionID: "exec_a", Repo: "o/x",
		Branch: "fix/a-aaa111", FromCommit: "base0", ToCommit: "workA", Reason: "deliver-dev failed",
	}); err != nil {
		t.Fatal(err)
	}
	// A failed delivery is unmerged, so the counter-booking reverts its range on dev.
	gh.cb = CounterBookResult{Changed: true, Before: "workA", After: "reverted9", DefaultBranch: "main"}

	out, err := Rollback(context.Background(), gh, ledger, tempRuns(t), "dlv_a", model.Actor{User: "tester"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.ReversalPR != nil {
		t.Errorf("an unmerged failed delivery gets no reversal PR, got %+v", out.ReversalPR)
	}
	if len(gh.closeCalls) != 0 {
		t.Errorf("a failed delivery has no PR to close, got close calls %v", gh.closeCalls)
	}
	if !strings.Contains(strings.ToLower(out.Detail), "failed delivery rolled back") {
		t.Errorf("the outcome must name the failed-delivery rollback, got %q", out.Detail)
	}
	d, _, _ := ledger.ByID("dlv_a")
	if d.Failed() || d.ClosedAt == nil || d.FailedAt != nil {
		t.Fatalf("the rolled-back delivery must be settled (closed) and no longer failed, got %+v", d)
	}
	if !strings.Contains(d.ClosedReason, "rolled back by tester") {
		t.Errorf("the record must carry the rollback reason, got %q", d.ClosedReason)
	}
	// The tip is sound again: the sound layer beneath is merged (settled), so nothing holds a follower.
	if tip, _ := FailedTip(ledger, "o/x"); tip != nil {
		t.Errorf("after the rollback the tip must be sound, got %+v", tip)
	}
	if len(gh.redelivered) != 1 {
		t.Errorf("dev must be re-delivered after the counter-booking, got %v", gh.redelivered)
	}
}

// TestRecordFailedDeliveryIdempotentAndSafe: a re-run that fails again updates the SAME record; a
// delivery that has meanwhile settled (merged) is never re-marked failed.
func TestRecordFailedDeliveryIdempotentAndSafe(t *testing.T) {
	ledger := tempLedger(t)
	in := FailedDeliveryIn{DeliveryID: "dlv_a", ExecutionID: "exec_a", Repo: "o/x", Branch: "fix/a-aaa111", FromCommit: "b0", ToCommit: "w1", Reason: "first failure"}
	if err := RecordFailedDelivery(ledger, in); err != nil {
		t.Fatal(err)
	}
	in.Reason = "second failure"
	if err := RecordFailedDelivery(ledger, in); err != nil {
		t.Fatal(err)
	}
	all, _ := ledger.All()
	n := 0
	for _, d := range all {
		if d.ID == "dlv_a" {
			n++
			if d.FailedReason != "second failure" {
				t.Errorf("the mark must update in place, got reason %q", d.FailedReason)
			}
		}
	}
	if n != 1 {
		t.Fatalf("the mark must be idempotent by id, got %d records", n)
	}

	// A merged delivery is left alone.
	mergedAt := t0.Add(time.Hour)
	_ = ledger.Put(runs.Delivery{ID: "dlv_m", Repo: "o/x", Branch: "fix/m-000", FromCommit: "c0", ToCommit: "c1", CreatedAt: t0, MergedAt: &mergedAt})
	if err := RecordFailedDelivery(ledger, FailedDeliveryIn{DeliveryID: "dlv_m", Repo: "o/x", Reason: "late"}); err != nil {
		t.Fatal(err)
	}
	if d, _, _ := ledger.ByID("dlv_m"); d.Failed() || d.MergedAt == nil {
		t.Fatalf("a settled delivery must not be re-marked failed, got %+v", d)
	}
}
