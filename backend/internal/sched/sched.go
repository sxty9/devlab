// Package sched is admission, slots, defer/overload, the due tick, the restart coordinator and
// the usage-limit pause (S7) — all PURE projections over the execution documents (execstate)
// plus the settings. It never imports the executor implementation: execution is injected as
// ExecFunc, and the executor reaches back ONLY through injected hooks (B-3).
package sched

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
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
	MaintainEvery   time.Duration
	ResumeWindow    time.Duration
	DrainGrace      time.Duration
	MaxDuration     time.Duration
	LimitBackoff    time.Duration
	LimitMaxResumes int
	StartCap        int
}

// Cancellation causes — the executor tells a deliberate abort, a defer, the shared usage-limit
// pause and a shutdown drain apart by context.Cause (replacement for the old ErrRunAborted;
// REQ-013.5).
var (
	ErrAborted     = errors.New("execution aborted by kill switch")
	ErrDeferred    = errors.New("execution deferred to free its slot")
	ErrUsagePause  = errors.New("execution paused on the shared usage limit")
	ErrShutdown    = errors.New("daemon draining for shutdown")
	ErrMaxDuration = errors.New("maximum execution duration exceeded")
)

// Named condition errors the HTTP layer maps to status codes.
var (
	ErrUnknownRun = errors.New("unknown run")
	ErrNotActive  = errors.New("run has no live execution")
	ErrNotRunning = errors.New("run has no running execution")
	ErrNotPaused  = errors.New("execution is not paused, blocked or interrupted")
)

// liveExec is the control handle of one running execution goroutine. It is NOT state — every
// admission/display/resume fact derives from the persisted documents; this only carries the
// cancel plumbing a mortal process needs to stop its own children.
type liveExec struct {
	runID  string
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// Scheduler coordinates admissions over the persisted documents.
type Scheduler struct {
	cfg      Config
	docs     *execstate.Store
	runs     *runs.Store
	results  *runs.ResultStore
	setStore *runs.SettingsStore
	gate     PreflightGate
	exec     ExecFunc
	maintain MaintainFunc
	pub      live.Publisher

	// settings is the capacity/default source — a seam over setStore.Get so tests can pin
	// settings without the store (the store itself stays the single runtime truth).
	settings func() (runs.Settings, error)
	// notice receives operator-facing one-liners (restart completed, …); wired to the notice
	// pool in cmd/devlabd, defaults to the log.
	notice func(kind, text string)
	now    func() time.Time

	mu       sync.Mutex // THE admission mutex: Submit ⊕ RequestRestart ⊕ promotion are atomic under it
	running  map[string]*liveExec
	draining bool
	poke     chan struct{}
	wg       sync.WaitGroup
}

// DefaultCapacity bounds concurrent executions when no capacity is configured.
const DefaultCapacity = 2

// defaultRestartDrainCap bounds the drain-out window reported on a restart request when no
// per-execution duration cap is configured (mirrors the restart wrapper's default max wait).
const defaultRestartDrainCap = 6 * time.Hour

// New wires the scheduler. All state lives in the stores; the scheduler holds no truth of its
// own.
func New(cfg Config, docs *execstate.Store, rs *runs.Store, res *runs.ResultStore, set *runs.SettingsStore,
	gate PreflightGate, exec ExecFunc, maintain MaintainFunc, pub live.Publisher) *Scheduler {
	if cfg.Tick <= 0 {
		cfg.Tick = 30 * time.Second
	}
	if cfg.MaintainEvery <= 0 {
		// The admission tick is fast (it lets contended executions in); GitHub PR maintenance is NOT.
		// A tracked pull request read every 20 s over dozens of entries exhausts the hourly request
		// budget, so the maintenance runs on its own, far wider cadence — decoupled from the tick.
		cfg.MaintainEvery = 5 * time.Minute
	}
	if cfg.ResumeWindow <= 0 {
		cfg.ResumeWindow = 240 * time.Hour
	}
	if cfg.DrainGrace <= 0 {
		cfg.DrainGrace = 60 * time.Second
	}
	if cfg.LimitBackoff <= 0 {
		cfg.LimitBackoff = 30 * time.Minute
	}
	if cfg.LimitMaxResumes <= 0 {
		cfg.LimitMaxResumes = 8
	}
	s := &Scheduler{
		cfg:      cfg,
		docs:     docs,
		runs:     rs,
		results:  res,
		setStore: set,
		gate:     gate,
		exec:     exec,
		maintain: maintain,
		pub:      pub,
		running:  map[string]*liveExec{},
		poke:     make(chan struct{}, 1),
		now:      func() time.Time { return time.Now().UTC() },
		notice:   func(kind, text string) { log.Printf("devlabd: sched notice [%s]: %s", kind, text) },
	}
	s.settings = func() (runs.Settings, error) {
		if s.setStore == nil {
			return runs.Settings{}, nil
		}
		return s.setStore.Get()
	}
	return s
}

// SetNoticeFunc routes operator notices (restart completed, abandoned executions) into the
// notice pool — wired in cmd/devlabd; the default logs.
func (s *Scheduler) SetNoticeFunc(f func(kind, text string)) {
	if f != nil {
		s.notice = f
	}
}

func (s *Scheduler) publish(t live.Topic) {
	if s.pub != nil {
		s.pub.Publish(t)
	}
}

func (s *Scheduler) pokeNow() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

func (s *Scheduler) settingsEffective() runs.Settings {
	set, err := s.settings()
	if err != nil || set.MaxConcurrency <= 0 {
		set.MaxConcurrency = DefaultCapacity
	}
	return set
}

// Start boots the scheduler: restart completion (boot step 4, idempotent when main already ran
// it), resume enqueueing (boot step 7), then the due ticker.
func (s *Scheduler) Start(ctx context.Context) error {
	if _, err := s.CompleteBootRestart(ctx); err != nil {
		return err
	}
	if err := s.enqueueInterruptedAtBoot(); err != nil {
		return err
	}
	go s.loop(ctx)
	s.pokeNow()
	return nil
}

// enqueueInterruptedAtBoot re-queues every interrupted execution (newest first) through the
// normal admission so it continues at the same spot (REQ-018.6); documents older than the
// resume window are reliably abandoned (REQ-019.3) — marked discarded, visibly.
func (s *Scheduler) enqueueInterruptedAtBoot() error {
	alive, err := s.docs.Live()
	if err != nil {
		return err
	}
	now := s.now()
	// docs.Live is oldest first; walk backwards for newest first.
	for i := len(alive) - 1; i >= 0; i-- {
		d := alive[i]
		if d.Phase != model.PhaseInterrupted {
			continue
		}
		if s.abandonedByAge(d, now) {
			_, _ = s.docs.Update(d.ID, func(doc *execstate.Doc) error {
				doc.SetPhase(model.PhaseDiscarded, "abandoned: older than the resume window", model.Actor{Autonomous: true}, now)
				return nil
			})
			s.notice("execution-abandoned", fmt.Sprintf("execution %s of run %s was abandoned (older than the resume window)", d.ID, d.RunID))
			continue
		}
		_, _ = s.docs.Update(d.ID, func(doc *execstate.Doc) error {
			if doc.Phase != model.PhaseInterrupted {
				return nil
			}
			doc.SetPhase(model.PhaseQueued, "auto-resume after restart", model.Actor{Autonomous: true}, now)
			return nil
		})
	}
	s.publish(live.TopicActive)
	s.publish(live.TopicSlots)
	return nil
}

func (s *Scheduler) abandonedByAge(d execstate.Doc, now time.Time) bool {
	ref := d.CreatedAt
	if d.UpdatedAt != nil {
		ref = *d.UpdatedAt
	}
	return now.Sub(ref) > s.cfg.ResumeWindow
}

// loop runs two independent cadences: the fast admission tick (limit-pause probes, queued
// promotion, due fires) and the much slower PR maintenance. They are SEPARATE tickers on purpose —
// the maintenance reads GitHub, and running it at the admission tick's pace is what exhausted the
// hourly request budget.
func (s *Scheduler) loop(ctx context.Context) {
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	mt := time.NewTicker(s.cfg.MaintainEvery)
	defer mt.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.poke:
			s.pass(ctx, false)
		case <-t.C:
			s.pass(ctx, true)
		case <-mt.C:
			s.runMaintain(ctx)
		}
	}
}

