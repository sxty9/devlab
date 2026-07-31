package runs

import (
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/model"
)

// PlanResume classification (C F1, REQ-019) — replacement for the deleted
// handlers_mercury_runs_live_test.go resumeOrNew coverage: pure decisions over the persisted
// truth, no I/O.

func interruptedDoc(id string, touched time.Time) execstate.Doc {
	return execstate.Doc{
		ID:        id,
		RunID:     "run_1",
		Kind:      model.KindTodo,
		Phase:     model.PhaseInterrupted,
		UpdatedAt: &touched,
		CreatedAt: touched.Add(-time.Hour),
		Repos: []execstate.RepoProgress{
			{Repo: "alpha", State: execstate.RepoDone},
			{Repo: "beta", State: execstate.RepoActive},
		},
		Continuation: &model.ContinuationView{Repo: "beta", Stage: model.StageImplement},
	}
}

// A live document within the resume window resumes — SAME id, with a reason that names the
// continuation point and the preserved progress.
func TestPlanResumeContinuesWithinWindow(t *testing.T) {
	now := time.Now()
	doc := interruptedDoc("exec_1", now.Add(-2*time.Hour))
	plan := PlanResume([]execstate.Doc{doc}, nil, nil, nil, 240*time.Hour, now)
	if plan.Action != ResumeActionResume || plan.ExecutionID != "exec_1" {
		t.Fatalf("must resume the same execution: %+v", plan)
	}
	if !strings.Contains(plan.Reason, "beta") || !strings.Contains(plan.Reason, "1 of 2") {
		t.Fatalf("the caller learns where and what continues: %q", plan.Reason)
	}
}

// A document older than the resume window counts as RELIABLY abandoned.
func TestPlanResumeAbandonsBeyondWindow(t *testing.T) {
	now := time.Now()
	doc := interruptedDoc("exec_1", now.Add(-300*time.Hour))
	plan := PlanResume([]execstate.Doc{doc}, nil, nil, nil, 240*time.Hour, now)
	if plan.Action != ResumeActionFresh {
		t.Fatalf("beyond the window the execution is abandoned: %+v", plan)
	}
	if !strings.Contains(plan.Reason, "abandoned") {
		t.Fatalf("the abandonment is named: %q", plan.Reason)
	}
}

// Paused and blocked documents resume too (one pause concept; blocked waits for the UI but a
// renewed trigger IS the resumption); terminal documents never do.
func TestPlanResumePhases(t *testing.T) {
	now := time.Now()
	for _, phase := range []model.ExecPhase{model.PhasePaused, model.PhaseBlocked, model.PhaseInterrupted} {
		d := interruptedDoc("exec_1", now.Add(-time.Hour))
		d.Phase = phase
		if plan := PlanResume([]execstate.Doc{d}, nil, nil, nil, 240*time.Hour, now); plan.Action != ResumeActionResume {
			t.Fatalf("%s must resume: %+v", phase, plan)
		}
	}
	for _, phase := range []model.ExecPhase{model.PhaseCompleted, model.PhaseFailed, model.PhaseDiscarded} {
		d := interruptedDoc("exec_1", now.Add(-time.Hour))
		d.Phase = phase
		if plan := PlanResume([]execstate.Doc{d}, nil, nil, nil, 240*time.Hour, now); plan.Action == ResumeActionResume {
			t.Fatalf("%s must never resume: %+v", phase, plan)
		}
	}
}

// An open PR with the same work is ADOPTED, never duplicated (REQ-019.5).
func TestPlanResumeAdoptsOpenPR(t *testing.T) {
	now := time.Now()
	pr := &model.PRRef{Number: 7, URL: "https://example.invalid/pr/7", HeadBranch: "fix/login"}
	plan := PlanResume(nil, nil, nil, pr, 240*time.Hour, now)
	if plan.Action != ResumeActionAdoptPR {
		t.Fatalf("open PR must be adopted: %+v", plan)
	}
	if !strings.Contains(plan.Reason, "#7") {
		t.Fatalf("the adoption names the PR: %q", plan.Reason)
	}
}

// An open execution-created delivery in the ledger equally means adoption, not duplication.
func TestPlanResumeAdoptsOpenDelivery(t *testing.T) {
	now := time.Now()
	open := []Delivery{{ID: "dlv_1", Repo: "alpha", Branch: "fix/x", ExecutionID: "exec_9", CreatedAt: now.Add(-time.Hour)}}
	plan := PlanResume(nil, nil, open, nil, 240*time.Hour, now)
	if plan.Action != ResumeActionAdoptPR || plan.ExecutionID != "exec_9" {
		t.Fatalf("open delivery must be adopted: %+v", plan)
	}
}

// Nothing to continue → fresh, with the reason said.
func TestPlanResumeFreshWhenNothingLives(t *testing.T) {
	plan := PlanResume(nil, nil, nil, nil, 240*time.Hour, time.Now())
	if plan.Action != ResumeActionFresh || plan.Reason == "" {
		t.Fatalf("fresh with a named reason: %+v", plan)
	}
}
