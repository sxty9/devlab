// Package sched is admission, slots, defer/overload, the due tick, the restart coordinator and
// the usage-limit pause (S7) — all PURE projections over the execution documents (execstate)
// plus the settings. It never imports the executor implementation: execution is injected as
// ExecFunc, and the executor reaches back ONLY through injected hooks (B-3).
package sched

import (
	"context"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
)

// ExecFunc is the ONE seam to S8 (the executor) — injected in cmd/devlabd.
type ExecFunc func(ctx context.Context, doc execstate.Doc, run runs.Run) error

// PreflightGate is the B-4 admission gate: preflight runs per target repo BEFORE any document
// is created; all targets delivered ⇒ no document, a reasoned answer instead.
type PreflightGate func(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error)

// MaintainFunc is the PR maintenance (deliver.Maintain), driven by the tick.
type MaintainFunc func(ctx context.Context) error

// Config are the scheduler's runtime knobs (env only seeds them; runtime wins).
type Config struct {
	Tick            time.Duration
	ResumeWindow    time.Duration
	DrainGrace      time.Duration
	MaxDuration     time.Duration
	LimitBackoff    time.Duration
	LimitMaxResumes int
	StartCap        int
}

// Scheduler coordinates admissions over the persisted documents.
type Scheduler struct {
	_ struct{}
}

// New wires the scheduler. All state lives in the stores; the scheduler holds no truth of its
// own.
func New(cfg Config, docs *execstate.Store, rs *runs.Store, res *runs.ResultStore, set *runs.SettingsStore,
	gate PreflightGate, exec ExecFunc, maintain MaintainFunc, pub live.Publisher) *Scheduler {
	panic("TODO(B2)")
}

// Start boots the scheduler: resume enqueueing (boot step 7), then the due ticker.
func (s *Scheduler) Start(ctx context.Context) error {
	panic("TODO(B2)")
}

// StartRequest asks to start (or resume) a run.
type StartRequest struct {
	RunID     string
	By        model.Actor
	Fresh     bool
	Placement *Placement
}

// Placement is the caller's slot decision when slots are contended.
type Placement struct {
	Kind             string // "queue" | "defer" | "overload"
	DeferExecutionID string
}

// Submit is the ONE atomic admission point: restart gate ⊕ admission ⊕ B-4 preflight gate ⊕
// resume-vs-fresh (runs.PlanResume). delivered targets drop out WITH evidence; ALL targets
// delivered ⇒ no document, StartOutcome.NotStarted (the K-3 start ban).
func (s *Scheduler) Submit(ctx context.Context, req StartRequest) (model.StartOutcome, error) {
	panic("TODO(B2)")
}

// Defer frees the slot; progress is kept (phase paused, reason deferred-by-user).
func (s *Scheduler) Defer(runID string, by model.Actor) error {
	panic("TODO(B2)")
}

// Resume resumes a deferred or blocked execution (the UI resumption path).
func (s *Scheduler) Resume(runID string, by model.Actor) error {
	panic("TODO(B2)")
}

// Cancel aborts exactly one run's live execution (REQ-013.5).
func (s *Scheduler) Cancel(runID string, by model.Actor) error {
	panic("TODO(B2)")
}

// PauseAllUsageLimit puts every live execution into the ONE shared usage-limit pause
// (REQ-016).
func (s *Scheduler) PauseAllUsageLimit(msg string, notBefore time.Time) error {
	panic("TODO(B2)")
}

// NoteAgentSuccess pulls the resume probe forward: any agent success ends the shared pause
// early (REQ-016.3).
func (s *Scheduler) NoteAgentSuccess() {
	panic("TODO(B2)")
}

// RequestRestart writes restart.json — atomic versus Submit (one mutex). Implemented here,
// injected into the executor seam (B-3).
func (s *Scheduler) RequestRestart(by model.Actor) (model.RestartState, error) {
	panic("TODO(B2)")
}

// RestartState reports the restart marker's state.
func (s *Scheduler) RestartState() model.RestartState {
	panic("TODO(B2)")
}

// RestartReady reports free ⇔ no document is "running" (the ready socket's truth).
func (s *Scheduler) RestartReady() bool {
	panic("TODO(B2)")
}

// DrainAndPersist is the SIGTERM drain: gate starts, SIGTERM the agent children, persist every
// running execution as interrupted (with continuation), flush results — all within DrainGrace.
func (s *Scheduler) DrainAndPersist(ctx context.Context) error {
	panic("TODO(B2)")
}

// SlotOverview is the read-only slot projection.
func (s *Scheduler) SlotOverview() (model.SlotOverview, error) {
	panic("TODO(B2)")
}

// AdmitDecision is the pure admission verdict.
type AdmitDecision struct {
	Admit      bool
	Overload   bool
	Queue      bool
	Reason     string
	BusyRepos  []string
	Suggestion *model.DeferSuggestion
}

// Admit is the pure admission rule (unit-testable without a process): capacity cap; overload
// only on a cap block, at most ONE living overload (never summing), never on a repo conflict;
// repo exclusivity is hard; busy repos queue at the BACK, never skipped (REQ-013/014/015).
func Admit(docs []execstate.Doc, set runs.Settings, targets []string, placement *Placement) (AdmitDecision, error) {
	panic("TODO(B2)")
}

// SuggestDefer proposes which execution to defer (C F3 deferScore) — with its reason.
func SuggestDefer(docs []execstate.Doc) *model.DeferSuggestion {
	panic("TODO(B2)")
}

// IsDue is THE ONE place "due" is decided; a living document for the run suppresses any new
// fire (no double start, no lost fire).
func IsDue(r runs.Run, lastStart *time.Time, now time.Time) bool {
	panic("TODO(B2)")
}
