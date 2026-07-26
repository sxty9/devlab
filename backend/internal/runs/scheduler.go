package runs

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"
)

// ErrRunAborted is the cancellation cause the kill-switch (Cancel) attaches to a run's context. It lets
// the executor tell a DELIBERATE abort (stop for good — finalise the run) apart from a process shutdown
// (a plain context cancel), which must instead leave the run resumable so the next fire continues it
// rather than redoing every repo into duplicate PRs.
var ErrRunAborted = errors.New("run aborted by kill-switch")

// defaultMaxConcurrent caps how many runs execute at once when DEVLAB_RUNS_MAX_CONCURRENT is unset. A
// conservative default: runs are expensive (each drives a Claude session across repos), so the ceiling
// protects the subscription quota, CPU and memory from a burst of ToDos all firing together. Repos are
// still mutually exclusive on top of this (see footprint), so two runs never touch the same workspace.
const defaultMaxConcurrent = 2

// Executor runs a run end-to-end (across all target repos) and performs periodic maintenance
// (auto-merging overdue PRs). It is injected by the api layer so the runs package never imports it —
// the runs package owns scheduling + persistence, the api layer owns side effects.
type Executor interface {
	// Execute runs a run end-to-end. report is invoked as soon as the execution's result id is known
	// (freshly minted or resumed) so the scheduler can publish it on the run's live Activity — the UI
	// locates the in-flight result document by that id. The scheduler always passes a non-nil report; it
	// may go uncalled if the run fails before a result exists.
	Execute(ctx context.Context, run Run, report func(resultID string)) (ResultRef, error)
	Maintain(ctx context.Context)
}

