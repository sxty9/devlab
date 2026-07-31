package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// fakeSources is the fixture behind Derive/SyncStartupTodos — prepared repo truth, no store.
type fakeSources struct {
	ahead      bool
	head       string
	wbErr      error
	deliveries map[string][]runs.Delivery // key runID+"/"+repo
	ledgerErr  error
	openPR     *model.PRRef
	prErr      error
	contained  map[string]bool // commit → contained
	containErr error
	implAt     map[string]bool // key runID+"/"+repo — this run already ran implement there
	implErr    error
}

func (f *fakeSources) WorkbenchState(ctx context.Context, repo string) (bool, string, error) {
	return f.ahead, f.head, f.wbErr
}
func (f *fakeSources) RunDeliveries(runID, repo string) ([]runs.Delivery, error) {
	if f.ledgerErr != nil {
		return nil, f.ledgerErr
	}
	return f.deliveries[runID+"/"+repo], nil
}
func (f *fakeSources) PriorImplementAt(runID, repo string) (bool, error) {
	if f.implErr != nil {
		return false, f.implErr
	}
	return f.implAt[runID+"/"+repo], nil
}
func (f *fakeSources) OpenPRByHead(ctx context.Context, repo, head string) (*model.PRRef, error) {
	return f.openPR, f.prErr
}
func (f *fakeSources) ContainedInDefault(ctx context.Context, repo, commit string) (bool, error) {
	if f.containErr != nil {
		return false, f.containErr
	}
	return f.contained[commit], nil
}

func mkRun(id string, targets ...string) runs.Run {
	r := runs.Run{ID: id, Kind: model.KindTodo, Title: "t-" + id}
	for _, t := range targets {
		r.Targets = append(r.Targets, runs.Target{Repo: t})
	}
	return r
}

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

// K-3: the three prepared repo states yield the three task states — each WITH evidence, never
// from a stored flag.
func TestDeriveThreeStates(t *testing.T) {
	ctx := context.Background()
	run := mkRun("run_x", "org/app")

	t.Run("not-implemented", func(t *testing.T) {
		src := &fakeSources{ahead: false, head: "aaaa1111"}
		f, err := Derive(ctx, src, "org/app", run)
		if err != nil {
			t.Fatal(err)
		}
		if f.State != model.TaskNotImplemented {
			t.Fatalf("state = %s", f.State)
		}
		if len(f.Evidence) == 0 {
			t.Fatalf("no evidence")
		}
	})

	t.Run("implemented-undelivered via open delivery", func(t *testing.T) {
		src := &fakeSources{
			ahead: true, head: "bbbb2222",
			deliveries: map[string][]runs.Delivery{"run_x/org/app": {{
				ID: "dlv_1", Repo: "org/app", Branch: "fix/x-abc", FromCommit: "aaaa", ToCommit: "bbbb",
			}}},
			openPR: &model.PRRef{Number: 7, URL: "https://example.invalid/pr/7", HeadBranch: "fix/x-abc"},
		}
		f, err := Derive(ctx, src, "org/app", run)
		if err != nil {
			t.Fatal(err)
		}
		if f.State != model.TaskImplementedUndelivered {
			t.Fatalf("state = %s", f.State)
		}
		if f.OpenDelivery == nil || f.OpenDelivery.ID != "dlv_1" {
			t.Fatalf("open delivery not surfaced: %+v", f.OpenDelivery)
		}
		if f.OpenPR == nil || f.OpenPR.Number != 7 {
			t.Fatalf("open PR not surfaced: %+v", f.OpenPR)
		}
	})

	t.Run("implemented-undelivered via workbench ahead after this run's own implement", func(t *testing.T) {
		// The crash between the agent's commit and the ledger record: this run ran implement
		// here, the workbench carries the commits, the ledger knows nothing. Only THEN is the
		// rest path the truth.
		src := &fakeSources{ahead: true, head: "cccc3333", implAt: map[string]bool{"run_x/org/app": true}}
		f, err := Derive(ctx, src, "org/app", run)
		if err != nil {
			t.Fatal(err)
		}
		if f.State != model.TaskImplementedUndelivered {
			t.Fatalf("state = %s", f.State)
		}
	})

	t.Run("delivered", func(t *testing.T) {
		src := &fakeSources{
			ahead: false, head: "dddd4444",
			deliveries: map[string][]runs.Delivery{"run_x/org/app": {{
				ID: "dlv_2", Repo: "org/app", ToCommit: "dddd", MergedAt: ts("2026-07-27T10:00:00Z"),
			}}},
		}
		f, err := Derive(ctx, src, "org/app", run)
		if err != nil {
			t.Fatal(err)
		}
		if f.State != model.TaskDelivered {
			t.Fatalf("state = %s", f.State)
		}
		if len(f.Evidence) == 0 {
			t.Fatalf("no evidence")
		}
	})
}

