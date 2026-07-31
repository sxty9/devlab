package api

// The protection check is the one recurring pass whose effect leaves DevLab: it walks every repository
// of the configured organisation and, on a deviation, PATCHes that repository's default branch. Nothing
// in a cutover asks for that, so it is HELD — unarmed it reads and reports, and only
// DEVLAB_RUNS_PROTECTION_ENFORCE turns a finding into a write. These tests drive both states.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/deliver"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// protectionOps counts what the pass does: which repositories it READ and which it WROTE. The
// protection it reports is deliberately deviating (no PR requirement, no required status), so every
// repository is a candidate for restoration.
type protectionOps struct {
	*fakeDeliverOps
	read    []string
	patched []string
}

func (p *protectionOps) GetProtection(_ context.Context, repo string) (deliver.Protection, error) {
	p.read = append(p.read, repo)
	return deliver.Protection{MergeMethods: []string{"merge", "squash"}}, nil
}

func (p *protectionOps) ProtectDefaultBranch(_ context.Context, repo, _ string) error {
	p.patched = append(p.patched, repo)
	return nil
}

// protectionServer is a delivery server with a notice pool (the findings must land somewhere) and the
// instance repo set + ops seams substituted.
func protectionServer(t *testing.T, ops deliver.GitHubOps, repos ...string) *Server {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(t.TempDir(), "notices.json"))
	s := deliveriesServer(t)
	s.runNotices = runs.NewNoticeStore(nil)

	oldOps := deliverOps
	deliverOps = func(*Server, string) deliver.GitHubOps { return ops }
	t.Cleanup(func() { deliverOps = oldOps })

	oldSet := runnerRepoSet
	runnerRepoSet = func(context.Context, string, string) ([]model.Repo, error) {
		out := make([]model.Repo, 0, len(repos))
		for _, r := range repos {
			out = append(out, model.Repo{ID: repoShort(r), Name: repoShort(r), FullName: r})
		}
		return out, nil
	}
	t.Cleanup(func() { runnerRepoSet = oldSet })
	return s
}

// Unarmed (the default): every repository is READ, none is written, and the deviation is recorded in
// words that say so. This is the state a freshly cut-over instance boots in.
func TestVerifyRepoProtectionHeldReportsWithoutWriting(t *testing.T) {
	ops := &protectionOps{fakeDeliverOps: &fakeDeliverOps{}}
	s := protectionServer(t, ops, "org/one", "org/two")

	reports, err := s.VerifyRepoProtection(context.Background())
	if err != nil {
		t.Fatalf("a held pass reports, it does not fail: %v", err)
	}
	if len(ops.read) != 2 {
		t.Errorf("every repository must be read, got %v", ops.read)
	}
	if len(ops.patched) != 0 {
		t.Fatalf("an unarmed pass must change NOTHING on GitHub, it patched %v", ops.patched)
	}
	if len(reports) != 2 {
		t.Fatalf("want one report per repository, got %d", len(reports))
	}
	for _, rep := range reports {
		if rep.OK || rep.Restored {
			t.Errorf("%s: a deviation that was not restored must not read as ok/restored: %+v", rep.Repo, rep)
		}
		if !strings.Contains(rep.Detail, "NOT changed") {
			t.Errorf("%s: the report must say the deviation was not changed: %q", rep.Repo, rep.Detail)
		}
		if !strings.Contains(rep.Detail, "merge methods") {
			t.Errorf("%s: the report must name WHAT deviated: %q", rep.Repo, rep.Detail)
		}
		if strings.Contains(rep.Detail, "restoring failed") {
			t.Errorf("%s: a held write is this daemon's decision, not a failed restoration: %q", rep.Repo, rep.Detail)
		}
	}

	// The finding is nonetheless recorded, so the notice panel and the daily report see it.
	notices, err := s.runNotices.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) == 0 {
		t.Fatal("a held pass must still record its findings — reporting is the whole point of the hold")
	}
	found := false
	for _, n := range notices {
		if n.Kind == "protection-deviation" && strings.Contains(n.Text+n.Reason, "NOT changed") {
			found = true
		}
	}
	if !found {
		t.Errorf("the notice must name the honest outcome: %+v", notices)
	}
}

// Armed: the same deviation is restored. The hold is a decision the operator can take back, not a
// capability that was removed (REQ-033.7).
func TestVerifyRepoProtectionArmedRestores(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_PROTECTION_ENFORCE", "1")
	ops := &protectionOps{fakeDeliverOps: &fakeDeliverOps{}}
	s := protectionServer(t, ops, "org/one", "org/two")

	reports, err := s.VerifyRepoProtection(context.Background())
	if err != nil {
		t.Fatalf("armed enforcement: %v", err)
	}
	if len(ops.patched) != 2 {
		t.Fatalf("an armed pass must restore every deviation, it patched %v", ops.patched)
	}
	for _, rep := range reports {
		if !rep.Restored || !strings.Contains(rep.Detail, "restored") {
			t.Errorf("%s: an armed pass reports the restoration: %+v", rep.Repo, rep)
		}
	}
}

// Only an explicit, affirmative value arms it: a leftover "0" or "off" in a drop-in must not write.
func TestProtectionEnforcementNeedsAnAffirmativeValue(t *testing.T) {
	for _, v := range []string{"", "0", "off", "false", "no", " ", "maybe"} {
		t.Setenv("DEVLAB_RUNS_PROTECTION_ENFORCE", v)
		if protectionEnforcementArmed() {
			t.Errorf("%q must not arm protection writes", v)
		}
	}
	for _, v := range []string{"1", "true", "yes", "ON"} {
		t.Setenv("DEVLAB_RUNS_PROTECTION_ENFORCE", v)
		if !protectionEnforcementArmed() {
			t.Errorf("%q must arm protection writes", v)
		}
	}
}