// Activity is a snapshot of ONE executing run: its id, the live result id (once the executor reports it),
// and when this attempt started. Held in memory only — it exists exactly while a run's goroutine is
// alive, so it is the honest "is this run live right now" signal: a restart clears it because the
// goroutine dies with the process. The UI reads the set of them (via the /active endpoint) to restore
// the "Läufe aktiv" state after a reload and to follow each live result.
type Activity struct {
	RunID     string    `json:"runId"`
	ResultID  string    `json:"resultId,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// footprint is a run's claim on the box: the set of repos it will touch, or exclusive for an auto run
// (which sweeps EVERY Holistic repo and therefore must run alone). The scheduler reserves a footprint
// before launching a run and only starts a run whose footprint does not collide with any already active
// one — so two runs never work the same repo, and an auto run never overlaps anything.
type footprint struct {
	exclusive bool                // auto run: conflicts with every other run
	repos     map[string]struct{} // todo: the target-repo keys it will touch
}

// footprintFor derives a run's footprint from its model alone (no network): an auto run is exclusive; a
// ToDo claims exactly its declared targets. Keying on the target string (repo id/name, or new-repo name)
// matches how the executor resolves targets; the per-repo workspace lock is the ultimate backstop, so an
// imperfect key can at worst make one repo serialize when it need not, never let two runs collide.
func footprintFor(r Run) footprint {
	if !r.IsTodo() {
		return footprint{exclusive: true}
	}
	repos := map[string]struct{}{}
	for _, t := range r.TodoTargets() {
		switch {
		case t.Repo != "":
			repos["repo:"+t.Repo] = struct{}{}
		case t.NewRepo != "":
			repos["new:"+t.NewRepo] = struct{}{}
		}
	}
	return footprint{repos: repos}
}

// activeRun is the scheduler's live record of one executing run: its published Activity, the footprint it
// reserved, and its kill-switch. The map of these IS the concurrency state — its size is the active-run
// count (the marker: set when the first run begins, cleared when the last ends), its keys the busy runs,
// its footprints the busy repos.
type activeRun struct {
	activity  Activity
	footprint footprint
	stop      context.CancelCauseFunc
}

// Scheduler fires due runs on a ticker and runs several at once — bounded by maxConcurrent, never two on
// the same repo, and an auto run strictly alone. A run whose repos are busy (or that cannot fit under the
// ceiling) is left due and retried on a later tick: it is DEFERRED, never blocked, so a waiting run holds
// no concurrency slot. The scheduler advances each fired run's schedule forward from now (so downtime
// never causes a storm of missed firings) and drives run-now and per-run cancel.
type Scheduler struct {
	store         *Store
	exec          Executor
	tick          time.Duration
	maxConcurrent int
	logf          func(string, ...any)

	mu     sync.Mutex
	active map[string]*activeRun // runID → its live state; len is the active-run marker
}

// NewScheduler builds a scheduler. tick defaults to 30s; maxConcurrent to defaultMaxConcurrent.
func NewScheduler(store *Store, exec Executor, tick time.Duration, maxConcurrent int) *Scheduler {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrent
	}
	return &Scheduler{
		store:         store,
		exec:          exec,
		tick:          tick,
		maxConcurrent: maxConcurrent,
		logf:          log.Printf,
		active:        map[string]*activeRun{},
	}
}

// Run blocks until ctx is done: each tick it auto-merges overdue PRs and fires any due runs.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	s.logf("devlabd: runs scheduler started (tick %s, max concurrent %d)", s.tick, s.maxConcurrent)
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
		// tryStart is non-blocking: it launches the run if a slot is free and its repos are idle, else it
		// returns false and the run stays due — caught on a later tick. A deferred run holds no slot.
		s.tryStart(ctx, r.ID, "scheduler", true)
	}
}

// FireNow triggers a run immediately (manual "Jetzt ausführen"), detached from the caller's request
// so the HTTP handler returns at once. Returns false if the run cannot start right now — already running,
// the concurrency ceiling is reached, or its target repos are busy (an auto run: any other run active). A
// manual run does NOT advance the schedule (it is out of band).
func (s *Scheduler) FireNow(id, actor string) bool {
	return s.tryStart(context.Background(), id, actor, false)
}

// tryStart reserves a run's footprint and launches it in its own goroutine, or returns false if it may
// not start now (duplicate, over the ceiling, or a colliding footprint). Reserving BEFORE the goroutine
// is scheduled means a Cancel arriving immediately after cannot miss the run's kill-switch.
func (s *Scheduler) tryStart(ctx context.Context, id, actor string, advance bool) bool {
	run, ok, err := s.store.Get(id)
	if err != nil || !ok {
		return false
	}
	fp := footprintFor(run)

	s.mu.Lock()
	if _, dup := s.active[id]; dup {
		s.mu.Unlock()
		return false // this run is already executing
	}
	if len(s.active) >= s.maxConcurrent {
		s.mu.Unlock()
		return false // at the concurrency ceiling — deferred to a later tick
	}
	if s.conflictsLocked(fp) {
		s.mu.Unlock()
		return false // its repos are busy, or an exclusive run holds the box — deferred
	}
	rctx, cancel := context.WithCancelCause(ctx)
	s.active[id] = &activeRun{
		activity:  Activity{RunID: id, StartedAt: time.Now().UTC()},
		footprint: fp,
		stop:      cancel,
	}
	s.mu.Unlock()

	go func() {
		defer func() {
			cancel(nil)
			s.release(id)
		}()
		s.runOnce(rctx, id, actor, advance)
	}()
	return true
}

// conflictsLocked reports whether a footprint may NOT start given the current reservations. An auto run
// (exclusive) collides with any active run, and any active exclusive run collides with everything; two
// ToDos collide iff they share a target repo. The caller holds s.mu.
func (s *Scheduler) conflictsLocked(fp footprint) bool {
	for _, ar := range s.active {
		if fp.exclusive || ar.footprint.exclusive {
			return true
		}
		for k := range fp.repos {
			if _, busy := ar.footprint.repos[k]; busy {
				return true
			}
		}
	}
	return false
}

func (s *Scheduler) release(id string) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}

// reportResult publishes the live result id on the run's Activity the instant the executor mints it.
func (s *Scheduler) reportResult(id, resultID string) {
	s.mu.Lock()
	if ar := s.active[id]; ar != nil {
		ar.activity.ResultID = resultID
	}
	s.mu.Unlock()
}

// runOnce advances the schedule (if scheduled), executes the run, and attaches the result. No locking of
// the scheduler here — the run already holds its reservation for the duration of this call.
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
	// concurrent run advancing meanwhile could make it stale, so a run advanced or disabled meanwhile
	// must not fire. A manual run (advance=false) is an explicit override and skips this check.
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

	ref, err := s.exec.Execute(ctx, run, func(resultID string) { s.reportResult(id, resultID) })
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

// Cancel aborts a specific run in progress (kill-switch). Returns false if that run is not currently
// executing. The abort targets exactly the named run — other concurrent runs keep going.
func (s *Scheduler) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ar := s.active[id]
	if ar == nil || ar.stop == nil {
		return false
	}
	ar.stop(ErrRunAborted) // deliberate abort → the executor finalises rather than carrying over
	return true
}

// Active returns a snapshot of EVERY run currently executing (each with its id, live result id and start),
// ordered by start time so the UI list is stable. Copied out under the lock so callers can read freely.
func (s *Scheduler) Active() []Activity {
	s.mu.Lock()
	out := make([]Activity, 0, len(s.active))
	for _, ar := range s.active {
		out = append(out, ar.activity)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].RunID < out[j].RunID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// ActiveCount is the active-run marker as a number: how many runs execute right now. It rises from 0 to 1
// when the first run begins and falls to 0 only when the last one ends — so a caller that must hold off a
// disruptive action (e.g. a self-deploy that would restart devlabd and kill every in-flight run) waits on
// the COUNT, never on a single boolean that would clear while a second run is still working.
func (s *Scheduler) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// RunsActive reports whether any run is executing right now (the marker as a boolean view of the count).
func (s *Scheduler) RunsActive() bool {
	return s.ActiveCount() > 0
}