// K-3: the state is observed fresh each time — there is nothing stored that a manipulation
// could flip. Deriving twice over the same truth yields the same state; changing the TRUTH
// (not a flag) changes the state.
func TestDeriveStateless(t *testing.T) {
	ctx := context.Background()
	run := mkRun("run_y", "org/app")
	src := &fakeSources{ahead: false, head: "aaaa"}
	f1, _ := Derive(ctx, src, "org/app", run)
	f2, _ := Derive(ctx, src, "org/app", run)
	if f1.State != f2.State || f1.State != model.TaskNotImplemented {
		t.Fatalf("derivation not stable: %s vs %s", f1.State, f2.State)
	}
	// The repo truth changes — and so does the run's own history, because a shared workbench
	// running ahead is only this task's work once this run has implemented here.
	src.ahead = true
	src.implAt = map[string]bool{"run_y/org/app": true}
	f3, _ := Derive(ctx, src, "org/app", run)
	if f3.State != model.TaskImplementedUndelivered {
		t.Fatalf("… and the derived state must follow: %s", f3.State)
	}
}

// THE fault this rule exists against (measured 2026-07-31): mercury-dev is shared, so a workbench
// running ahead with ANOTHER run's undelivered work must never make a fresh task look implemented.
// It did, on all 23 repositories at once — implement then created nothing, every stage reported
// executed, and the requested work never came into existence.
func TestDeriveForeignWorkOnSharedWorkbenchIsNotThisTask(t *testing.T) {
	ctx := context.Background()
	run := mkRun("run_fresh", "org/app")

	// Another run's commit sits on the workbench; this run has never run here.
	src := &fakeSources{ahead: true, head: "eeee5555"}
	f, err := Derive(ctx, src, "org/app", run)
	if err != nil {
		t.Fatal(err)
	}
	if f.State != model.TaskNotImplemented {
		t.Fatalf("foreign undelivered work was read as this task's own: state = %s", f.State)
	}
	if len(f.Evidence) == 0 || !strings.Contains(f.Evidence[0], "another run") {
		t.Fatalf("the evidence must name whose work it is: %q", f.Evidence)
	}
}

// A delivery of this run that merged stays delivered even while the shared workbench runs ahead —
// the foreign work is named, not counted as a reason to re-derive the state.
func TestDeriveMergedWinsOverForeignAhead(t *testing.T) {
	run := mkRun("run_m", "org/app")
	src := &fakeSources{
		ahead: true, head: "ffff6666",
		deliveries: map[string][]runs.Delivery{"run_m/org/app": {{
			ID: "dlv_9", Repo: "org/app", ToCommit: "ffff", MergedAt: ts("2026-07-30T09:00:00Z"),
		}}},
	}
	f, err := Derive(context.Background(), src, "org/app", run)
	if err != nil {
		t.Fatal(err)
	}
	if f.State != model.TaskDelivered {
		t.Fatalf("state = %s", f.State)
	}
}

// The execution history is a source like any other: unreachable ⇒ unknown, named, never guessed.
func TestDeriveUnknownOnUnreachableHistory(t *testing.T) {
	src := &fakeSources{ahead: true, head: "aaaa", implErr: errors.New("archive unreadable")}
	f, err := Derive(context.Background(), src, "org/app", mkRun("run_h", "org/app"))
	if err == nil || f.State != model.TaskUnknown || f.Err == "" {
		t.Fatalf("history unreachable not honest: state=%s err=%q derr=%v", f.State, f.Err, err)
	}
}

// Unreachable sources yield unknown — named, never guessed.
func TestDeriveUnknownOnUnreachable(t *testing.T) {
	ctx := context.Background()
	run := mkRun("run_z", "org/app")

	src := &fakeSources{wbErr: errors.New("git unreachable")}
	f, err := Derive(ctx, src, "org/app", run)
	if err == nil || f.State != model.TaskUnknown || f.Err == "" {
		t.Fatalf("workbench unreachable not honest: state=%s err=%q derr=%v", f.State, f.Err, err)
	}

	src = &fakeSources{ledgerErr: errors.New("ledger corrupt")}
	f, err = Derive(ctx, src, "org/app", run)
	if err == nil || f.State != model.TaskUnknown {
		t.Fatalf("ledger unreachable not honest: state=%s derr=%v", f.State, err)
	}
}

// The PR probe is auxiliary: its failure never flips the ledger-attested state.
func TestDerivePRProbeFailureKeepsState(t *testing.T) {
	src := &fakeSources{
		deliveries: map[string][]runs.Delivery{"run_x/org/app": {{ID: "dlv_1", Branch: "fix/a"}}},
		prErr:      errors.New("github down"),
	}
	f, err := Derive(context.Background(), src, "org/app", mkRun("run_x", "org/app"))
	if err != nil {
		t.Fatal(err)
	}
	if f.State != model.TaskImplementedUndelivered {
		t.Fatalf("state flipped on auxiliary probe failure: %s", f.State)
	}
}

// ── SyncStartupTodos (REQ-039.2, B-5) ────────────────────────────────────────────────────

