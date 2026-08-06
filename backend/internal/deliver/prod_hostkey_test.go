package deliver

// Part 2: a reinstalled production host presents a NEW ssh host key, so every production send fails the
// host-key check. These tests prove the chain does NOT mask that as a generic failure and NEVER trusts
// the new key silently: the change gets its own distinct reason (NoticeProdHostKeyChanged), a deliberate
// approval is raised on the SAME Blocked/approval path the root-wrapper renewal uses, the key is applied
// only after the operator approves AND it still matches the approved fingerprint (content-pin,
// single-use), and a key that changed again after approval is refused rather than trusted.

import (
	"context"
	"errors"
	"testing"

	"devlab/backend/internal/runs"
)

// fakeHostKey is the fixture HostKeyGate: it reports one fingerprint the target currently presents and
// models the content-pin — Accept succeeds only when the approved fingerprint still matches that key.
type fakeHostKey struct {
	target   string
	fp       string // the fingerprint the target presents now
	scanErr  error
	accepted []string // fingerprints Accept installed
}

func (f *fakeHostKey) Target() string { return f.target }
func (f *fakeHostKey) ScanFingerprint(context.Context) (string, error) {
	return f.fp, f.scanErr
}
func (f *fakeHostKey) Accept(_ context.Context, approved string) error {
	if approved != f.fp {
		return errors.New("the production host now presents a different key than the one approved")
	}
	f.accepted = append(f.accepted, approved)
	return nil
}

func noticeKinds(n *runs.NoticeStore) map[string]int {
	notes, _ := n.List()
	m := map[string]int{}
	for _, x := range notes {
		m[x.Kind]++
	}
	return m
}

func openHostKeyQuestions(t *testing.T, q *runs.QuestionStore) []runs.Question {
	t.Helper()
	all, _ := q.List()
	var out []runs.Question
	for _, x := range all {
		if x.QKind == runs.QuestionProdHostKey && x.Open() {
			out = append(out, x)
		}
	}
	return out
}

// A changed host key surfaces as its OWN reason and a guarded question — never a masked prod-undelivered
// and never a silent trust.
func TestMaintainProd_HostKeyChange_RaisesDistinctReasonAndQuestion(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	q := tempQuestions(t)
	mergedDelivery(t, ledger, "dlv_1", "o/x", "exec_1")
	endedExecution(t, res, "exec_1")

	prod := &fakeProd{
		out: map[string]ProdOutcome{"o/x": {HostKeyChanged: true, HostKeyTarget: "prod.example"}},
		err: map[string]error{"o/x": errors.New("REMOTE HOST IDENTIFICATION HAS CHANGED")},
	}
	gate := &fakeHostKey{target: "prod.example", fp: "SHA256:NEWKEY"}
	if err := MaintainProd(context.Background(), prod, nil, ledger, ps, res, n, nil, q, gate); err == nil {
		t.Fatal("a failed production send must still surface its error")
	}

	kinds := noticeKinds(n)
	if kinds[runs.NoticeProdHostKeyChanged] != 1 {
		t.Fatalf("a changed host key must raise its OWN reason once, got notices %v", kinds)
	}
	if kinds[runs.NoticeProdUndelivered] != 0 {
		t.Fatalf("a changed host key must NOT be masked as a plain undelivered send, got %v", kinds)
	}
	oq := openHostKeyQuestions(t, q)
	if len(oq) != 1 {
		t.Fatalf("a changed host key must raise exactly one guarded approval, got %d", len(oq))
	}
	if oq[0].HostKeyTarget != "prod.example" || oq[0].HostKeyFingerprint != "SHA256:NEWKEY" {
		t.Fatalf("the question must pin the host and the scanned fingerprint, got %+v", oq[0])
	}
	if oq[0].Approved {
		t.Fatal("the question must NOT be pre-approved — nothing is trusted without the user")
	}
	if len(gate.accepted) != 0 {
		t.Fatalf("no key may be accepted before the user approves, accepted=%v", gate.accepted)
	}
	// The delivery is not settled and still owes production (the merged layer is untouched).
	d, _, _ := ledger.ByID("dlv_1")
	if d.Settled() || d.ProdDeployedAt != nil {
		t.Fatal("a host-key failure must not settle or historize the delivery")
	}
}

