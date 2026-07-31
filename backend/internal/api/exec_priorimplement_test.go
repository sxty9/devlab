package api

// The run-scoped half of the task state. `mercury-dev` is a branch every run shares, so a workbench
// running ahead attests that undelivered work EXISTS, never whose it is. PriorImplementAt is the
// observation that says whose: it reads this run's own execution archive.
//
// Two ways it could lie, and both were live faults:
//   - counting ANOTHER run's execution — the fresh task is declared done and never built (measured
//     2026-07-31 on presentr, at 0 tokens; all 23 workbenches were ahead at the time)
//   - counting a REST-PATH implement — that stage creates nothing by definition, so a run that once
//     skipped wrongly would keep finding "I implemented here" in a record that only restated the
//     skip, and would never build the task again

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// newImplementDeps builds a composition over nothing but the execution archive.
func newImplementDeps(t *testing.T) (*ChainDeps, *runs.ResultStore) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "executions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", dir)
	rs := runs.NewResultStore(nil)
	return &ChainDeps{s: &Server{results: rs}}, rs
}

// putResult stores one execution of runID at repo with the given observation and implement state.
func putResult(t *testing.T, rs *runs.ResultStore, id, runID, repo string, task model.TaskState, implement model.StepState) {
	t.Helper()
	now := time.Now().UTC()
	err := rs.Put(runs.Result{
		ID: id, RunID: runID, RunTitle: "t", Kind: model.KindTodo, StartedAt: now, EndedAt: &now,
		Repos: []model.RepoPipeline{{
			Repo:      repo,
			TaskState: task,
			Stages: []model.StageView{
				{Stage: model.StagePreflight, State: model.StepExecuted},
				{Stage: model.StageImplement, State: implement},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPriorImplementIsScopedToTheRunAndToRealWork(t *testing.T) {
	d, rs := newImplementDeps(t)

	t.Run("no history at all", func(t *testing.T) {
		got, err := d.PriorImplementAt("run_a", "org/app")
		if err != nil || got {
			t.Fatalf("got %v (err %v), want false", got, err)
		}
	})

	t.Run("another run's implement is not this run's", func(t *testing.T) {
		putResult(t, rs, "ex_other", "run_other", "org/app", model.TaskNotImplemented, model.StepExecuted)
		got, err := d.PriorImplementAt("run_a", "org/app")
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Fatal("another run's work was read as this run's own — the fresh task would be skipped")
		}
	})

	t.Run("a rest-path implement counts as nothing", func(t *testing.T) {
		// Exactly the shape the presentr record carries: implement green, and the observation
		// underneath it says the stage only restated an existing state.
		putResult(t, rs, "ex_rest", "run_a", "org/app", model.TaskImplementedUndelivered, model.StepExecuted)
		got, err := d.PriorImplementAt("run_a", "org/app")
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Fatal("a rest-path implement counted itself as work — the run could never build the task again")
		}
	})

	t.Run("this run's own implement counts", func(t *testing.T) {
		putResult(t, rs, "ex_own", "run_a", "org/app", model.TaskNotImplemented, model.StepExecuted)
		got, err := d.PriorImplementAt("run_a", "org/app")
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Fatal("this run's own implement was not recognised — a lost delivery would be re-implemented")
		}
	})

	t.Run("a failed implement counts too", func(t *testing.T) {
		// The agent commits its own work, so a failure can still have left commits behind.
		putResult(t, rs, "ex_fail", "run_b", "org/other", model.TaskNotImplemented, model.StepFailed)
		got, err := d.PriorImplementAt("run_b", "org/other")
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Fatal("a failed implement left its commits unaccounted for")
		}
	})

	t.Run("an implement that never ran counts as nothing", func(t *testing.T) {
		putResult(t, rs, "ex_none", "run_c", "org/third", model.TaskNotImplemented, model.StepNotExecuted)
		got, err := d.PriorImplementAt("run_c", "org/third")
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Fatal("a stage that never ran was counted as work")
		}
	})
}

// Without the archive there is no answer — and no answer is named, never guessed as "false".
func TestPriorImplementWithoutTheArchiveIsAnError(t *testing.T) {
	d := &ChainDeps{s: &Server{}}
	if _, err := d.PriorImplementAt("run_a", "org/app"); err == nil {
		t.Fatal("a missing archive answered like an empty one")
	}
}
