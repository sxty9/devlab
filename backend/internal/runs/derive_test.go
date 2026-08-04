package runs

import (
	"testing"
	"time"

	"devlab/backend/internal/model"
)

func endedResult(id, runID string, repos ...model.RepoPipeline) Result {
	t := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	return Result{ID: id, RunID: runID, StartedAt: t.Add(-time.Hour), EndedAt: &t, Repos: repos}
}

// throughResult is an execution whose whole chain ran through to production: ended AND stamped with
// MergedAt (SettleExecution sets it only once every delivery settled through production, B-8 +
// WHAT-1). Only such an execution is history-ready under the strict rule.
func throughResult(id, runID string, repos ...model.RepoPipeline) Result {
	r := endedResult(id, runID, repos...)
	mt := r.EndedAt.Add(time.Hour)
	r.MergedAt = &mt
	return r
}

func okRepo(repo string) model.RepoPipeline {
	return model.RepoPipeline{Repo: repo, Stages: []model.StageView{{Stage: model.StageImplement, State: model.StepExecuted}}}
}

func failedRepo(repo string) model.RepoPipeline {
	return model.RepoPipeline{Repo: repo, Stages: []model.StageView{{Stage: model.StageImplement, State: model.StepFailed, Reason: "boom"}}}
}

// History readiness — STRICT (WHAT-4): an execution historizes only once it ENDED AND its whole
// chain ran through to production (proven by the MergedAt stamp), with no unsettled delivery still
// hanging on it. A blocked/failed early run that delivered nothing is a CURRENT state, not history.
func TestExecutionCompleted(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	through := throughResult("exec_1", "run_a", okRepo("svc"))

	if ExecutionCompleted(Result{ID: "exec_r", RunID: "run_a", StartedAt: now}, nil) {
		t.Fatal("a still-running execution (no EndedAt) must not be history-ready")
	}

	// THE HOLE THIS CLOSES: an ended execution that produced NO delivery (blocked or failed on an
	// early stage) earned no MergedAt stamp, so it is NOT history — even though nothing hangs on it.
	early := endedResult("exec_early", "run_a", failedRepo("svc"))
	if ExecutionCompleted(early, nil) {
		t.Fatal("an ended-but-never-shipped execution must NOT be history-ready (no chain-through)")
	}
	// The same holds for an ended run whose repos happen to read succeeded but which never earned a
	// settled-through-production stamp: still a current state, not history.
	if ExecutionCompleted(endedResult("exec_nostamp", "run_a", okRepo("svc")), nil) {
		t.Fatal("an ended execution with no MergedAt stamp must NOT be history-ready")
	}

	// A frozen archive record is a closed past — history by construction, MergedAt or not.
	legacy := endedResult("exec_legacy", "run_a", okRepo("svc"))
	legacy.Legacy = true
	if !ExecutionCompleted(legacy, nil) {
		t.Fatal("a legacy archive record is history-ready by construction")
	}

	// The chain ran through (MergedAt stamped) and nothing hangs ⇒ history-ready.
	if !ExecutionCompleted(through, nil) {
		t.Fatal("an ended, production-settled execution (MergedAt stamped) ⇒ history-ready")
	}

	// Defensive: the stamp and the ledger are written together, but should a delivery still read
	// unsettled, the surface must never show it as history.
	open := Delivery{ID: "dlv_1", Repo: "svc", ExecutionID: "exec_1", CreatedAt: now}
	if ExecutionCompleted(through, []Delivery{open}) {
		t.Fatal("an unsettled delivery must keep the execution out of the history even with a stamp")
	}

	// A settled (production-delivered) delivery of THIS execution, plus a foreign execution's open
	// delivery, leaves this one history-ready.
	mt := now.Add(time.Hour)
	pt := mt.Add(time.Hour)
	live := open
	live.MergedAt = &mt
	live.ProdDeployedAt = &pt
	foreign := Delivery{ID: "dlv_x", Repo: "svc", ExecutionID: "exec_other", CreatedAt: now}
	if !ExecutionCompleted(through, []Delivery{live, foreign}) {
		t.Fatal("another execution's open delivery must not block this settled one")
	}
}

