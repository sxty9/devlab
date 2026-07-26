package api

import (
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// The "Aktive Läufe" overview reads ONE list: the run executing now PLUS every run suspended on the usage
// limit mid-execution. This pins the assembly — states, enrichment from the live result, and honest
// progress (an exact repo total for ToDos, none fabricated for automatic runs).
func TestAssembleInFlight(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(t.TempDir(), "res"))
	store, results := runs.NewStore(), runs.NewResults()
	s := &Server{runs: store, runResults: results}

	now := time.Now().UTC()
	resumeAt := now.Add(30 * time.Minute)

	execRun := runs.Run{
		ID: "run_exec", Name: "Live ToDo", Type: runs.TypeTodo, Enabled: true,
		Targets: []runs.Target{{Repo: "a"}, {Repo: "b"}},
	}
	suspRun := runs.Run{
		ID: "run_susp", Name: "Nightly", Type: runs.TypeAuto, Enabled: true,
		Suspended: &runs.Suspension{ResumeAt: resumeAt, ResultID: "rid_susp", Attempts: 2, Reason: "usage-limit"},
	}
	if _, err := store.Mutate("seed", "test", func([]runs.Run) ([]runs.Run, error) {
		return []runs.Run{execRun, suspRun}, nil
	}); err != nil {
		t.Fatal(err)
	}

	// Live result for the executing run: one repo done, one in flight on a running implement step.
	if err := results.Save(runs.Result{
		RunID: "run_exec", ResultID: "rid_exec", StartedAt: now,
		Repos: []runs.RepoResult{{Repo: "a", OK: true}},
		Live: &runs.RepoResult{Repo: "b", Running: true, Steps: []runs.Step{
			{Name: "analyze", OK: true},
			{Name: "implement", Running: true, Log: "…working"},
		}},
		InputTokens: 100, OutputTokens: 50, CostUSD: 0.25, NumTurns: 7,
	}); err != nil {
		t.Fatal(err)
	}
	// The suspended run's open husk: one repo already done before the limit paused it.
	if err := results.Save(runs.Result{
		RunID: "run_susp", ResultID: "rid_susp", StartedAt: now.Add(-time.Hour),
		Repos:       []runs.RepoResult{{Repo: "x", OK: true}},
		InputTokens: 10, OutputTokens: 5, CostUSD: 0.02, NumTurns: 3, Suspended: true, ResumeAt: &resumeAt,
	}); err != nil {
		t.Fatal(err)
	}

	list := s.assembleInFlight(&runs.Activity{RunID: "run_exec", ResultID: "rid_exec", StartedAt: now})
	if len(list) != 2 {
		t.Fatalf("want 2 in-flight, got %d: %+v", len(list), list)
	}

	ex := list[0]
	if ex.State != "executing" || ex.RunID != "run_exec" || ex.RunName != "Live ToDo" || ex.Type != "todo" {
		t.Fatalf("executing entry wrong: %+v", ex)
	}
	if ex.CurrentRepo != "b" || ex.CurrentStep != "implement" {
		t.Fatalf("executing not enriched from the live result: %+v", ex)
	}
	if ex.ReposDone != 1 || ex.ReposTotal != 2 {
		t.Fatalf("executing progress wrong: done=%d total=%d", ex.ReposDone, ex.ReposTotal)
	}
	if ex.OutputTokens != 50 || ex.NumTurns != 7 {
		t.Fatalf("executing spend not carried: %+v", ex)
	}

	sp := list[1]
	if sp.State != "suspended" || sp.RunID != "run_susp" || sp.Type != "auto" {
		t.Fatalf("suspended entry wrong: %+v", sp)
	}
	if sp.Attempts != 2 || sp.ResumeAt == nil || !sp.ResumeAt.Equal(resumeAt) {
		t.Fatalf("suspended fields wrong: %+v", sp)
	}
	if sp.ReposDone != 1 {
		t.Fatalf("suspended progress wrong: %+v", sp)
	}
	if sp.ReposTotal != 0 { // an automatic run has no cheap total → unknown, never fabricated
		t.Fatalf("auto run must not fabricate a repo total: %d", sp.ReposTotal)
	}
}

// The executing run must never be double-listed as suspended: a run cannot be both, and enrichment falls
// back gracefully when a result document is absent.
func TestAssembleInFlightNoDoubleCount(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(t.TempDir(), "res"))
	store := runs.NewStore()
	s := &Server{runs: store, runResults: runs.NewResults()}

	// A run that is BOTH the active one and still carries a stale Suspended pointer.
	if _, err := store.Mutate("seed", "test", func([]runs.Run) ([]runs.Run, error) {
		return []runs.Run{{
			ID: "run_x", Name: "X", Type: runs.TypeAuto, Enabled: true,
			Suspended: &runs.Suspension{ResumeAt: time.Now(), ResultID: "rid", Attempts: 1},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	list := s.assembleInFlight(&runs.Activity{RunID: "run_x", ResultID: "rid", StartedAt: time.Now().UTC()})
	if len(list) != 1 || list[0].State != "executing" {
		t.Fatalf("want a single executing entry, got %+v", list)
	}
}

// An idle system returns an empty — never nil — list (a nil slice marshals to JSON null and hangs the UI).
func TestAssembleInFlightIdle(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(t.TempDir(), "res"))
	s := &Server{runs: runs.NewStore(), runResults: runs.NewResults()}
	if got := s.assembleInFlight(nil); got == nil || len(got) != 0 {
		t.Fatalf("idle system must yield an empty (non-nil) list, got %#v", got)
	}
}
