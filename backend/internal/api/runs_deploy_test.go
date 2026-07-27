package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/runs"
)

// tempPRStore builds a PRStore backed by a throwaway file so a test can seed/inspect tracked PRs.
func tempPRStore(t *testing.T) *runs.PRStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_PRS", filepath.Join(t.TempDir(), "prs.json"))
	return runs.NewPRStore()
}

// ─── Pure decision helpers ─────────────────────────────────────────────────

// TestShouldCheckPR pins Finding A's read-budget discipline: report/pr never touch an in-window PR
// (zero extra GitHub calls), full mode rechecks in-window PRs but only once per interval, and an
// overdue PR is always checked in every mode.
func TestShouldCheckPR(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour) // still inside the auto-merge window
	past := now.Add(-time.Hour)  // overdue
	recheck := 5 * time.Minute

	cases := []struct {
		name        string
		mode        string
		mergeBy     time.Time
		lastChecked time.Time
		want        bool
	}{
		{"pr in-window never checked", "pr", future, time.Time{}, false},
		{"pr overdue is checked", "pr", past, time.Time{}, true},
		{"report in-window never checked", "report", future, time.Time{}, false},
		{"report overdue is checked", "report", past, time.Time{}, true},
		{"full in-window fresh (never checked) → check", "full", future, time.Time{}, true},
		{"full in-window checked just now → skip", "full", future, now.Add(-time.Minute), false},
		{"full in-window recheck elapsed → check", "full", future, now.Add(-6 * time.Minute), true},
		{"full overdue → check", "full", past, now.Add(-time.Second), true},
	}
	for _, c := range cases {
		if got := shouldCheckPR(c.mode, now, c.mergeBy, c.lastChecked, recheck); got != c.want {
			t.Errorf("%s: shouldCheckPR=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestDecidePR pins the per-PR action: report/pr NEVER deploy (a merged/closed PR is only untracked),
// while full mode turns a merged PR into a prod-deploy.
func TestDecidePR(t *testing.T) {
	merged := github.PullRequest{Merged: true, State: "closed"}
	open := github.PullRequest{State: "open"}
	closed := github.PullRequest{State: "closed"}

	cases := []struct {
		name    string
		mode    string
		pr      github.PullRequest
		overdue bool
		want    prAction
	}{
		{"full merged → deploy", "full", merged, true, prDeploy},
		{"pr merged → untrack (no deploy)", "pr", merged, true, prUntrack},
		{"report merged → untrack (no deploy)", "report", merged, true, prUntrack},
		{"full overdue open → merge", "full", open, true, prMerge},
		{"pr overdue open → merge", "pr", open, true, prMerge},
		{"full in-window open → none", "full", open, false, prNone},
		{"full closed unmerged → untrack", "full", closed, true, prUntrack},
		{"pr closed unmerged → untrack", "pr", closed, true, prUntrack},
	}
	for _, c := range cases {
		if got := decidePR(c.mode, c.pr, c.overdue); got != c.want {
			t.Errorf("%s: decidePR=%d, want %d", c.name, got, c.want)
		}
	}
}

// TestShouldDevDeploy pins the gate: a dev-deploy runs ONLY in full mode — for EVERY repo, the self
// repo included. The old self-repo exclusion (a restart would kill the running sweep) is gone: the
// install is harmless and the restart is deferred until the run slot frees (devlab-restart-idle), so
// excluding the repo only meant DevLab never went live from its own runs.
func TestShouldDevDeploy(t *testing.T) {
	self := selfRepoID() // default "devlab"
	cases := []struct {
		mode   string
		repoID string
		want   bool
	}{
		{"report", "aigentic", false},
		{"pr", "aigentic", false},
		{"full", "aigentic", true},
		{"full", self, true}, // the self repo deploys too — its restart is deferred, not skipped
		{"report", self, false},
		{"pr", self, false},
	}
	for _, c := range cases {
		x := &runExecutor{mode: c.mode}
		if got := x.shouldDevDeploy(c.repoID); got != c.want {
			t.Errorf("mode=%s repo=%s: shouldDevDeploy=%v, want %v", c.mode, c.repoID, got, c.want)
		}
	}

	// DEVLAB_RUNS_DEV_DEPLOY=off disables the in-run dev-deploy even in full mode (arms prod-deploy-on-
	// merge alone, safe before the cutover). Non-off keeps it on.
	x := &runExecutor{mode: "full"}
	for _, v := range []string{"off", "0", "false", "no", "OFF"} {
		t.Setenv("DEVLAB_RUNS_DEV_DEPLOY", v)
		if x.shouldDevDeploy("aigentic") {
			t.Errorf("DEVLAB_RUNS_DEV_DEPLOY=%q: dev-deploy must be OFF in full mode", v)
		}
	}
	for _, v := range []string{"", "on", "1", "true"} {
		t.Setenv("DEVLAB_RUNS_DEV_DEPLOY", v)
		if !x.shouldDevDeploy("aigentic") {
			t.Errorf("DEVLAB_RUNS_DEV_DEPLOY=%q: dev-deploy must be ON in full mode", v)
		}
	}
}

// ─── Maintain orchestration (behavioral, via seams) ─────────────────────────

// TestMaintainPRModeNoDeployNoInWindowGet is Finding A for pr mode: an in-window PR is neither GETed
// nor deployed (it stays tracked untouched), while an OVERDUE open PR is auto-merged and untracked —
// still with ZERO prod-deploys.
func TestMaintainPRModeNoDeployNoInWindowGet(t *testing.T) {
	store := tempPRStore(t)
	now := time.Now()
	// One in-window PR (must NOT be fetched) and one overdue open PR (auto-merged, no deploy).
	if err := store.Add(runs.PendingPR{Repo: "o/inwindow", Number: 1, MergeBy: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(runs.PendingPR{Repo: "o/overdue", Number: 2, MergeBy: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}

	var gets, merges, deploys int
	x := &runExecutor{
		s:         &Server{runPRs: store},
		mode:      "pr",
		tokenUser: "owner",
		tokenFn:   func(string) (string, error) { return "tok", nil },
		getPRFn: func(_ context.Context, _, repo string, n int) (github.PullRequest, error) {
			gets++
			if repo == "o/inwindow" {
				t.Errorf("pr mode fetched an IN-WINDOW PR %s#%d — Finding A violated", repo, n)
			}
			return github.PullRequest{State: "open"}, nil // overdue + open → merge
		},
		mergePRFn:    func(context.Context, string, string, int) error { merges++; return nil },
		prodDeployFn: func(context.Context, string, runs.PendingPR) (string, error) { deploys++; return "", nil },
	}

	x.Maintain(context.Background())

	if gets != 1 {
		t.Errorf("expected exactly 1 GET (only the overdue PR), got %d", gets)
	}
	if merges != 1 {
		t.Errorf("expected the overdue PR auto-merged once, got %d merges", merges)
	}
	if deploys != 0 {
		t.Errorf("pr mode must issue NO prod-deploy, got %d", deploys)
	}
	remaining, _ := store.List()
	if len(remaining) != 1 || remaining[0].Repo != "o/inwindow" {
		t.Errorf("expected only the in-window PR still tracked, got %+v", remaining)
	}
}

// TestMaintainFullModeMergedProdDeploysOnceThenUntracks is Finding A for full mode: a merged tracked PR
// triggers exactly one prod-deploy and is then untracked; a FAILING prod-deploy keeps it tracked so the
// deploy (never a re-merge) retries next tick.
func TestMaintainFullModeMergedProdDeploysOnceThenUntracks(t *testing.T) {
	now := time.Now()

	// Success path: merged PR → one prod-deploy → untracked.
	t.Run("success untracks", func(t *testing.T) {
		store := tempPRStore(t)
		if err := store.Add(runs.PendingPR{Repo: "o/merged", Number: 7, MergeBy: now.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		var merges, deploys int
		x := &runExecutor{
			s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
			tokenFn: func(string) (string, error) { return "tok", nil },
			getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
				return github.PullRequest{Merged: true, State: "closed"}, nil
			},
			mergePRFn:      func(context.Context, string, string, int) error { merges++; return nil },
			prodDeployFn:   func(context.Context, string, runs.PendingPR) (string, error) { deploys++; return "ok", nil },
			deployTargetFn: func(string) bool { return true },
		}

		x.Maintain(context.Background())

		if deploys != 1 {
			t.Errorf("expected exactly 1 prod-deploy for the merged PR, got %d", deploys)
		}
		if merges != 0 {
			t.Errorf("a merged PR must NOT be re-merged, got %d merges", merges)
		}
		if r, _ := store.List(); len(r) != 0 {
			t.Errorf("merged+deployed PR must be untracked, still tracked: %+v", r)
		}
	})

	// Failure path: prod-deploy errors → PR stays tracked for a deploy retry (no re-merge).
	t.Run("failure keeps tracked", func(t *testing.T) {
		store := tempPRStore(t)
		if err := store.Add(runs.PendingPR{Repo: "o/merged", Number: 8, MergeBy: now.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		var merges, deploys int
		x := &runExecutor{
			s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
			tokenFn: func(string) (string, error) { return "tok", nil },
			getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
				return github.PullRequest{Merged: true, State: "closed"}, nil
			},
			mergePRFn: func(context.Context, string, string, int) error { merges++; return nil },
			prodDeployFn: func(context.Context, string, runs.PendingPR) (string, error) {
				deploys++
				return "", errors.New("ship failed")
			},
			deployTargetFn: func(string) bool { return true },
		}

		x.Maintain(context.Background())

		if deploys != 1 {
			t.Errorf("expected 1 prod-deploy attempt, got %d", deploys)
		}
		if merges != 0 {
			t.Errorf("must never re-merge a merged PR, got %d merges", merges)
		}
		if r, _ := store.List(); len(r) != 1 {
			t.Errorf("a failed prod-deploy must keep the PR tracked for retry, got %+v", r)
		}
	})
}

// TestMaintainFullModeMergedWithoutDeployTargetUntracks pins the retry-storm fix: a merged PR for a repo
// with NO vetted deploy script (a library, template or data repo) has nothing to ship, so it is untracked
// WITHOUT a deploy attempt. Before this, every such PR was re-fetched and re-deployed every recheck
// interval forever — each attempt resetting the workspace and failing with wrapper exit 3.
func TestMaintainFullModeMergedWithoutDeployTargetUntracks(t *testing.T) {
	store := tempPRStore(t)
	if err := store.Add(runs.PendingPR{Repo: "o/prizm", Number: 4, MergeBy: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var deploys int
	x := &runExecutor{
		s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: true, State: "closed"}, nil
		},
		prodDeployFn:   func(context.Context, string, runs.PendingPR) (string, error) { deploys++; return "", nil },
		deployTargetFn: func(string) bool { return false },
	}

	x.Maintain(context.Background())

	if deploys != 0 {
		t.Errorf("a repo without a deploy target must NOT be deployed, got %d attempts", deploys)
	}
	if r, _ := store.List(); len(r) != 0 {
		t.Errorf("merged PR without a deploy target must be untracked, still tracked: %+v", r)
	}
}

// TestMaintainFullModeThrottlesInWindowRecheck confirms the per-PR throttle: an in-window PR fetched
// once records LastChecked, so an immediate second Maintain pass does not fetch it again.
func TestMaintainFullModeThrottlesInWindowRecheck(t *testing.T) {
	store := tempPRStore(t)
	now := time.Now()
	if err := store.Add(runs.PendingPR{Repo: "o/open", Number: 3, MergeBy: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var gets int
	x := &runExecutor{
		s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			gets++
			return github.PullRequest{State: "open"}, nil // in-window + open → prNone
		},
		mergePRFn:      func(context.Context, string, string, int) error { return nil },
		prodDeployFn:   func(context.Context, string, runs.PendingPR) (string, error) { return "", nil },
		deployTargetFn: func(string) bool { return true },
	}

	x.Maintain(context.Background()) // first pass fetches once and stamps LastChecked
	x.Maintain(context.Background()) // second pass is throttled → no fetch

	if gets != 1 {
		t.Errorf("expected the in-window PR fetched once (throttled thereafter), got %d fetches", gets)
	}
	if r, _ := store.List(); len(r) != 1 {
		t.Errorf("an in-window open PR must stay tracked, got %+v", r)
	}
}

// TestMaintainMergesInCreationOrder pins the queue discipline: every run branches off the default
// branch, so a younger PR that lands first turns the older one — touching the same files — into a
// conflict that never resolves itself. Two overdue PRs of the SAME repo must therefore merge oldest
// first, and a younger one must NOT overtake while the older is still open (here: its merge fails).
// A PR of a DIFFERENT repo is unaffected — the queue is per repo, not global.
func TestMaintainMergesInCreationOrder(t *testing.T) {
	store := tempPRStore(t)
	now := time.Now()
	older := now.Add(-3 * time.Hour)
	// Deliberately added youngest-first, so passing this test cannot rely on insertion order.
	for _, p := range []runs.PendingPR{
		{Repo: "o/app", Number: 2, CreatedAt: older.Add(time.Hour), MergeBy: now.Add(-time.Minute)},
		{Repo: "o/app", Number: 1, CreatedAt: older, MergeBy: now.Add(-time.Minute)},
		{Repo: "o/other", Number: 9, CreatedAt: older, MergeBy: now.Add(-time.Minute)},
	} {
		if err := store.Add(p); err != nil {
			t.Fatal(err)
		}
	}

	var merged []int
	x := &runExecutor{
		s: &Server{runPRs: store}, mode: "pr", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(_ context.Context, _, _ string, _ int) (github.PullRequest, error) {
			return github.PullRequest{State: "open"}, nil
		},
		mergePRFn: func(_ context.Context, _, repo string, n int) error {
			merged = append(merged, n)
			if repo == "o/app" && n == 1 {
				return errors.New("merge conflict") // the oldest stays open → it blocks its repo's queue
			}
			return nil
		},
	}

	x.Maintain(context.Background())

	// The oldest of o/app was attempted; #2 must NOT have been merged behind its stuck predecessor.
	for _, n := range merged {
		if n == 2 {
			t.Errorf("PR #2 overtook the still-open older PR #1 of the same repo — merge order broken (merged=%v)", merged)
		}
	}
	if len(merged) == 0 || merged[0] != 1 {
		t.Errorf("expected the OLDEST PR (#1) attempted first, got %v", merged)
	}
	var sawOther bool
	for _, n := range merged {
		if n == 9 {
			sawOther = true
		}
	}
	if !sawOther {
		t.Errorf("a PR of a different repo must not be blocked by o/app's queue (merged=%v)", merged)
	}
}
