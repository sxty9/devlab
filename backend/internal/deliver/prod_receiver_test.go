package deliver

// Part 3: a devlab (self) delivery whose merged content changes the ROOT RECEIVER SCRIPTS on the
// production host ships a change the chain cannot install (the deploy key is a forced command that
// cannot overwrite its own gatekeeper). These tests prove the chain does NOT settle such a delivery
// live while the host still carries the old scripts, and instead surfaces the outstanding step as a
// deliberate approval on the Blocked surface — the SAME approval path the wrapper renewal and the
// host-key change use — carrying the operator command and the checksums that bind it. And once the
// operator brings the receiver current (the next send proves it running with the scripts in place),
// the delivery settles live and the approval is retired.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"devlab/backend/internal/runs"
)

func openReceiverQuestions(t *testing.T, q *runs.QuestionStore) []runs.Question {
	t.Helper()
	all, _ := q.List()
	var out []runs.Question
	for _, x := range all {
		if x.QKind == runs.QuestionProdReceiver && x.Open() {
			out = append(out, x)
		}
	}
	return out
}

// A merged devlab delivery whose receiver scripts have not reached the production host must NOT settle
// live, and must raise a receiver approval naming the command and the checksums.
func TestMaintainProd_ReceiverStale_HeldNotLiveAndRaisesApproval(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	q := tempQuestions(t)
	mergedDelivery(t, ledger, "dlv_1", "o/devlab", "exec_1")
	endedExecution(t, res, "exec_1")

	grants := []runs.WrapperGrant{
		{Name: "devlab-deploy-recv", SHA: "aaa111", Summary: "host carries oldsha; needs aaa111"},
		{Name: "devlab-setup-lib.sh", SHA: "bbb222", Summary: "host carries oldsha; needs bbb222"},
	}
	prod := &fakeProd{
		out: map[string]ProdOutcome{"o/devlab": {
			ReceiverStale: true, ReceiverTarget: "prod.example", ReceiverGrants: grants, Commit: "std99999",
		}},
		err: map[string]error{"o/devlab": errors.New("the production host still carries an older receiver")},
	}
	if err := MaintainProd(context.Background(), prod, nil, ledger, ps, res, n, nil, q, nil); err == nil {
		t.Fatal("a receiver-stale send is a failure and must surface its error")
	}

	d, ok, _ := ledger.ByID("dlv_1")
	if !ok {
		t.Fatal("the delivery must remain in the ledger")
	}
	// The heart of the fix: NOT live while the host carries the old receiver.
	if d.ProdDeployedAt != nil {
		t.Fatal("a delivery whose receiver scripts never reached the host must NOT be stamped live")
	}
	if d.ProdFailedAt == nil {
		t.Fatal("a receiver-stale send is booked as a failure (an implemented change without its delivery)")
	}
	// The task is NOT historized — its execution stays open until production is genuinely complete.
	if completed(t, ledger, res, "exec_1") {
		t.Fatal("the task must not historize while its receiver-carried change is undelivered")
	}

	// The outstanding step is surfaced as a deliberate approval — the same Blocked/approval path.
	qs := openReceiverQuestions(t, q)
	if len(qs) != 1 {
		t.Fatalf("exactly one receiver approval must be raised, got %d", len(qs))
	}
	got := qs[0]
	if got.ProdReceiverTarget != "prod.example" {
		t.Fatalf("the approval must name the production host, got %q", got.ProdReceiverTarget)
	}
	if len(got.Wrappers) != 2 {
		t.Fatalf("the approval must pin the receiver scripts and their checksums, got %v", got.Wrappers)
	}
	// It carries the exact command a human with sudo runs.
	if got.ProdReceiverCommand == "" || !strings.Contains(got.ProdReceiverCommand, "devlab-install-recv") {
		t.Fatalf("the approval must carry the operator command, got %q", got.ProdReceiverCommand)
	}
	// The consent wording is derived from the question's own subject (the checksums that bind it).
	stmt := got.GuardedApprovalStatement()
	if !strings.Contains(stmt, "aaa111") || !strings.Contains(stmt, "prod.example") {
		t.Fatalf("the approval statement must name the host and the pinned checksums, got %q", stmt)
	}
	// A receiver hold is production-only: it must NOT block a new order on this repository's dev branch.
	if held, _ := q.OpenForRepo("o/devlab", "some-other-run"); held != nil {
		t.Fatal("a receiver approval must not hold the repository's dev branch (production-only)")
	}
}

// A second pass in which the receiver is now current settles the delivery live and RETIRES the approval.
func TestMaintainProd_ReceiverBecameCurrent_SettlesLiveAndRetiresApproval(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	q := tempQuestions(t)
	mergedDelivery(t, ledger, "dlv_1", "o/devlab", "exec_1")
	endedExecution(t, res, "exec_1")

	// An approval from a previous (stale) pass still stands open.
	if _, err := q.Raise(runs.Question{
		QKind: runs.QuestionProdReceiver, Repo: "o/devlab", ProdReceiverTarget: "prod.example",
		Wrappers: []runs.WrapperGrant{{Name: "devlab-deploy-recv", SHA: "aaa111"}},
	}); err != nil {
		t.Fatal(err)
	}

	// This pass: the operator brought the receiver current, so the send proves it running.
	prod := &fakeProd{out: map[string]ProdOutcome{"o/devlab": {Running: true, Commit: "std99999"}}}
	if err := MaintainProd(context.Background(), prod, nil, ledger, ps, res, n, nil, q, nil); err != nil {
		t.Fatalf("a clean send must complete: %v", err)
	}

	d, _, _ := ledger.ByID("dlv_1")
	if d.ProdDeployedAt == nil {
		t.Fatal("with the receiver current the delivery must settle live")
	}
	if len(openReceiverQuestions(t, q)) != 0 {
		t.Fatal("the receiver approval must be retired once the host carries the scripts (drift measured closed)")
	}
}