// runMaintain runs the injected PR maintenance, contained — a bad PR check must never take the
// daemon down.
func (s *Scheduler) runMaintain(ctx context.Context) {
	if s.maintain == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("devlabd: sched maintain panicked (contained): %v", r)
		}
	}()
	if err := s.maintain(ctx); err != nil {
		log.Printf("devlabd: sched maintain: %v", err)
	}
}

// pass is one admission sweep: resume due limit pauses, promote queued documents, fire due
// runs. fireDue is skipped on poke-passes only when false adds nothing (a poke means "a slot
// freed" — due runs are tick-driven).
func (s *Scheduler) pass(ctx context.Context, fireDue bool) {
	s.mu.Lock()
	changed := s.resumeDueLimitPausesLocked()
	changed = s.promoteQueuedLocked() || changed
	s.mu.Unlock()
	if fireDue {
		s.fireDue(ctx)
	}
	if changed {
		s.publish(live.TopicActive)
		s.publish(live.TopicSlots)
	}
}

// Transition reasons of the usage-limit resume paths — the attempt count is DERIVED from the
// transition protocol, so it survives every pause/resume cycle without a second counter.
const (
	reasonLimitProbe = "usage-limit resume probe"
	reasonLimitEarly = "resumed early: the usage limit has lifted"
)

// countLimitResumes derives how often this execution was resumed out of the shared limit pause.
func countLimitResumes(d execstate.Doc) int {
	n := 0
	for _, t := range d.Transitions {
		if t.Reason == reasonLimitProbe || t.Reason == reasonLimitEarly {
			n++
		}
	}
	return n
}

