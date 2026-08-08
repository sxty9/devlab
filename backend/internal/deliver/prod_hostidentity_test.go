package deliver

// A production-state record is a CLAIM about ONE specific host, identified by its ssh host key. When
// the production machine is rebuilt from blank it presents a NEW host key, so every record about the
// old machine is not stale but VOID — a claim about a host that no longer exists. These tests prove
// the chain closes that at the root: the reconciliation reads the records through the host identity,
// so a replaced host invalidates every record about it in one step, automatically, and the roster is
// then delivered afresh — with NO human editing the production-state file (the exact hand step that,
// twice in one day, was the last thing between a blank VPS and full production).

import (
	"context"
	"sort"
	"strings"
	"testing"

	"devlab/backend/internal/runs"
)

// TestReconcileProd_ReplacedHostVoidsRecordsAndSendsRoster is THE decisive case: production is
// replaced by a different host (its ssh host key changes) and the chain, with no hand step and no
// file edited by a human, sends every service in the roster. The SAME records that read "even" for
// the old host read "void → measure afresh" for the new one, purely from the host identity.
func TestReconcileProd_ReplacedHostVoidsRecordsAndSendsRoster(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	// The landscape, and the state production carried on the ORIGINAL host: every service at exactly its
	// standard-branch tip, each record bound to that host's key (SHA256:OLD). Written the way a
	// successful send records it — no hand-authored file.
	roster := []string{"o/aigentic", "o/dashboard", "o/devlab", "o/notify", "o/prizm"}
	const oldHost, newHost = "SHA256:OLD", "SHA256:NEW"
	tips := map[string]string{}
	out := map[string]ProdOutcome{}
	for _, r := range roster {
		tips[r] = "tip-" + r
		out[r] = ProdOutcome{Running: true, Commit: "tip-" + r}
		if err := ps.Put(runs.ProdRecord{Repo: r, Commit: "tip-" + r, HostKey: oldHost, DeployedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}

	// Pass 1 — the ORIGINAL host is still there (the gate reads its key). Production carries the whole
	// roster at the tip, so the reconciliation is silent: no send, no notice.
	prod := &fakeProd{tips: tips, out: out}
	oldGate := &fakeHostKey{target: "prod.example", fp: oldHost}
	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, tempQuestions(t), oldGate); err != nil {
		t.Fatalf("MaintainProd (original host): %v", err)
	}
	if len(prod.calls) != 0 {
		t.Fatalf("a complete, current production on the original host must trigger no send, got calls=%v", prod.calls)
	}
	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("a complete, current production must raise no notice, got %+v", notes)
	}

	// The host is REPLACED — a blank rebuild presents a new ssh host key. Nothing else changes: the
	// production-state file is NOT touched by anyone; the records still name the old host.
	newGate := &fakeHostKey{target: "prod.example", fp: newHost}

	// Pass 2 — the same records, now describing a machine that no longer exists, are void. Every roster
	// service is measured afresh, found MISSING, and delivered to the host now there.
	prod2 := &fakeProd{tips: tips, out: out}
	if err := MaintainProd(context.Background(), prod2, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, tempQuestions(t), newGate); err != nil {
		t.Fatalf("MaintainProd (replaced host): %v", err)
	}

	// Every service in the roster was sent — the whole landscape, from a single change in host identity,
	// with no hand step.
	sort.Strings(prod2.calls)
	if strings.Join(prod2.calls, ",") != strings.Join(roster, ",") {
		t.Fatalf("a replaced host must re-deliver every roster service, got %v want %v", prod2.calls, roster)
	}
	// Each was named MISSING (a first-ever delivery to the new host), never a spurious DRIFT.
	if got := missingNotices(t, n); strings.Join(got, ",") != strings.Join(roster, ",") {
		t.Fatalf("every roster service must be named missing on the new host, got %v want %v", got, roster)
	}
	if noticeKinds(n)[runs.NoticeProdDrift] != 0 {
		t.Fatalf("a first-ever delivery to the new host must not be announced as a drift, got %v", noticeKinds(n))
	}
	// The pool now records each service bound to the NEW host, so the next pass on that host reads even.
	for _, r := range roster {
		rec, ok, _ := ps.Get(r)
		if !ok || rec.Commit != tips[r] || rec.HostKey != newHost {
			t.Fatalf("after re-delivery the record must be bound to the new host, got %+v ok=%v", rec, ok)
		}
	}
}

// TestReconcileProd_SameHostHonoursRecords is the counterpart: as long as the host identity is
// unchanged, the records stand — a matching host reads "even" and triggers no re-send. This guards
// the loop the void must NOT create: a transient identity read or an unchanged host must never void a
// valid record and re-deliver the landscape for nothing.
func TestReconcileProd_SameHostHonoursRecords(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	roster := []string{"o/aigentic", "o/devlab"}
	tips := map[string]string{}
	for _, r := range roster {
		tips[r] = "tip-" + r
		if err := ps.Put(runs.ProdRecord{Repo: r, Commit: "tip-" + r, HostKey: "SHA256:SAME", DeployedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	prod := &fakeProd{tips: tips} // no scripted send: none must be called
	gate := &fakeHostKey{target: "prod.example", fp: "SHA256:SAME"}
	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, tempQuestions(t), gate); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 0 {
		t.Fatalf("an unchanged host must honour its records — no re-send, got calls=%v", prod.calls)
	}
	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("an unchanged host must raise no notice, got %+v", notes)
	}
}

// TestReconcileProd_UnreadableIdentityHonoursRecords pins the safe degrade: when the host identity
// cannot be read this pass (the gate's scan fails), the reconciliation makes NO identity judgement
// and honours the records as they stand, rather than void a valid record on a transient failure.
func TestReconcileProd_UnreadableIdentityHonoursRecords(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	roster := []string{"o/aigentic"}
	if err := ps.Put(runs.ProdRecord{Repo: "o/aigentic", Commit: "tip", HostKey: "SHA256:BOUND", DeployedAt: t0}); err != nil {
		t.Fatal(err)
	}
	prod := &fakeProd{tips: map[string]string{"o/aigentic": "tip"}}
	gate := &fakeHostKey{target: "prod.example", scanErr: context.DeadlineExceeded} // identity unreadable
	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, tempQuestions(t), gate); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 0 {
		t.Fatalf("an unreadable host identity must not void records and re-deliver, got calls=%v", prod.calls)
	}
	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("an unreadable host identity must raise no notice, got %+v", notes)
	}
}