// The changed key is a property of the HOST: two deliveries to the same host in one pass raise the
// approval ONCE, not once per delivery.
func TestMaintainProd_HostKeyChange_AskedOncePerHost(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	q := tempQuestions(t)
	mergedDelivery(t, ledger, "dlv_1", "o/x", "exec_1")
	mergedDelivery(t, ledger, "dlv_2", "o/y", "exec_2")
	endedExecution(t, res, "exec_1")
	endedExecution(t, res, "exec_2")

	prod := &fakeProd{
		out: map[string]ProdOutcome{
			"o/x": {HostKeyChanged: true, HostKeyTarget: "prod.example"},
			"o/y": {HostKeyChanged: true, HostKeyTarget: "prod.example"},
		},
		err: map[string]error{
			"o/x": errors.New("Host key verification failed"),
			"o/y": errors.New("Host key verification failed"),
		},
	}
	gate := &fakeHostKey{target: "prod.example", fp: "SHA256:NEWKEY"}
	_ = MaintainProd(context.Background(), prod, nil, ledger, ps, res, n, nil, q, gate)

	if oq := openHostKeyQuestions(t, q); len(oq) != 1 {
		t.Fatalf("two deliveries to one changed host must raise ONE approval, got %d", len(oq))
	}
}

// answeredApprovedHostKey pre-records the operator's approval of a host key on the Blocked surface.
func answeredApprovedHostKey(t *testing.T, q *runs.QuestionStore, target, fp string) {
	t.Helper()
	raised, err := q.Raise(runs.Question{QKind: runs.QuestionProdHostKey, Repo: "o/x", HostKeyTarget: target, HostKeyFingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Answer(raised.ID, "approved", true, runs.Question{}.AskedBy); err != nil {
		t.Fatal(err)
	}
}

// Once the operator approves, the next pass ACCEPTS the key (verified against the pinned fingerprint),
// records a positive transition, consumes the approval, and the held delivery goes through.
func TestMaintainProd_HostKeyApproval_AcceptsAndResumes(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	q := tempQuestions(t)
	mergedDelivery(t, ledger, "dlv_1", "o/x", "exec_1")
	endedExecution(t, res, "exec_1")
	answeredApprovedHostKey(t, q, "prod.example", "SHA256:NEWKEY")

	// With the key accepted, the send now proves running.
	prod := &fakeProd{out: map[string]ProdOutcome{"o/x": {Running: true}}}
	gate := &fakeHostKey{target: "prod.example", fp: "SHA256:NEWKEY"}
	if err := MaintainProd(context.Background(), prod, nil, ledger, ps, res, n, nil, q, gate); err != nil {
		t.Fatalf("MaintainProd after approval: %v", err)
	}

	if len(gate.accepted) != 1 || gate.accepted[0] != "SHA256:NEWKEY" {
		t.Fatalf("the approved key must be accepted exactly once, got %v", gate.accepted)
	}
	if noticeKinds(n)[runs.NoticeProdHostKeyAccepted] != 1 {
		t.Fatalf("acceptance must record a positive transition, got %v", noticeKinds(n))
	}
	if len(openHostKeyQuestions(t, q)) != 0 {
		t.Fatal("the approval must be consumed (single-use) after it is applied")
	}
	if d, _, _ := ledger.ByID("dlv_1"); d.ProdDeployedAt == nil || !d.Settled() {
		t.Fatal("with the key accepted the held delivery must go through and settle")
	}
}

// A key that changed AGAIN after the approval is refused, not trusted: the stale approval is consumed
// (single-use) and the failure is named with its own reason — nothing carries over to the new key.
func TestMaintainProd_HostKeyApproval_RefusesAKeyThatChangedAgain(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)
	q := tempQuestions(t)
	mergedDelivery(t, ledger, "dlv_1", "o/x", "exec_1")
	endedExecution(t, res, "exec_1")
	answeredApprovedHostKey(t, q, "prod.example", "SHA256:APPROVED")

	// The host now presents a DIFFERENT key than the one approved.
	prod := &fakeProd{out: map[string]ProdOutcome{"o/x": {HostKeyChanged: true, HostKeyTarget: "prod.example"}},
		err: map[string]error{"o/x": errors.New("REMOTE HOST IDENTIFICATION HAS CHANGED")}}
	gate := &fakeHostKey{target: "prod.example", fp: "SHA256:CHANGED-AGAIN"}
	_ = MaintainProd(context.Background(), prod, nil, ledger, ps, res, n, nil, q, gate)

	if len(gate.accepted) != 0 {
		t.Fatalf("a key that changed again must NOT be accepted, got %v", gate.accepted)
	}
	// The stale approval is consumed so it cannot silently carry forward to a future key.
	for _, x := range func() []runs.Question { all, _ := q.List(); return all }() {
		if x.QKind == runs.QuestionProdHostKey && x.HostKeyFingerprint == "SHA256:APPROVED" && !x.Resolved {
			t.Fatal("the stale approval must be consumed, not left live")
		}
	}
	if noticeKinds(n)[runs.NoticeProdHostKeyChanged] == 0 {
		t.Fatalf("the refusal must be named with its own reason, got %v", noticeKinds(n))
	}
}
