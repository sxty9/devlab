package deliver

import (
	"context"
	"sort"
	"strings"
	"testing"

	"devlab/backend/internal/runs"
)

// fakeLandscape is the fixture LandscapeSource: it answers a fixed roster of full repository names,
// the way the real source derives it from the edge routes plus devlab and the central dashboard.
type fakeLandscape struct{ svc []string }

func (f fakeLandscape) Services(context.Context) ([]string, error) { return f.svc, nil }

// missingNotices returns the repositories named in prod-missing disturbances.
func missingNotices(t *testing.T, n *runs.NoticeStore) []string {
	t.Helper()
	notes, _ := n.List()
	var repos []string
	for _, note := range notes {
		if note.Kind == runs.NoticeProdMissing {
			// notify() prefixes the message with "<repo>: "; recover the repo from that prefix.
			if i := strings.Index(note.Message(), ": "); i > 0 {
				repos = append(repos, note.Message()[:i])
			}
		}
	}
	sort.Strings(repos)
	return repos
}

// TestReconcileProd_MissingLandscapeMembersAreDeliveredNotSilent is test (a), the DECISIVE one:
// production is empty and the landscape knows N services; ALL N — not only the ones ever delivered
// before (here none were) — are recognised as missing, named as a disturbance, and delivered over the
// regular production path. devlab itself and the central dashboard are among the N.
func TestReconcileProd_MissingLandscapeMembersAreDeliveredNotSilent(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	// The landscape roster: routed services + devlab + the dashboard. Production carries NONE of them
	// (no prod-state records) and the ledger never delivered any of them to production.
	roster := []string{"o/aigentic", "o/dashboard", "o/devlab", "o/notify", "o/prizm"}
	tips := map[string]string{}
	out := map[string]ProdOutcome{}
	for _, r := range roster {
		tips[r] = "tip-" + r
		out[r] = ProdOutcome{Running: true, Commit: "tip-" + r}
	}
	prod := &fakeProd{tips: tips, out: out}

	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}

	// Every landscape member missing from production was delivered — the whole roster, not a subset
	// drawn from the delivery history.
	sort.Strings(prod.calls)
	if strings.Join(prod.calls, ",") != strings.Join(roster, ",") {
		t.Fatalf("every missing landscape member must be delivered, got %v want %v", prod.calls, roster)
	}
	// devlab and the dashboard are provably among the delivered members.
	for _, must := range []string{"o/devlab", "o/dashboard"} {
		found := false
		for _, c := range prod.calls {
			if c == must {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s must be a landscape member that is delivered, got calls=%v", must, prod.calls)
		}
	}
	// Each was NAMED as a prod-missing disturbance — not silence.
	if got := missingNotices(t, n); strings.Join(got, ",") != strings.Join(roster, ",") {
		t.Fatalf("every missing member must raise a prod-missing disturbance, got %v want %v", got, roster)
	}
	if !runs.IsDisturbance(runs.NoticeProdMissing) {
		t.Fatal("a prod-missing notice must be a disturbance so it reaches the user")
	}
	// After delivery, production is recorded to carry each member's standard-branch tip, so the next
	// pass reads even.
	for _, r := range roster {
		if rec, ok, _ := ps.Get(r); !ok || rec.Commit != tips[r] {
			t.Fatalf("after delivery, production must be recorded carrying %s at %s, got %+v ok=%v", r, tips[r], rec, ok)
		}
	}
}

// TestReconcileProd_CompleteAndCurrentIsSilent is test (b): production carries every landscape service
// at exactly the standard-branch tip — the landscape is complete AND current — so there is no message
// and no unnecessary send.
func TestReconcileProd_CompleteAndCurrentIsSilent(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	roster := []string{"o/aigentic", "o/dashboard", "o/devlab"}
	tips := map[string]string{}
	for _, r := range roster {
		tips[r] = "same-" + r
		if err := ps.Put(runs.ProdRecord{Repo: r, Commit: "same-" + r, DeployedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	prod := &fakeProd{tips: tips} // no scripted DeployProd outcome: none must be called

	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 0 {
		t.Fatalf("a complete, current production must trigger no send, got calls=%v", prod.calls)
	}
	if notes, _ := n.List(); len(notes) != 0 {
		t.Fatalf("a complete, current production must raise no notice, got %+v", notes)
	}
}

// TestReconcileProd_NewLandscapeMemberIsExpectedAndDelivered is test (c): a service newly enters the
// landscape (the roster grows) — it is expected on production and delivered WITHOUT any hand-edit,
// while the members already complete-and-current stay silent.
func TestReconcileProd_NewLandscapeMemberIsExpectedAndDelivered(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	// Two members are already complete and current.
	for _, r := range []string{"o/aigentic", "o/notify"} {
		if err := ps.Put(runs.ProdRecord{Repo: r, Commit: "cur-" + r, DeployedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	// A third service, o/newcomer, just joined the landscape (a fresh edge route); production has never
	// carried it.
	roster := []string{"o/aigentic", "o/newcomer", "o/notify"}
	prod := &fakeProd{
		tips: map[string]string{"o/aigentic": "cur-o/aigentic", "o/notify": "cur-o/notify", "o/newcomer": "tip-new"},
		out:  map[string]ProdOutcome{"o/newcomer": {Running: true, Commit: "tip-new"}},
	}

	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 1 || prod.calls[0] != "o/newcomer" {
		t.Fatalf("only the new landscape member must be delivered, got calls=%v", prod.calls)
	}
	if got := missingNotices(t, n); len(got) != 1 || got[0] != "o/newcomer" {
		t.Fatalf("only the new member must be named missing, got %v", got)
	}
	if rec, ok, _ := ps.Get("o/newcomer"); !ok || rec.Commit != "tip-new" {
		t.Fatalf("the new member must be recorded carried after delivery, got %+v ok=%v", rec, ok)
	}
}

// TestReconcileProd_NonServiceRepoIsNeverExpected is test (d): a repository that is not a service
// carries no edge route, so it is not part of the landscape roster — it turns up in no list, is never
// delivered and raises nothing. Here o/toollib is such a repo: present in the instance, measurable,
// but absent from the roster.
func TestReconcileProd_NonServiceRepoIsNeverExpected(t *testing.T) {
	ledger, res, n, ps := tempLedger(t), tempResults(t), tempNotices(t), tempProdState(t)

	// The one real service is complete and current; the non-service o/toollib is deliberately NOT in
	// the roster (it carries no route) even though its standard-branch tip is measurable.
	if err := ps.Put(runs.ProdRecord{Repo: "o/aigentic", Commit: "same", DeployedAt: t0}); err != nil {
		t.Fatal(err)
	}
	roster := []string{"o/aigentic"}
	prod := &fakeProd{tips: map[string]string{"o/aigentic": "same", "o/toollib": "libtip"}}

	if err := MaintainProd(context.Background(), prod, fakeLandscape{svc: roster}, ledger, ps, res, n, nil, nil, nil); err != nil {
		t.Fatalf("MaintainProd: %v", err)
	}
	if len(prod.calls) != 0 {
		t.Fatalf("a non-service repo must never be delivered, got calls=%v", prod.calls)
	}
	notes, _ := n.List()
	for _, note := range notes {
		if strings.Contains(note.Message(), "o/toollib") {
			t.Fatalf("a non-service repo must raise nothing, got %+v", note)
		}
	}
	if len(notes) != 0 {
		t.Fatalf("a complete landscape with a bystander non-service repo must be silent, got %+v", notes)
	}
}