// A todo is done only when ONE completed execution covered ALL its targets successfully; an
// auto run recurs and is never "done".
func TestRunCompleted(t *testing.T) {
	todo := Run{ID: "run_t", Kind: model.KindTodo, Targets: []Target{{Repo: "a"}, {Repo: "b"}}}

	partial := endedResult("exec_1", "run_t", okRepo("a")) // b never worked
	if RunCompleted(todo, []Result{partial}, nil) {
		t.Fatal("an execution missing a target must not complete the todo")
	}

	failed := endedResult("exec_2", "run_t", okRepo("a"), failedRepo("b"))
	if RunCompleted(todo, []Result{failed}, nil) {
		t.Fatal("a failed target pipeline must not complete the todo")
	}

	// Covering all targets is not enough on its own: the covering execution must also have run
	// through to production (MergedAt stamped). An ended-but-unstamped execution leaves the todo open.
	unstamped := endedResult("exec_3", "run_t", okRepo("a"), okRepo("b"))
	if RunCompleted(todo, []Result{unstamped}, nil) {
		t.Fatal("an execution that covered every target but never shipped must not complete the todo")
	}

	full := throughResult("exec_3", "run_t", okRepo("a"), okRepo("b"))
	if !RunCompleted(todo, []Result{full}, nil) {
		t.Fatal("a production-settled execution covering all targets completes the todo")
	}

	// The same execution with an unsettled delivery is not completed yet — neither is the todo.
	open := Delivery{ID: "dlv_1", Repo: "a", ExecutionID: "exec_3", CreatedAt: time.Now()}
	if RunCompleted(todo, []Result{full}, []Delivery{open}) {
		t.Fatal("an undelivered execution must not complete the todo")
	}

	auto := Run{ID: "run_auto", Kind: model.KindAuto, AxiomIDs: []string{"ax"}}
	if RunCompleted(auto, []Result{throughResult("exec_4", "run_auto", okRepo("a"))}, nil) {
		t.Fatal("an auto run recurs — it is never done")
	}
}

// REQ-011.2: open list ∩ history = ∅ — a done todo leaves the list the moment its completing
// execution enters the history; an unfinished execution appears nowhere in the history.
func TestSplitOpenHistoryDisjoint(t *testing.T) {
	doneTodo := Run{ID: "run_done", Kind: model.KindTodo, Targets: []Target{{Repo: "a"}}}
	openTodo := Run{ID: "run_open", Kind: model.KindTodo, Targets: []Target{{Repo: "b"}}}
	auto := Run{ID: "run_auto", Kind: model.KindAuto}

	completed := throughResult("exec_done", "run_done", okRepo("a"))
	running := Result{ID: "exec_live", RunID: "run_open", StartedAt: time.Now()}

	open, history := SplitOpenHistory([]Run{doneTodo, openTodo, auto}, []Result{completed, running}, nil)

	openIDs := map[string]bool{}
	for _, r := range open {
		openIDs[r.ID] = true
	}
	if openIDs["run_done"] {
		t.Fatal("a completed todo must leave the open list")
	}
	if !openIDs["run_open"] || !openIDs["run_auto"] {
		t.Fatalf("open list lost a live entry: %v", openIDs)
	}

	histIDs := map[string]bool{}
	for _, res := range history {
		histIDs[res.ID] = true
	}
	if !histIDs["exec_done"] || histIDs["exec_live"] {
		t.Fatalf("history must hold exactly the completed executions: %v", histIDs)
	}

	// Disjointness proper: no open run's completion evidence sits in history AND in the list.
	for _, r := range open {
		for _, res := range history {
			if res.RunID == r.ID && RunCompleted(r, []Result{res}, nil) {
				t.Fatalf("run %s is simultaneously open and completed by history entry %s", r.ID, res.ID)
			}
		}
	}

	// Never nil — a JSON null would blank the UI lists.
	if open == nil || history == nil {
		t.Fatal("SplitOpenHistory must return non-nil slices")
	}
}
