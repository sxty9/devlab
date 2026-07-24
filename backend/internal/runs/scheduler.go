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

// Scheduler fires due runs on a ticker, one at a time, and advances each run's schedule forward from
// now (so downtime never causes a storm of missed firings). It also drives run-now and cancel.
type Scheduler struct {
	store *Store
	exec  Executor
	tick  time.Duration
	logf  func(string, ...any)

	runMu sync.Mutex // held for the duration of ONE run — guarantees one run at a time

	curMu       sync.Mutex
	curID       string
	curStop     context.CancelCauseFunc
	curActivity *Activity
}

// NewScheduler builds a scheduler. tick defaults to 30s.
func NewScheduler(store *Store, exec Executor, tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	return &Scheduler{store: store, exec: exec, tick: tick, logf: log.Printf}
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

// isDue reports whether a run should fire now: a recurring auto run at its NextFireAt, a ToDo once at
// its optional DueAt (never again once done; a ToDo without a DueAt only ever runs manually). A run
// suspended on the usage limit overrides its schedule entirely — it is due exactly when the window
// resets, and resumes the same execution rather than starting a new one.
func isDue(r Run, now time.Time) bool {
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
		if !s.runMu.TryLock() {
			return // a run is already in progress; the rest stay due and are caught next tick
		}
		func(id string) {
			defer s.runMu.Unlock()
			rctx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			s.setCurrent(id, cancel)
			defer s.clearCurrent()
			s.runOnce(rctx, id, "scheduler", true) // scheduled: advance the schedule
		}(r.ID)
	}
}

// FireNow triggers a run immediately (manual "Jetzt ausführen"), detached from the caller's request
// so the HTTP handler returns at once. Returns false if a run is already in progress. A manual run
// does NOT advance the schedule (it is out of band).
func (s *Scheduler) FireNow(id, actor string) bool {
	if !s.runMu.TryLock() {
		return false
	}
	// Register the run BEFORE returning: otherwise a Cancel arriving between this return and the
	// goroutine being scheduled would find curStop nil and silently do nothing.
	ctx, cancel := context.WithCancelCause(context.Background())
	s.setCurrent(id, cancel)
	go func() {
		defer s.runMu.Unlock()
		defer cancel(nil)
		defer s.clearCurrent()
		s.runOnce(ctx, id, actor, false)
	}()
	return true
}

// runOnce advances the schedule (if scheduled), executes the run, and attaches the result. No locking
// here — the caller holds runMu.
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
		s.curMu.Lock()
		if s.curActivity != nil {
			s.curActivity.ResultID = resultID
		}
		s.curMu.Unlock()
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

// Cancel aborts the run currently in progress, if any (kill-switch).
func (s *Scheduler) Cancel() bool {
	s.curMu.Lock()
	defer s.curMu.Unlock()
	if s.curStop == nil {
		return false
	}
	s.curStop(ErrRunAborted) // deliberate abort → the executor finalises rather than carrying over
	return true
}

// Current returns the id of the run in progress, or "".
func (s *Scheduler) Current() string {
	s.curMu.Lock()
	defer s.curMu.Unlock()
	return s.curID
}

// Active returns a snapshot of the run currently executing (id, live result id, start), or nil when
// nothing runs. Copied out under the lock so callers can read it freely.
func (s *Scheduler) Active() *Activity {
	s.curMu.Lock()
	defer s.curMu.Unlock()
	if s.curActivity == nil {
		return nil
	}
	a := *s.curActivity
	return &a
}

func (s *Scheduler) setCurrent(id string, cancel context.CancelCauseFunc) {
	s.curMu.Lock()
	s.curID, s.curStop = id, cancel
	s.curActivity = &Activity{RunID: id, StartedAt: time.Now().UTC()}
	s.curMu.Unlock()
}

func (s *Scheduler) clearCurrent() {
	s.curMu.Lock()
	s.curID, s.curStop, s.curActivity = "", nil, nil
	s.curMu.Unlock()
}
