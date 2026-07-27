package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/runs"
)

// seedRunsStore builds a runs store backed by throwaway files, seeded with the given runs.
func seedRunsStore(t *testing.T, in []runs.Run) *runs.Store {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(t.TempDir(), "hist"))
	st := runs.NewStore()
	if _, err := st.Mutate("seed", "t", func([]runs.Run) ([]runs.Run, error) { return in, nil }); err != nil {
		t.Fatal(err)
	}
	return st
}

// writeHusk drops an unfinished result file directly (Save would stamp UpdatedAt to now, defeating the
// staleness a reap test needs), so the test controls exactly how long ago the husk was last worked.
func writeHusk(t *testing.T, resDir, runID, id string, updated time.Time) {
	t.Helper()
	r := runs.Result{RunID: runID, ResultID: id, StartedAt: updated, UpdatedAt: updated, Repos: []runs.RepoResult{}}
	b, _ := json.Marshal(r)
	d := filepath.Join(resDir, runID)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, id+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestMaintainMarksTodoDoneWhenPRMerges pins the "erledigt erst nach dem Merge"-rule end to end: once a
// ToDo's tracked PR is observed merged and no further PRs of the run remain, Maintain checks the ToDo off.
func TestMaintainMarksTodoDoneWhenPRMerges(t *testing.T) {
	runStore := seedRunsStore(t, []runs.Run{{ID: "todo1", Type: runs.TypeTodo, Task: "x"}})
	prStore := tempPRStore(t)
	if err := prStore.Add(runs.PendingPR{Repo: "o/r", Number: 7, RunID: "todo1", MergeBy: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	x := &runExecutor{
		s: &Server{runs: runStore, runPRs: prStore}, mode: "pr", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: true, State: "closed"}, nil
		},
	}
	x.Maintain(context.Background())

	got, _, _ := runStore.Get("todo1")
	if !got.Done {
		t.Error("a ToDo whose PR merged to main must be checked off")
	}
	if prs, _ := prStore.List(); len(prs) != 0 {
		t.Errorf("a merged PR must be untracked, got %d still tracked", len(prs))
	}
}

// TestMaintainKeepsTodoOpenWhenPRClosedUnmerged pins the flip side: a PR CLOSED without a merge is a
// rejection, not a completion — the ToDo stays open (untracked but not done) so it can be restarted.
func TestMaintainKeepsTodoOpenWhenPRClosedUnmerged(t *testing.T) {
	runStore := seedRunsStore(t, []runs.Run{{ID: "todo1", Type: runs.TypeTodo, Task: "x"}})
	prStore := tempPRStore(t)
	if err := prStore.Add(runs.PendingPR{Repo: "o/r", Number: 7, RunID: "todo1", MergeBy: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	x := &runExecutor{
		s: &Server{runs: runStore, runPRs: prStore}, mode: "pr", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: false, State: "closed"}, nil // rejected
		},
	}
	x.Maintain(context.Background())

	got, _, _ := runStore.Get("todo1")
	if got.Done {
		t.Error("a ToDo whose PR was closed WITHOUT merging must stay open (restartable), not be checked off")
	}
}

// TestReapOrphanedHusks pins the anti-corpse sweep: a fired-once ToDo's abandoned husk is finalized so it
// stops lingering forever and the ToDo becomes restartable, while an auto run's stranded husk is left for
// its scheduled resume and a fresh (in-grace) husk is not disturbed.
func TestReapOrphanedHusks(t *testing.T) {
	resDir := filepath.Join(t.TempDir(), "res")
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", resDir)
	results := runs.NewResults()
	runStore := seedRunsStore(t, []runs.Run{
		{ID: "todo_old", Type: runs.TypeTodo, Task: "x"},
		{ID: "todo_fresh", Type: runs.TypeTodo, Task: "x"},
		{ID: "auto_old", Type: runs.TypeAuto},
	})
	now := time.Now().UTC()
	writeHusk(t, resDir, "todo_old", "h", now.Add(-time.Hour))    // abandoned ToDo → reap
	writeHusk(t, resDir, "todo_fresh", "h", now.Add(-time.Minute)) // just worked → keep
	writeHusk(t, resDir, "auto_old", "h", now.Add(-time.Hour))    // auto resumes on schedule → keep

	x := &runExecutor{s: &Server{runs: runStore, runResults: results}, mode: "pr"}
	x.reapOrphanedHusks()

	if r, ok, _ := results.Get("todo_old", "h"); !ok || r.FinishedAt.IsZero() {
		t.Error("an abandoned ToDo husk must be reaped (finished)")
	}
	if r, ok, _ := results.Get("todo_fresh", "h"); !ok || !r.FinishedAt.IsZero() {
		t.Error("a ToDo husk still within the grace must NOT be reaped")
	}
	if r, ok, _ := results.Get("auto_old", "h"); !ok || !r.FinishedAt.IsZero() {
		t.Error("an auto run's husk must be left for its scheduled resume, not reaped")
	}
}

// TestRunWedged pins the stuck-run threshold: a run is wedged only once it has blown past the ceiling by
// more than the grace; a disabled ceiling or a run within budget is never wedged.
func TestRunWedged(t *testing.T) {
	now := time.Now()
	ceiling := 4 * time.Hour
	if runWedged(now.Add(-2*time.Hour), ceiling, now) {
		t.Error("a run well within the ceiling must not be wedged")
	}
	if runWedged(now.Add(-(ceiling + 10*time.Minute)), ceiling, now) {
		t.Error("a run just past the ceiling but inside the grace must not be wedged")
	}
	if !runWedged(now.Add(-(ceiling + wedgedRunGrace + time.Minute)), ceiling, now) {
		t.Error("a run past ceiling + grace must be wedged")
	}
	if runWedged(now.Add(-10*ceiling), 0, now) {
		t.Error("a disabled ceiling (0) must never report wedged")
	}
	if runWedged(time.Time{}, ceiling, now) {
		t.Error("a zero start time must never report wedged")
	}
}

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

// TestShouldDevDeploy pins Findings B + C's gate: a dev-deploy runs ONLY in full mode, and NEVER for
// the self repo (whose restart would kill the running sweep). Covers "dev-deploy only in full mode"
// and "self repo dev-deploy skipped".
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
		{"full", self, false}, // self repo skipped even in full mode (Finding B)
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