// resumeDueLimitPausesLocked promotes usage-limit-paused documents whose NotBefore passed —
// the resume PROBE: it re-queues (the next promotion actually starts it); past the resume
// budget the document turns blocked and waits for the UI (REQ-016).
func (s *Scheduler) resumeDueLimitPausesLocked() bool {
	docs, err := s.docs.Live()
	if err != nil {
		log.Printf("devlabd: sched limit-pause sweep: %v", err)
		return false
	}
	now := s.now()
	changed := false
	for _, d := range docs {
		if d.Phase != model.PhasePaused || d.Pause == nil || d.Pause.Reason != model.PauseUsageLimit {
			continue
		}
		if d.Pause.NotBefore != nil && d.Pause.NotBefore.After(now) {
			continue
		}
		changed = true
		if d.Pause.ResumeAttempts >= s.cfg.LimitMaxResumes {
			_, _ = s.docs.Update(d.ID, func(doc *execstate.Doc) error {
				doc.SetPhase(model.PhaseBlocked,
					fmt.Sprintf("usage limit persisted after %d resume attempts", doc.Pause.ResumeAttempts),
					model.Actor{Autonomous: true}, now)
				return nil
			})
			continue
		}
		_, _ = s.docs.Update(d.ID, func(doc *execstate.Doc) error {
			if doc.Phase != model.PhasePaused {
				return nil
			}
			doc.Pause = nil
			doc.SetPhase(model.PhaseQueued, reasonLimitProbe, model.Actor{Autonomous: true}, now)
			return nil
		})
	}
	return changed
}

// promoteQueuedLocked starts every queued document the admission rules allow, oldest first —
// busy repos stay queued (put to the back by order), never skipped. StartCap (when set) bounds
// one pass's start burst. It is also where a queued start that can NEVER be kept is retired
// visibly: a vanished run or one older than the resume window turns discarded with its reason and
// an operator notice — the same sweep that keeps the promise is the one that reports breaking it.
func (s *Scheduler) promoteQueuedLocked() bool {
	if s.draining || s.restartPendingLocked() {
		return false
	}
	docs, err := s.docs.Live()
	if err != nil {
		log.Printf("devlabd: sched promotion sweep: %v", err)
		return false
	}
	set := s.settingsEffective()
	now := s.now()
	started := 0
	changed := false
	for _, d := range docs {
		if d.Phase != model.PhaseQueued {
			continue
		}
		if s.running[d.ID] != nil {
			// Its previous goroutine is still winding down — it pokes a new pass on exit.
			continue
		}
		// A queued start is a promise, not an immortal one. The two reasons a promise cannot be
		// kept — the run is gone, or the start is older than the resume window — end it VISIBLY:
		// the document turns discarded WITH its reason, and an operator notice names it. A queued
		// start never disappears in silence (REQ-018 · "verlieren ist erlaubt, schweigen nicht").
		if s.abandonedByAge(d, now) {
			upd, _ := s.docs.Update(d.ID, func(doc *execstate.Doc) error {
				if doc.Phase != model.PhaseQueued {
					return nil
				}
				doc.SetPhase(model.PhaseDiscarded, "abandoned: queued longer than the resume window", model.Actor{Autonomous: true}, now)
				return nil
			})
			if upd.Phase == model.PhaseDiscarded {
				s.notice("execution-abandoned", fmt.Sprintf("queued start of run %s (execution %s) abandoned: it waited longer than the resume window", d.RunID, d.ID))
				changed = true
			}
			continue
		}
		// Run-existence is checked BEFORE admission so a vanished run is closed at once, never left
		// to wait out a busy repository in silence.
		run, ok, err := s.runs.Get(d.RunID)
		if err != nil {
			continue // a transient read error: leave it queued and retry next pass
		}
		if !ok {
			upd, _ := s.docs.Update(d.ID, func(doc *execstate.Doc) error {
				if doc.Phase != model.PhaseQueued {
					return nil
				}
				doc.SetPhase(model.PhaseDiscarded, "run definition no longer exists", model.Actor{Autonomous: true}, now)
				return nil
			})
			if upd.Phase == model.PhaseDiscarded {
				s.notice("execution-abandoned", fmt.Sprintf("queued start of run %s (execution %s) abandoned: its run definition no longer exists", d.RunID, d.ID))
				changed = true
			}
			continue
		}
		if s.cfg.StartCap > 0 && started >= s.cfg.StartCap {
			continue // the start burst is spent this pass; the doc stays queued for the next
		}
		// Re-read the live picture each admission — an earlier start in this pass claims slots.
		cur, err := s.docs.Live()
		if err != nil {
			break
		}
		dec, err := Admit(without(cur, d.ID), set, claimTargets(d), placementForDoc(d))
		if err != nil || !dec.Admit {
			continue
		}
		if err := s.startLocked(d.ID, run); err != nil {
			log.Printf("devlabd: sched promote %s: %v", d.ID, err)
			continue
		}
		started++
		changed = true
	}
	return changed
}

// placementForDoc keeps an overload document an overload through re-admission.
func placementForDoc(d execstate.Doc) *Placement {
	if d.Overload {
		return &Placement{Kind: PlacementOverload}
	}
	return nil
}

