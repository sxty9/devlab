package runs

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// ErrRunAborted is the cancellation cause the kill-switch (Cancel) attaches to a run's context. It lets
// the executor tell a DELIBERATE abort (stop for good — finalise the run) apart from a process shutdown
// (a plain context cancel), which must instead leave the run resumable so the next fire continues it
// rather than redoing every repo into duplicate PRs.
var ErrRunAborted = errors.New("run aborted by kill-switch")

// defaultDrainTTL bounds how long a requested-but-not-executed restart keeps blocking new runs. In the
// normal path the process exits (draining) long before this — the TTL only fires if a restart was
// requested yet never happened (e.g. an aborted deploy), so admission self-heals instead of freezing
// forever. Requirement: the drain state is time-limited and must never permanently halt operations.
const defaultDrainTTL = 1 * time.Hour

// Executor runs a run end-to-end (across all target repos) and performs periodic maintenance
// (auto-merging overdue PRs). It is injected by the api layer so the runs package never imports it —
// the runs package owns scheduling + persistence, the api layer owns side effects.
type Executor interface {
	// Execute runs a run end-to-end. report is invoked as soon as the execution's result id is known
	// (freshly minted or resumed) so the scheduler can publish it as the live Activity — the UI locates
	// the in-flight result document by that id. The scheduler always passes a non-nil report; it may go
	// uncalled if the run fails before a result exists.
	Execute(ctx context.Context, run Run, report func(resultID string)) (ResultRef, error)
	Maintain(ctx context.Context)
}

