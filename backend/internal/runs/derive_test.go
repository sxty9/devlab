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

func okRepo(repo string) model.RepoPipeline {
	return model.RepoPipeline{Repo: repo, Stages: []model.StageView{{Stage: model.StageImplement, State: model.StepExecuted}}}
}

func failedRepo(repo string) model.RepoPipeline {
	return model.RepoPipeline{Repo: repo, Stages: []model.StageView{{Stage: model.StageImplement, State: model.StepFailed, Reason: "boom"}}}
}

// History readiness (REQ-037.1, B-8): an execution historizes only once it ENDED and every
// delivery it produced is merged or closed with a reason — an open PR keeps it in the list.
func TestExecutionCompleted(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	res := endedResult("exec_1", "run_a", okRepo("svc"))

	if ExecutionCompleted(Result{ID: "exec_r", RunID: "run_a", StartedAt: now}, nil) {
		t.Fatal("a still-running execution (no EndedAt) must not be history-ready")
	}
	if !ExecutionCompleted(res, nil) {
		t.Fatal("an ended execution with no deliveries is history-ready")
	}

	open := Delivery{ID: "dlv_1", Repo: "svc", ExecutionID: "exec_1", CreatedAt: now}
	if ExecutionCompleted(res, []Delivery{open}) {
		t.Fatal("an open delivery (PR not merged) must keep the execution out of the history")
	}

	merged := open
	mt := now.Add(time.Hour)
	merged.MergedAt = &mt
	// WHAT-1: a merge is not the end — production is. A merged delivery still owing its production
	// step keeps its execution out of the history.
	if ExecutionCompleted(res, []Delivery{merged}) {
		t.Fatal("a merged delivery still owing production must keep the execution open (WHAT-1)")
	}

	live := merged
	pt := mt.Add(time.Hour)
	live.ProdDeployedAt = &pt
	if !ExecutionCompleted(res, []Delivery{live}) {
		t.Fatal("a merged, production-delivered delivery ⇒ history-ready")
	}

	// A repository proven to be no service needs no production — its merged delivery settles at once.
	napp := merged
	napp.ProdNotApplicable = true
	if !ExecutionCompleted(res, []Delivery{napp}) {
		t.Fatal("a merged, not-a-service delivery needs no production ⇒ history-ready")
	}

	closed := open
	closed.ClosedAt = &mt
	closed.ClosedReason = "rolled back"
	if !ExecutionCompleted(res, []Delivery{closed}) {
		t.Fatal("a delivery closed with a reason (rollback) also completes the execution")
	}

	foreign := Delivery{ID: "dlv_x", Repo: "svc", ExecutionID: "exec_other", CreatedAt: now}
	if !ExecutionCompleted(res, []Delivery{live, foreign}) {
		t.Fatal("another execution's open delivery must not block this one")
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

	full := endedResult("exec_3", "run_t", okRepo("a"), okRepo("b"))
	if !RunCompleted(todo, []Result{full}, nil) {
		t.Fatal("a completed execution covering all targets completes the todo")
	}

	// The same execution with an open delivery is not completed yet — neither is the todo.
	open := Delivery{ID: "dlv_1", Repo: "a", ExecutionID: "exec_3", CreatedAt: time.Now()}
	if RunCompleted(todo, []Result{full}, []Delivery{open}) {
		t.Fatal("an undelivered execution must not complete the todo")
	}

	auto := Run{ID: "run_auto", Kind: model.KindAuto, AxiomIDs: []string{"ax"}}
	if RunCompleted(auto, []Result{endedResult("exec_4", "run_auto", okRepo("a"))}, nil) {
		t.Fatal("an auto run recurs — it is never done")
	}
}

// REQ-011.2: open list ∩ history = ∅ — a done todo leaves the list the moment its completing
// execution enters the history; an unfinished execution appears nowhere in the history.
func TestSplitOpenHistoryDisjoint(t *testing.T) {
	doneTodo := Run{ID: "run_done", Kind: model.KindTodo, Targets: []Target{{Repo: "a"}}}
	openTodo := Run{ID: "run_open", Kind: model.KindTodo, Targets: []Target{{Repo: "b"}}}
	auto := Run{ID: "run_auto", Kind: model.KindAuto}

	completed := endedResult("exec_done", "run_done", okRepo("a"))
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