func without(docs []execstate.Doc, id string) []execstate.Doc {
	out := make([]execstate.Doc, 0, len(docs))
	for _, d := range docs {
		if d.ID != id {
			out = append(out, d)
		}
	}
	return out
}

// fireDue fires every due run through the ONE submission path (queued when contended — due
// work is never skipped). A live document for a run suppresses its fire entirely.
func (s *Scheduler) fireDue(ctx context.Context) {
	all, err := s.runs.List()
	if err != nil {
		log.Printf("devlabd: sched due sweep: %v", err)
		return
	}
	now := s.now()
	for _, r := range all {
		liveDoc, err := s.docs.LiveForRun(r.ID)
		if err != nil || liveDoc != nil {
			continue // a live document suppresses any new fire (no double start)
		}
		lastStart, err := s.lastStart(r.ID)
		if err != nil {
			continue
		}
		if !IsDue(r, lastStart, now) {
			continue
		}
		out, err := s.Submit(ctx, StartRequest{
			RunID:     r.ID,
			By:        model.Actor{Autonomous: true},
			Placement: &Placement{Kind: PlacementQueue},
		})
		if err != nil {
			log.Printf("devlabd: sched due fire %s: %v", r.ID, err)
			continue
		}
		if out.NotStarted != "" {
			log.Printf("devlabd: sched due fire %s not started: %s", r.ID, out.NotStarted)
		}
	}
}

// lastStart derives when a run last ACTUALLY started — from the execution documents plus the
// (new and legacy) results. The document written at queued→running IS the schedule
// progression; there is no second pointer to advance (B 1.3c).
func (s *Scheduler) lastStart(runID string) (*time.Time, error) {
	var last *time.Time
	docs, err := s.docs.List()
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		if d.RunID != runID || d.StartedAt == nil {
			continue
		}
		if last == nil || d.StartedAt.After(*last) {
			t := *d.StartedAt
			last = &t
		}
	}
	if s.results != nil {
		res, err := s.results.ForRun(runID)
		if err != nil {
			return nil, err
		}
		for _, r := range res {
			if r.StartedAt.IsZero() {
				continue
			}
			if last == nil || r.StartedAt.After(*last) {
				t := r.StartedAt
				last = &t
			}
		}
	}
	return last, nil
}

// StartRequest asks to start (or resume) a run.
type StartRequest struct {
	RunID string
	By    model.Actor
	// Fresh discards a resumable execution (paused/blocked/interrupted) and starts over. It is scoped
	// to the resume decision ALONE: it never overrides the "already delivered" determination (the K-3
	// start ban), which is bound to the todo's content and decided by the preflight gate. A delivered
	// todo whose text has not grown stays refused with fresh=true; a grown todo starts with or without
	// it. There is deliberately NO switch that bypasses the delivered determination (it is made right,
	// not skipped).
	Fresh     bool
	Placement *Placement
}

// Placement is the caller's slot decision when slots are contended.
type Placement struct {
	Kind             string // "queue" | "defer" | "overload"
	DeferExecutionID string
}

// Placement kinds (mirrored by src/data/source.ts StartPlacement).
const (
	PlacementQueue    = "queue"
	PlacementDefer    = "defer"
	PlacementOverload = "overload"
)