// Activity is a snapshot of the run currently executing: its id, the live result id (once the executor
// reports it), and when this attempt started. Held in memory only — it exists exactly while a run's
// goroutine is alive, so it is the honest "is a run live right now" signal: a restart clears it because
// the goroutine dies with the process. The UI reads it (via the /active endpoint) to restore the
// "Lauf aktiv" state after a reload and to follow the live result.
type Activity struct {
	RunID     string    `json:"runId"`
	ResultID  string    `json:"resultId,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// StartOutcome is the result of trying to begin a run — the single, atomic admission verdict.
type StartOutcome int

const (
	StartFired    StartOutcome = iota // a run began (or will, detached in a goroutine)
	StartBusy                         // another run is already in progress
	StartDeferred                     // a restart is draining: no run may begin now; it was queued to start after the restart
)

// Scheduler fires due runs on a ticker, one at a time, and advances each run's schedule forward from
// now (so downtime never causes a storm of missed firings). It also drives run-now, cancel, and the
// restart handshake.
//
// Restart vs. run-start is a MUTUAL EXCLUSION, not a time window: whether a run may begin and whether a
// restart may proceed are decided at ONE place (tryClaim / RequestRestart), both under `mu`, so there is
// no instant at which a run starts while a restart is imminent. A pre-check whose result must still hold
// afterwards (the old marker-then-restart) is exactly what this replaces.
type Scheduler struct {
	store *Store
	exec  Executor
	tick  time.Duration
	logf  func(string, ...any)

	mu   sync.Mutex // guards every field below AND the admit-vs-restart decision (the single atomic place)
	cond *sync.Cond // broadcast when the in-flight run releases (curID clears) → wakes AwaitDrain

	curID       string
	curStop     context.CancelCauseFunc
	curActivity *Activity

	// Restart gate. Once restartRequested is set, tryClaim admits NO new run (a manual trigger is queued
	// instead), so "a run begins" and "the restart proceeds" can never both be true. Cleared only by the
	// process actually restarting (in-memory state dies) or, as a safety net, by the drain TTL.
	restartRequested bool
	restartAt        time.Time
	drainTTL         time.Duration
}

// NewScheduler builds a scheduler. tick defaults to 30s.
func NewScheduler(store *Store, exec Executor, tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	s := &Scheduler{store: store, exec: exec, tick: tick, logf: log.Printf, drainTTL: defaultDrainTTL}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Run blocks until ctx is done: each tick it auto-merges overdue PRs and fires any due runs.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	s.logf("devlabd: runs scheduler started (tick %s)", s.tick)
	for {
		select {
		case <-ctx.Done():
			s.logf("devlabd: runs scheduler stopped")
			return
		case <-t.C:
			s.maintain(ctx)
			s.fireDue(ctx)
		}
	}
}

// isDue reports whether a run should fire now. A pending explicit start (StartPending) overrides the
// schedule entirely — it is the queue that survives a restart (a manual trigger deferred by a draining
// restart, or a startup self-heal of an execution the restart interrupted), so a ToDo whose one-time
// DueAt was already consumed stays runnable instead of lying unfinished forever. Otherwise: a run
// suspended on the usage limit is due exactly when its window resets; a recurring auto run at its
// NextFireAt; a ToDo once at its optional DueAt (never again once done).
func isDue(r Run, now time.Time) bool {
	if r.StartPending {
		return true
	}
	if r.Suspended != nil {
		return !r.Suspended.ResumeAt.After(now)
	}
	if r.IsTodo() {
		return !r.Done && r.DueAt != nil && !r.DueAt.After(now)
	}
	return r.NextFireAt != nil && !r.NextFireAt.After(now)
}

// maintain runs the executor's periodic upkeep (auto-merge), isolated from panics — a bad PR check
// must never take devlabd down.
func (s *Scheduler) maintain(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("devlabd: runs maintain panicked (ignored): %v", r)
		}
	}()
	s.exec.Maintain(ctx)
}

func (s *Scheduler) fireDue(ctx context.Context) {
	all, err := s.store.List()
	if err != nil {
		s.logf("devlabd: runs scheduler list: %v", err)
		return
	}
	now := time.Now()
	for _, r := range all {
		if !r.Enabled || !isDue(r, now) {
			continue
		}
		rctx, cancel := context.WithCancelCause(ctx)
		switch s.tryClaim(r.ID, cancel) {
		case StartFired:
			func(id string) {
				defer s.release()
				defer cancel(nil)
				s.runOnce(rctx, id, "scheduler", true) // scheduled: advance the schedule
			}(r.ID)
		case StartDeferred:
			cancel(nil)
			return // a restart is draining — start no new run; due runs persist and fire after it
		default: // StartBusy
			cancel(nil)
			return // a run is already in progress; the rest stay due and are caught next tick
		}
	}
}

// FireNow triggers a run immediately (manual "Jetzt ausführen"), detached from the caller's request so
// the HTTP handler returns at once. StartBusy: a run is already in progress. StartDeferred: a restart is
// draining, so the run is NOT started (nothing may begin once a restart is imminent) but is queued to
// start by itself after the restart — the caller is told so, never silently dropped. A manual run does
// NOT advance the schedule (it is out of band).
func (s *Scheduler) FireNow(id, actor string) StartOutcome {
	// Register the run BEFORE returning: otherwise a Cancel arriving between this return and the
	// goroutine being scheduled would find curStop nil and silently do nothing.
	ctx, cancel := context.WithCancelCause(context.Background())
	switch s.tryClaim(id, cancel) {
	case StartFired:
		go func() {
			defer s.release()
			defer cancel(nil)
			s.runOnce(ctx, id, actor, false)
		}()
		return StartFired
	case StartDeferred:
		cancel(nil)
		s.queueStart(id)
		return StartDeferred
	default: // StartBusy
		cancel(nil)
		return StartBusy
	}
}

// tryClaim is the single atomic admission point: under mu it decides, in one indivisible step, whether
// this run may begin. It refuses (StartDeferred) if a restart is pending — so no run can start once the
// restart is imminent — refuses (StartBusy) if another run holds the slot, and otherwise claims the slot
// (publishing the live Activity + kill handle) and returns StartFired.
func (s *Scheduler) tryClaim(id string, cancel context.CancelCauseFunc) StartOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restartPendingLocked() {
		return StartDeferred
	}
	if s.curID != "" {
		return StartBusy
	}
	s.curID = id
	s.curStop = cancel
	s.curActivity = &Activity{RunID: id, StartedAt: time.Now().UTC()}
	return StartFired
}

// release relinquishes the run slot and wakes anyone draining (AwaitDrain).
func (s *Scheduler) release() {
	s.mu.Lock()
	s.curID, s.curStop, s.curActivity = "", nil, nil
	s.cond.Broadcast()
	s.mu.Unlock()
}

// queueStart persists StartPending so a run requested while a restart drained starts by itself
// afterwards — the request is never lost.
func (s *Scheduler) queueStart(id string) {
	if _, err := s.store.Patch(func(cur []Run) ([]Run, error) {
		for i := range cur {
			if cur[i].ID == id {
				cur[i].StartPending = true
			}
		}
		return cur, nil
	}); err != nil {
		s.logf("devlabd: run %s could not be queued for after-restart start: %v", id, err)
		return
	}
	s.logf("devlabd: run %s requested during restart drain — queued; it starts automatically after the restart", id)
}

// runOnce advances the schedule (if scheduled), executes the run, and attaches the result. No locking
// of the admission mutex here — the caller holds the run slot (curID), so the (possibly long) execution
// runs lock-free and RequestRestart/Active/AwaitDrain stay responsive throughout.
func (s *Scheduler) runOnce(ctx context.Context, id, actor string, advance bool) {
	// An autonomous run must never take the process down. Both call paths are goroutines (the ticker
	// and FireNow's worker), where an unrecovered panic in the injected Executor would crash ALL of
	// devlabd — not just this run. Contain it here.
	defer func() {
		if r := recover(); r != nil {
			s.logf("devlabd: run %s panicked (contained): %v", id, r)
		}
	}()

	run, ok, err := s.store.Get(id)
	if err != nil || !ok || !run.Enabled {
		return
	}
	now := time.Now()
	// A run suspended on the usage limit resumes the SAME execution: it must not advance its schedule
	// and must skip the freshness re-check (it is legitimately "due" via its ResumeAt).
	resuming := run.Suspended != nil
	// Re-verify freshness for a SCHEDULED fire: fireDue iterates a snapshot taken at tick start, and a
	// long-running first run makes it stale, so a run advanced or disabled meanwhile must not fire.
	// A manual run (advance=false) is an explicit override and skips this check.
	if advance && !resuming && !isDue(run, now) {
		return
	}
	// A queued start has now been honoured — clear StartPending so this request fires exactly once. Done
	// AFTER the freshness check (which relies on isDue seeing StartPending) and unconditionally (a resume
	// skips the advance patch below, so this must not be folded into it).
	if run.StartPending {
		_, _ = s.store.Patch(func(cur []Run) ([]Run, error) {
			for i := range cur {
				if cur[i].ID == id {
					cur[i].StartPending = false
				}
			}
			return cur, nil
		})
		run.StartPending = false
	}
	// Advance BEFORE executing (persisted) so a crash mid-run doesn't refire the same slot. Patch, not
	// Mutate — a schedule tick is not a user config change. A resume continues in place and skips this.
	if !resuming {
		_, _ = s.store.Patch(func(cur []Run) ([]Run, error) {
			for i := range cur {
				if cur[i].ID != id {
					continue
				}
				t := now
				cur[i].LastFiredAt = &t
				if cur[i].IsTodo() {
					// A ToDo fires once: drop the due date up front so a crash mid-run can't refire it.
					cur[i].DueAt = nil
				} else if advance {
					if nf, e := cur[i].Schedule.Next(now); e == nil {
						cur[i].NextFireAt = &nf
					}
				}
			}
			return cur, nil
		})
	}

	// Publish the live result id the instant the executor knows it, so /active can point the UI at the
	// in-flight result document (single source of truth: the scheduler owns "what is live").
	report := func(resultID string) {
		s.mu.Lock()
		if s.curActivity != nil {
			s.curActivity.ResultID = resultID
		}
		s.mu.Unlock()
	}
	ref, err := s.exec.Execute(ctx, run, report)
	if err != nil {
		s.logf("devlabd: run %s execution error: %v", id, err)
	}

	// Persist the outcome: either the execution paused on the usage limit (re-arm the suspension so it
	// resumes when the window resets), or it finished (clear any suspension and attach the result).
	_, _ = s.store.Patch(func(cur []Run) ([]Run, error) {
		for i := range cur {
			if cur[i].ID != id {
				continue
			}
			if ref.Suspended {
				attempts := 1
				if cur[i].Suspended != nil {
					attempts = cur[i].Suspended.Attempts + 1
				}
				resumeAt := now
				if ref.ResumeAt != nil {
					resumeAt = *ref.ResumeAt
				}
				cur[i].Suspended = &Suspension{ResumeAt: resumeAt, ResultID: ref.ResultID, Attempts: attempts, Reason: "usage-limit"}
				t := now
				cur[i].LastFiredAt = &t
				continue
			}
			cur[i].Suspended = nil
			if ref.ResultID != "" {
				r := ref
				cur[i].LastResult = &r
			}
			if cur[i].IsTodo() && ref.OK {
				cur[i].Done = true // a completed ToDo is checked off; a failed one stays open
			}
			// A resume that finished after a long suspension may have left NextFireAt in the past;
			// re-anchor it forward so it doesn't immediately catch-up fire.
			if resuming && !cur[i].IsTodo() {
				if nf, e := cur[i].Schedule.Next(now); e == nil {
					cur[i].NextFireAt = &nf
				}
			}
		}
		return cur, nil
	})
}

// RequestRestart marks that a restart is imminent: from now on tryClaim admits no new run. It returns
// the id of the run still in flight (or "" — meaning it is safe to restart immediately). Idempotent.
// This is one half of the mutual exclusion; tryClaim is the other, both under mu.
func (s *Scheduler) RequestRestart() (activeRunID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.restartRequested {
		s.restartRequested = true
		s.restartAt = time.Now()
	}
	if s.curID != "" {
		s.logf("devlabd: restart requested — no new run will start; draining active run %s", s.curID)
	} else {
		s.logf("devlabd: restart requested — no run active, safe to restart now")
	}
	return s.curID
}

// RestartPending reports whether a restart is currently draining (subject to the safety TTL). Read by
// the /active endpoint so the UI (and whoever tries to trigger a run) learns a restart is under way.
func (s *Scheduler) RestartPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartPendingLocked()
}

// restartPendingLocked answers "is a restart blocking admission right now", expiring a stale request
// past the drain TTL so an aborted restart can never freeze the scheduler forever. Caller holds mu.
func (s *Scheduler) restartPendingLocked() bool {
	if !s.restartRequested {
		return false
	}
	if s.drainTTL > 0 && time.Since(s.restartAt) > s.drainTTL {
		s.restartRequested = false
		s.logf("devlabd: restart request expired after %s with no restart — resuming normal run admission", s.drainTTL)
		return false
	}
	return true
}

// AwaitDrain blocks until no run is in flight (the restart is then safe) or ctx is done (the grace
// window elapsed). Returns true if it drained cleanly, false if the grace elapsed with a run still
// running — so a hanging run can never prevent the restart indefinitely (the caller then forces it).
func (s *Scheduler) AwaitDrain(ctx context.Context) bool {
	// Wake the cond when ctx fires so a blocked Wait re-checks the deadline.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		case <-stop:
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.curID != "" && ctx.Err() == nil {
		s.cond.Wait()
	}
	if s.curID != "" {
		s.logf("devlabd: drain grace elapsed with run %s still active — restarting anyway (it carries over and self-heals)", s.curID)
		return false
	}
	return true
}

// cancelCurrentForCarryOver plainly cancels the in-flight run (cause = context.Canceled, NOT
// ErrRunAborted) so the executor CARRIES IT OVER — leaves it resumable for the next start — rather than
// finalising it as a deliberate abort. Used only when the drain grace elapses.
func (s *Scheduler) cancelCurrentForCarryOver() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.curStop != nil {
		s.logf("devlabd: forcing in-flight run %s to carry over so the restart can proceed", s.curID)
		s.curStop(nil)
	}
}

// Shutdown is the graceful restart path: gate new runs (RequestRestart), wait for the in-flight run to
// finish (bounded by graceCtx), and if the grace elapses first, cancel it for carry-over and give it a
// brief moment to record its husk (so the resume after restart skips the repos already done). Because
// admission is gated the instant this returns, the caller can restart with no run able to sneak in.
func (s *Scheduler) Shutdown(graceCtx context.Context) {
	s.RequestRestart()
	if s.AwaitDrain(graceCtx) {
		return
	}
	s.cancelCurrentForCarryOver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.AwaitDrain(ctx)
}

// Reconcile is the startup self-heal (requirement: an execution interrupted by a restart resumes by
// itself after boot). An interrupted run left an unfinished husk on disk, but the run may no longer be
// "due" (a ToDo's one-time DueAt is consumed at its first fire; an auto run would not refire until its
// next schedule slot, ~24h out). Mark every such run StartPending so the next tick resumes it — the
// executor's resume then skips the repos already completed and re-attempts the half-finished one. A run
// carrying a live usage-limit suspension is left alone (it resumes via its own ResumeAt), and a disabled
// run is not woken. `within` bounds recency so a long-abandoned husk is not resurrected.
func (s *Scheduler) Reconcile(results *Results, within time.Duration) {
	if results == nil {
		return
	}
	all, err := s.store.List()
	if err != nil {
		s.logf("devlabd: startup self-heal list failed: %v", err)
		return
	}
	notBefore := time.Now().Add(-within)
	queued := map[string]bool{}
	for _, r := range all {
		if !r.Enabled || r.Suspended != nil || r.StartPending {
			continue
		}
		if _, ok := results.FindResumable(r.ID, notBefore); ok {
			queued[r.ID] = true
		}
	}
	if len(queued) == 0 {
		return
	}
	if _, err := s.store.Patch(func(cur []Run) ([]Run, error) {
		for i := range cur {
			if queued[cur[i].ID] {
				cur[i].StartPending = true
			}
		}
		return cur, nil
	}); err != nil {
		s.logf("devlabd: startup self-heal could not queue interrupted runs: %v", err)
		return
	}
	ids := make([]string, 0, len(queued))
	for id := range queued {
		ids = append(ids, id)
	}
	s.logf("devlabd: startup self-heal — %d interrupted run(s) queued to resume: %v", len(ids), ids)
}

// Cancel aborts the run currently in progress, if any (kill-switch).
func (s *Scheduler) Cancel() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.curStop == nil {
		return false
	}
	s.curStop(ErrRunAborted) // deliberate abort → the executor finalises rather than carrying over
	return true
}

// Current returns the id of the run in progress, or "".
func (s *Scheduler) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.curID
}

// Active returns a snapshot of the run currently executing (id, live result id, start), or nil when
// nothing runs. Copied out under the lock so callers can read it freely.
func (s *Scheduler) Active() *Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.curActivity == nil {
		return nil
	}
	a := *s.curActivity
	return &a
}