type pubRec struct{ topics []live.Topic }

func (p *pubRec) Publish(t live.Topic) { p.topics = append(p.topics, t) }

func stores(t *testing.T) (*runs.Store, *runs.ResultStore, *runs.NoticeStore) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", dir+"/runs.json")
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", dir+"/executions")
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", "")
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", dir+"/notices.json")
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", dir+"/history")
	return runs.NewStore(nil), runs.NewResultStore(nil), runs.NewNoticeStore(nil)
}

func TestSyncStartupTodosChecksOff(t *testing.T) {
	rs, res, n := stores(t)
	run := mkRun("run_a", "org/app")
	if err := rs.Put(run); err != nil {
		t.Fatal(err)
	}
	src := &fakeSources{
		deliveries: map[string][]runs.Delivery{"run_a/org/app": {{
			ID: "dlv_1", Repo: "org/app", ToCommit: "feedcafe", MergedAt: ts("2026-07-27T09:00:00Z"),
		}}},
		contained: map[string]bool{"feedcafe": true},
	}
	pub := &pubRec{}
	got, err := SyncStartupTodos(context.Background(), src, rs, res, n, pub)
	if err != nil || got != 1 {
		t.Fatalf("reconciled = %d, err = %v", got, err)
	}

	list, err := res.ForRun("run_a")
	if err != nil || len(list) != 1 {
		t.Fatalf("synthetic result missing: %d, %v", len(list), err)
	}
	r := list[0]
	if !r.Synthetic {
		t.Fatalf("result not marked synthetic")
	}
	if r.MergedAt == nil {
		t.Fatalf("MergedAt not set")
	}
	if !r.Requested.Created.Autonomous {
		t.Fatalf("authorship not autonomous: %+v", r.Requested)
	}
	if len(r.Repos) != 1 || len(r.Repos[0].Stages) != 1 {
		t.Fatalf("want exactly one preflight stage, got %+v", r.Repos)
	}
	sv := r.Repos[0].Stages[0]
	if sv.Stage != model.StagePreflight || sv.State != model.StepExecuted {
		t.Fatalf("stage not an executed preflight: %+v", sv)
	}
	if want := "arrived in default branch @feedcafe"; sv.Log != want {
		t.Fatalf("stage log %q, want %q", sv.Log, want)
	}
	if !r.Repos[0].Succeeded {
		t.Fatalf("synthetic pipeline not succeeded")
	}

	notes, _ := n.List()
	if len(notes) != 1 || notes[0].RunID != "run_a" {
		t.Fatalf("notice missing: %+v", notes)
	}
	if len(pub.topics) == 0 {
		t.Fatalf("no live publish")
	}

	// Idempotent: the second call reconciles nothing and adds nothing.
	got, err = SyncStartupTodos(context.Background(), src, rs, res, n, pub)
	if err != nil || got != 0 {
		t.Fatalf("second run reconciled %d, err %v", got, err)
	}
	list, _ = res.ForRun("run_a")
	if len(list) != 1 {
		t.Fatalf("idempotency broken: %d results", len(list))
	}
}

func TestSyncStartupTodosSkipsUnarrived(t *testing.T) {
	rs, res, n := stores(t)
	if err := rs.Put(mkRun("run_b", "org/app")); err != nil {
		t.Fatal(err)
	}
	// Open (unmerged) delivery only — nothing arrived.
	src := &fakeSources{deliveries: map[string][]runs.Delivery{"run_b/org/app": {{ID: "dlv_1"}}}}
	got, err := SyncStartupTodos(context.Background(), src, rs, res, n, nil)
	if err != nil || got != 0 {
		t.Fatalf("reconciled = %d, err = %v", got, err)
	}
	if list, _ := res.ForRun("run_b"); len(list) != 0 {
		t.Fatalf("result synthesized without arrival")
	}
}

// GitHub unreachable ⇒ a NAMED deferral, never a guess: no synthetic result, one notice.
func TestSyncStartupTodosDefersOnUnreachable(t *testing.T) {
	rs, res, n := stores(t)
	if err := rs.Put(mkRun("run_c", "org/app")); err != nil {
		t.Fatal(err)
	}
	src := &fakeSources{
		deliveries: map[string][]runs.Delivery{"run_c/org/app": {{
			ID: "dlv_1", ToCommit: "beef", MergedAt: ts("2026-07-27T09:00:00Z"),
		}}},
		containErr: errors.New("github unreachable"),
	}
	got, err := SyncStartupTodos(context.Background(), src, rs, res, n, nil)
	if err != nil || got != 0 {
		t.Fatalf("reconciled = %d, err = %v", got, err)
	}
	if list, _ := res.ForRun("run_c"); len(list) != 0 {
		t.Fatalf("guessed a result despite unreachable source")
	}
	notes, _ := n.List()
	if len(notes) != 1 || notes[0].Kind != startupNoticeKind {
		t.Fatalf("deferral not named: %+v", notes)
	}
}