// Submit is the ONE atomic admission point: restart gate ⊕ admission ⊕ B-4 preflight gate ⊕
// resume-vs-fresh (runs.PlanResume). delivered targets drop out WITH evidence; ALL targets
// delivered ⇒ no document, StartOutcome.NotStarted (the K-3 start ban).
func (s *Scheduler) Submit(ctx context.Context, req StartRequest) (model.StartOutcome, error) {
	run, ok, err := s.runs.Get(req.RunID)
	if err != nil {
		return model.StartOutcome{}, err
	}
	if !ok {
		return model.StartOutcome{}, fmt.Errorf("%w: %s", ErrUnknownRun, req.RunID)
	}

	// The B-4 preflight gate runs BEFORE any document exists and outside the admission mutex
	// (it talks to git/GitHub). Only fresh starts of todos gate; a resume keeps its document.
	taskStates, taskEvidence, remaining, gateErr := s.gateTargets(ctx, run)
	if gateErr != nil {
		return model.StartOutcome{}, gateErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Restart gate: a pending restart (or a drain) queues the start instead of racing it.
	if st := s.restartStateLocked(); st.Pending {
		if err := s.appendQueuedStartLocked(req, run); err != nil {
			return model.StartOutcome{}, err
		}
		s.publish(live.TopicRestart)
		return model.StartOutcome{
			Queued:         true,
			RestartPending: true,
			NotStarted:     "a restart is pending — the start is queued and fires after the restart",
		}, nil
	}
	if s.draining {
		doc, err := s.createDocLocked(run, remaining, false, req.By, "queued during shutdown drain")
		if err != nil {
			return model.StartOutcome{}, err
		}
		return model.StartOutcome{ExecutionID: doc.ID, Queued: true, RestartPending: true, TaskStates: taskStates}, nil
	}

	// Resume-vs-fresh over the run's live document (REQ-019).
	liveDoc, err := s.docs.LiveForRun(run.ID)
	if err != nil {
		return model.StartOutcome{}, err
	}
	if liveDoc != nil {
		switch liveDoc.Phase {
		case model.PhaseQueued, model.PhaseRunning:
			return model.StartOutcome{
				ExecutionID: liveDoc.ID,
				NotStarted:  "already executing — the active execution is the one addressed",
			}, nil
		}
		// paused | blocked | interrupted — resumable.
		if req.Fresh {
			now := s.now()
			if _, err := s.docs.Update(liveDoc.ID, func(doc *execstate.Doc) error {
				doc.SetPhase(model.PhaseDiscarded, "fresh start requested — previous execution discarded", req.By, now)
				return nil
			}); err != nil {
				return model.StartOutcome{}, err
			}
		} else {
			var plan runs.ResumePlan
			if res, rerr := s.resultsForRun(run.ID); rerr == nil {
				plan = runs.PlanResume([]execstate.Doc{*liveDoc}, res, nil, nil, s.cfg.ResumeWindow, s.now())
			} else {
				plan = runs.PlanResume([]execstate.Doc{*liveDoc}, nil, nil, nil, s.cfg.ResumeWindow, s.now())
			}
			if plan.Action == "resume" {
				out, err := s.resumeDocLocked(*liveDoc, req.By)
				if err != nil {
					return model.StartOutcome{}, err
				}
				out.TaskStates = taskStates
				return out, nil
			}
			// Reliably abandoned (older than the resume window) — discard visibly, start fresh.
			now := s.now()
			if _, err := s.docs.Update(liveDoc.ID, func(doc *execstate.Doc) error {
				doc.SetPhase(model.PhaseDiscarded, plan.Reason, model.Actor{Autonomous: true, OnBehalfOf: req.By.User}, now)
				return nil
			}); err != nil {
				return model.StartOutcome{}, err
			}
		}
	}

	// K-3 start ban: every target already delivered ⇒ no document at all.
	if run.Kind == model.KindTodo && len(run.Targets) > 0 && len(remaining) == 0 {
		return model.StartOutcome{
			NotStarted:   "already delivered — every target repository carries this todo's work in its default branch, and the todo text still asks for exactly that work; see taskEvidence for the delivered stand",
			TaskStates:   taskStates,
			TaskEvidence: taskEvidence,
		}, nil
	}

	// Admission (pure) over the live documents.
	liveDocs, err := s.docs.Live()
	if err != nil {
		return model.StartOutcome{}, err
	}
	set := s.settingsEffective()
	dec, err := Admit(liveDocs, set, remaining, req.Placement)
	if err != nil {
		return model.StartOutcome{}, err
	}

	if dec.DeferFirst != "" {
		if err := s.deferExecLocked(dec.DeferFirst, req.By); err != nil {
			return model.StartOutcome{}, err
		}
		liveDocs, err = s.docs.Live()
		if err != nil {
			return model.StartOutcome{}, err
		}
		dec, err = Admit(liveDocs, set, remaining, &Placement{Kind: PlacementQueue})
		if err != nil {
			return model.StartOutcome{}, err
		}
		dec.Queue = !dec.Admit
	}

	switch {
	case dec.Admit:
		doc, err := s.createDocLocked(run, remaining, dec.Overload, req.By, "")
		if err != nil {
			return model.StartOutcome{}, err
		}
		if err := s.startLocked(doc.ID, run); err != nil {
			return model.StartOutcome{}, err
		}
		s.publish(live.TopicActive)
		s.publish(live.TopicSlots)
		return model.StartOutcome{ExecutionID: doc.ID, Started: true, Fresh: req.Fresh, TaskStates: taskStates}, nil
	case dec.Queue:
		doc, err := s.createDocLocked(run, remaining, false, req.By, dec.Reason)
		if err != nil {
			return model.StartOutcome{}, err
		}
		s.publish(live.TopicActive)
		s.publish(live.TopicSlots)
		return model.StartOutcome{ExecutionID: doc.ID, Queued: true, TaskStates: taskStates}, nil
	default:
		return model.StartOutcome{
			NotStarted: dec.Reason,
			TaskStates: taskStates,
			Suggestion: dec.Suggestion,
		}, nil
	}
}

// gateTargets runs the B-4 preflight gate per target repo. Delivered targets drop out WITH their
// evidence recorded (so a "delivered" rejection can name the stand it rests on); an unreachable
// source keeps the target (unknown — never guessed).
func (s *Scheduler) gateTargets(ctx context.Context, run runs.Run) (map[string]model.TaskState, map[string][]string, []string, error) {
	if run.Kind != model.KindTodo || len(run.Targets) == 0 {
		return nil, nil, nil, nil
	}
	states := map[string]model.TaskState{}
	evidence := map[string][]string{}
	remaining := []string{}
	for _, t := range run.Targets {
		if t.Repo == "" {
			continue
		}
		if t.Create || s.gate == nil {
			// A repo to be created cannot be delivered yet; without a gate nothing is dropped.
			states[t.Repo] = model.TaskNotImplemented
			remaining = append(remaining, t.Repo)
			continue
		}
		f, err := s.gate(ctx, t.Repo, run)
		if err != nil {
			states[t.Repo] = model.TaskUnknown
			remaining = append(remaining, t.Repo)
			continue
		}
		states[t.Repo] = f.State
		if len(f.Evidence) > 0 {
			evidence[t.Repo] = f.Evidence
		}
		if f.State != model.TaskDelivered {
			remaining = append(remaining, t.Repo)
		}
	}
	return states, evidence, remaining, nil
}

func (s *Scheduler) resultsForRun(runID string) ([]runs.Result, error) {
	if s.results == nil {
		return nil, nil
	}
	return s.results.ForRun(runID)
}

// createDocLocked writes the execution document: created, then queued (both recorded). The
// repo order is the admission order — free repos first, busy ones at the back, never dropped.
func (s *Scheduler) createDocLocked(run runs.Run, targets []string, overload bool, by model.Actor, queueReason string) (execstate.Doc, error) {
	doc, err := s.docs.Create(run.ID, run.Kind, targets, overload, by)
	if err != nil {
		return execstate.Doc{}, err
	}
	reason := queueReason
	now := s.now()
	return s.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhaseQueued, reason, by, now)
		return nil
	})
}

// startLocked flips one queued/created document to running and launches its goroutine. The
// document write IS the schedule progression (a live doc suppresses any further fire).
func (s *Scheduler) startLocked(execID string, run runs.Run) error {
	if _, dup := s.running[execID]; dup {
		return fmt.Errorf("execution %s already has a live goroutine", execID)
	}
	now := s.now()
	doc, err := s.docs.Update(execID, func(d *execstate.Doc) error {
		t := now
		d.StartedAt = &t
		d.Pause = nil
		d.SetPhase(model.PhaseRunning, "", model.Actor{Autonomous: true}, now)
		return nil
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	if s.cfg.MaxDuration > 0 {
		// The overall duration ceiling per execution is configuration (S8); enforce it here so
		// a hung agent can never hold a slot forever.
		t := time.AfterFunc(s.cfg.MaxDuration, func() { cancel(ErrMaxDuration) })
		go func() { <-ctx.Done(); t.Stop() }()
	}
	le := &liveExec{runID: run.ID, cancel: cancel, done: make(chan struct{})}
	s.running[execID] = le
	s.wg.Add(1)
	go s.runExec(ctx, cancel, le, doc, run)
	return nil
}

// runExec is the goroutine body: execute, then terminalize the document honestly, then free the
// slot (which pokes the next promotion).
func (s *Scheduler) runExec(ctx context.Context, cancel context.CancelCauseFunc, le *liveExec, doc execstate.Doc, run runs.Run) {
	defer s.wg.Done()
	defer close(le.done)
	err := s.execContained(ctx, doc, run)
	s.finalizeExec(ctx, doc.ID, err)
	cancel(nil)
	s.mu.Lock()
	delete(s.running, doc.ID)
	s.mu.Unlock()
	s.publish(live.TopicActive)
	s.publish(live.TopicSlots)
	s.pokeNow()
}

// execContained shields the daemon from a panicking executor.
func (s *Scheduler) execContained(ctx context.Context, doc execstate.Doc, run runs.Run) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("execution panicked: %v", r)
		}
	}()
	if s.exec == nil {
		return errors.New("no executor wired")
	}
	return s.exec(ctx, doc, run)
}

// finalizeExec terminalizes the document after the executor returned — honestly, by cause. A
// document the executor (or a pause/defer) already moved out of running is left as it stands.
func (s *Scheduler) finalizeExec(ctx context.Context, execID string, execErr error) {
	now := s.now()
	cause := context.Cause(ctx)
	doc, err := s.docs.Update(execID, func(d *execstate.Doc) error {
		if d.Phase != model.PhaseRunning {
			return nil // already terminal, paused or blocked — the transition that moved it owns the truth
		}
		switch {
		case errors.Is(cause, ErrShutdown):
			d.SetPhase(model.PhaseInterrupted, "shutdown", model.Actor{Autonomous: true}, now)
		case errors.Is(cause, ErrMaxDuration):
			d.SetPhase(model.PhaseFailed,
				fmt.Sprintf("maximum execution duration exceeded (%s)", s.cfg.MaxDuration), model.Actor{Autonomous: true}, now)
		case errors.Is(cause, ErrAborted):
			// Cancel stamped phase+actor already; reaching here means the stamp raced — record it now.
			d.SetPhase(model.PhaseFailed, "aborted", model.Actor{Autonomous: true}, now)
		case execErr != nil:
			d.SetPhase(model.PhaseFailed, execErr.Error(), model.Actor{Autonomous: true}, now)
		default:
			if blocked := firstBlockedRepo(d); blocked != "" {
				d.SetPhase(model.PhaseBlocked, "repository "+blocked+" is blocked", model.Actor{Autonomous: true}, now)
			} else {
				d.SetPhase(model.PhaseCompleted, "", model.Actor{Autonomous: true}, now)
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("devlabd: sched finalize %s: %v", execID, err)
		return
	}
	if doc.Phase == model.PhaseCompleted {
		// A finished execution proves the subscription limit (if any) has lifted — pull the
		// shared resume probe forward (REQ-016.3).
		s.NoteAgentSuccess()
	}
}

func firstBlockedRepo(d *execstate.Doc) string {
	for _, r := range d.Repos {
		if r.State == execstate.RepoBlocked {
			return r.Repo
		}
	}
	return ""
}

// resumeDocLocked promotes a paused/blocked/interrupted document back into the queue under its
// OWN id (REQ-019.1) and starts it at once when admissible.
func (s *Scheduler) resumeDocLocked(doc execstate.Doc, by model.Actor) (model.StartOutcome, error) {
	now := s.now()
	updated, err := s.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.Pause = nil
		for i := range d.Repos {
			if d.Repos[i].State == execstate.RepoBlocked {
				d.Repos[i].State = execstate.RepoPending
				d.Repos[i].Block = nil
			}
		}
		d.SetPhase(model.PhaseQueued, "resumed", by, now)
		return nil
	})
	if err != nil {
		return model.StartOutcome{}, err
	}
	out := model.StartOutcome{ExecutionID: updated.ID, Resumed: true}
	liveDocs, err := s.docs.Live()
	if err != nil {
		return out, err
	}
	dec, err := Admit(without(liveDocs, updated.ID), s.settingsEffective(), claimTargets(updated), placementForDoc(updated))
	if err == nil && dec.Admit && s.running[updated.ID] == nil {
		run, ok, rerr := s.runs.Get(updated.RunID)
		if rerr == nil && ok {
			if serr := s.startLocked(updated.ID, run); serr == nil {
				out.Started = true
			}
		}
	}
	if !out.Started {
		out.Queued = true
	}
	s.publish(live.TopicActive)
	s.publish(live.TopicSlots)
	return out, nil
}

// Defer frees the slot; progress is kept (phase paused, reason deferred-by-user).
func (s *Scheduler) Defer(runID string, by model.Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.docs.LiveForRun(runID)
	if err != nil {
		return err
	}
	if doc == nil {
		return fmt.Errorf("%w: %s", ErrNotActive, runID)
	}
	if doc.Phase != model.PhaseRunning {
		return fmt.Errorf("%w: %s is %s", ErrNotRunning, runID, doc.Phase)
	}
	return s.deferExecLocked(doc.ID, by)
}

func (s *Scheduler) deferExecLocked(execID string, by model.Actor) error {
	now := s.now()
	doc, err := s.docs.Update(execID, func(d *execstate.Doc) error {
		if d.Phase != model.PhaseRunning {
			return fmt.Errorf("%w: execution %s is %s", ErrNotRunning, execID, d.Phase)
		}
		attempts := 0
		if d.Pause != nil {
			attempts = d.Pause.ResumeAttempts
		}
		d.Pause = &model.PauseView{Reason: model.PauseDeferredByUser, ResumeAttempts: attempts}
		d.SetPhase(model.PhasePaused, "deferred to free the slot", by, now)
		return nil
	})
	if err != nil {
		return err
	}
	if le := s.running[doc.ID]; le != nil {
		le.cancel(ErrDeferred)
	}
	s.publish(live.TopicActive)
	s.publish(live.TopicSlots)
	return nil
}

// Resume resumes a deferred or blocked execution (the UI resumption path).
func (s *Scheduler) Resume(runID string, by model.Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.docs.LiveForRun(runID)
	if err != nil {
		return err
	}
	if doc == nil {
		return fmt.Errorf("%w: %s", ErrNotActive, runID)
	}
	switch doc.Phase {
	case model.PhasePaused, model.PhaseBlocked, model.PhaseInterrupted:
		_, err := s.resumeDocLocked(*doc, by)
		return err
	default:
		return fmt.Errorf("%w: %s is %s", ErrNotPaused, runID, doc.Phase)
	}
}

// Cancel aborts exactly one run's live execution (REQ-013.5) — other executions keep going.
func (s *Scheduler) Cancel(runID string, by model.Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.docs.LiveForRun(runID)
	if err != nil {
		return err
	}
	if doc == nil {
		return fmt.Errorf("%w: %s", ErrNotActive, runID)
	}
	now := s.now()
	switch doc.Phase {
	case model.PhaseRunning:
		if _, err := s.docs.Update(doc.ID, func(d *execstate.Doc) error {
			d.SetPhase(model.PhaseFailed, "aborted by user", by, now)
			return nil
		}); err != nil {
			return err
		}
		if le := s.running[doc.ID]; le != nil {
			le.cancel(ErrAborted)
		}
	default: // queued | paused | blocked | interrupted — never ran to an end: discard visibly
		if _, err := s.docs.Update(doc.ID, func(d *execstate.Doc) error {
			d.SetPhase(model.PhaseDiscarded, "aborted before completion", by, now)
			return nil
		}); err != nil {
			return err
		}
	}
	s.publish(live.TopicActive)
	s.publish(live.TopicSlots)
	return nil
}

// FailForDeclinedQuestion ends a run because the user REJECTED its blocking question. The run's live
// execution — running, blocked, paused or interrupted — is marked FAILED with the rejection as its
// reason and, if a goroutine still runs it, cancelled; either way the execution leaves the live set,
// so its slot is freed. It clears any repository still marked blocked on that execution, so the
// rejection leaves NO lingering hold. A run with no live execution is a no-op — nothing holds a slot.
// It never touches deliveries: a question mints none, so there is no hanging delivery tip to unwind.
func (s *Scheduler) FailForDeclinedQuestion(runID string, by model.Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.docs.LiveForRun(runID)
	if err != nil {
		return err
	}
	if doc == nil {
		return nil // no live execution — the slot is already free
	}
	now := s.now()
	if _, err := s.docs.Update(doc.ID, func(d *execstate.Doc) error {
		for i := range d.Repos {
			if d.Repos[i].State == execstate.RepoBlocked {
				d.Repos[i].State = execstate.RepoPending
				d.Repos[i].Block = nil
			}
		}
		d.SetPhase(model.PhaseFailed, "declined by the user — the run's blocking question was rejected", by, now)
		return nil
	}); err != nil {
		return err
	}
	if le := s.running[doc.ID]; le != nil {
		le.cancel(ErrAborted)
	}
	s.publish(live.TopicActive)
	s.publish(live.TopicSlots)
	return nil
}

// PauseAllUsageLimit puts every live execution into the ONE shared usage-limit pause
// (REQ-016): the limit binds in sum, so everyone pauses together and resumes together.
func (s *Scheduler) PauseAllUsageLimit(msg string, notBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	docs, err := s.docs.Live()
	if err != nil {
		return err
	}
	now := s.now()
	nb := notBefore
	if nb.IsZero() {
		nb = now.Add(s.cfg.LimitBackoff)
	}
	for _, d := range docs {
		if d.Phase != model.PhaseRunning && d.Phase != model.PhaseQueued {
			continue
		}
		phaseWas := d.Phase
		if _, err := s.docs.Update(d.ID, func(doc *execstate.Doc) error {
			t := nb
			doc.Pause = &model.PauseView{
				Reason:         model.PauseUsageLimit,
				Message:        msg,
				ResumeAttempts: countLimitResumes(*doc),
				NotBefore:      &t,
			}
			doc.SetPhase(model.PhasePaused, "usage limit reached", model.Actor{Autonomous: true}, now)
			return nil
		}); err != nil {
			return err
		}
		if phaseWas == model.PhaseRunning {
			if le := s.running[d.ID]; le != nil {
				le.cancel(ErrUsagePause)
			}
		}
	}
	s.publish(live.TopicActive)
	s.publish(live.TopicSlots)
	return nil
}

// NoteAgentSuccess pulls the resume probe forward: any agent success ends the shared pause
// early (REQ-016.3) — the limit has demonstrably lifted, so nobody waits out the deadline.
func (s *Scheduler) NoteAgentSuccess() {
	s.mu.Lock()
	docs, err := s.docs.Live()
	if err != nil {
		s.mu.Unlock()
		return
	}
	now := s.now()
	changed := false
	for _, d := range docs {
		if d.Phase != model.PhasePaused || d.Pause == nil || d.Pause.Reason != model.PauseUsageLimit {
			continue
		}
		_, _ = s.docs.Update(d.ID, func(doc *execstate.Doc) error {
			if doc.Phase != model.PhasePaused {
				return nil
			}
			doc.Pause = nil
			doc.SetPhase(model.PhaseQueued, reasonLimitEarly, model.Actor{Autonomous: true}, now)
			return nil
		})
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.publish(live.TopicActive)
		s.publish(live.TopicSlots)
		s.pokeNow()
	}
}

// DrainAndPersist is the SIGTERM drain: gate starts, SIGTERM the agent children, persist every
// running execution as interrupted (with continuation), flush results — all within DrainGrace.
func (s *Scheduler) DrainAndPersist(ctx context.Context) error {
	s.mu.Lock()
	s.draining = true
	handles := make([]*liveExec, 0, len(s.running))
	ids := make([]string, 0, len(s.running))
	for id, le := range s.running {
		handles = append(handles, le)
		ids = append(ids, id)
	}
	s.mu.Unlock()

	for _, le := range handles {
		le.cancel(ErrShutdown)
	}

	deadline := time.NewTimer(s.cfg.DrainGrace)
	defer deadline.Stop()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-deadline.C:
	case <-ctx.Done():
	}

	// Whatever still reads running is persisted interrupted NOW — the stop budget wins over a
	// slow goroutine; the document (with its continuation) is the resume truth.
	now := s.now()
	for _, id := range ids {
		_, _ = s.docs.Update(id, func(d *execstate.Doc) error {
			if d.Phase != model.PhaseRunning {
				return nil
			}
			d.SetPhase(model.PhaseInterrupted, "shutdown", model.Actor{Autonomous: true}, now)
			return nil
		})
	}
	return nil
}
