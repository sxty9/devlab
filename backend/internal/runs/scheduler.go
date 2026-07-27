package runs

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrRunAborted is the cancellation cause the kill-switch (Cancel) attaches to a run's context. It lets
// the executor tell a DELIBERATE abort (stop for good — finalise the run) apart from a process shutdown
// (a plain context cancel), which must instead leave the run resumable so the next fire continues it
// rather than redoing every repo into duplicate PRs.
var ErrRunAborted = errors.New("run aborted by kill-switch")

// defaultMaxConcurrent caps how many runs execute at once when the cap is not configured. Conservative:
// each run drives a Claude session across repos, so the ceiling protects the subscription quota, CPU and
// memory from a burst of ToDos firing together. Repos are still mutually exclusive on top of this, so two
// runs never touch the same workspace.
const defaultMaxConcurrent = 2

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
	// PlanResume decides — synchronously, before the run executes — whether the imminent execution will
	// continue an interrupted one or start fresh, and why (a ResumePlan). With fresh=true it additionally
	// DISCARDS any resumable execution (marking it as such) so the run deliberately starts over. The
	// scheduler consults it in FireNow so a manual trigger can report the decision to its caller; the
	// execution path re-derives the same decision, so the two never diverge.
	PlanResume(run Run, fresh bool) ResumePlan
}

// Activity is a snapshot of ONE executing run: its id/name, the live result id (once the executor reports
// it), and when this attempt started. Held in memory only — it exists exactly while a run's goroutine is
// alive, so it is the honest "is this run live right now" signal: a restart clears it because the
// goroutine dies with the process. The UI reads it (via /active) to restore state after a reload and to
// follow the live result. Exclusive/Overload mark how the run holds its slot (for the slot overview).
type Activity struct {
	RunID     string    `json:"runId"`
	RunName   string    `json:"runName,omitempty"`
	ResultID  string    `json:"resultId,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	Exclusive bool      `json:"exclusive,omitempty"`
	Overload  bool      `json:"overload,omitempty"`
}

// activeRun is the scheduler's live record of one executing run: its published Activity, the slot it
// reserved (repo claims, or the exclusive floor), whether it is an overload (a temporary extra slot that
// vanishes on release), and its kill-switch. The map of these IS the concurrency state — its size is the
// active-run count, its keys the busy runs.
type activeRun struct {
	activity  Activity
	claims    []string // repo-keys this run reserved (a ToDo/per-repo run)
	exclusive bool     // an auto run holding the whole floor
	overload  bool     // admitted past the cap for this one execution
	stop      context.CancelCauseFunc
}

// Scheduler fires due runs on a ticker and runs several at once — bounded by the concurrency cap, never
// two on the same repo, and (until Part D relaxes it) an auto run strictly alone. It also drives run-now
// and per-run cancel.
type Scheduler struct {
	store *Store
	exec  Executor
	tick  time.Duration
	logf  func(string, ...any)

	maxConc int // concurrency cap (0 → default)

	mu            sync.Mutex
	active        map[string]*activeRun // runID → its live state; len is the active-run marker
	claimedRepos  map[string]bool       // repo-key → a live run is working it
	exclusiveHeld bool                  // an auto run holds the whole floor
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
		store: store, exec: exec, tick: tick, logf: log.Printf, maxConc: maxConcurrent,
		active: map[string]*activeRun{}, claimedRepos: map[string]bool{},
	}
}

// Run blocks until ctx is done: each tick it auto-merges overdue PRs and fires any due runs.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	// A marker left behind by a killed predecessor would defer every deploy restart forever; at start-up
	// no run can be in flight, so the floor is provably empty.
	clearBusy()
	s.logf("devlabd: runs scheduler started (tick %s, max concurrent %d)", s.tick, s.maxConc)
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
// suspended (on the usage limit, or deferred to free a slot) overrides its schedule entirely — it is due
// exactly when its ResumeAt passes, and resumes the same execution rather than starting a new one.
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

// fireDue admits every due, enabled run it can — non-blocking. A run that cannot start right now (cap
// reached, a target repo busy, or the exclusive floor held) is simply left due and retried next tick; it
// holds no slot while it waits.
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
		s.tryStart(ctx, r, "scheduler", true) // scheduled: advance the schedule
	}
}

// tryStart reserves a slot for r and, if admitted, launches its execution goroutine. Returns whether it
// started. overload=false for the ordinary path (cap-bounded); FireNow/StartOverload pass their own.
func (s *Scheduler) tryStart(parent context.Context, r Run, actor string, advance bool) bool {
	ctx, cancel := context.WithCancelCause(parent)
	if !s.admit(r, cancel, false) {
		cancel(nil)
		return false
	}
	go s.execute(ctx, cancel, r.ID, actor, advance)
	return true
}

// execute is the goroutine body: run once, then release the slot no matter how it ends.
func (s *Scheduler) execute(ctx context.Context, cancel context.CancelCauseFunc, id, actor string, advance bool) {
	defer func() {
		cancel(nil)
		s.release(id)
	}()
	s.runOnce(ctx, id, actor, advance)
}

// admit reserves a slot for run r under the concurrency rules, returning true if reserved (the caller
// launches the goroutine) or false if it must stay due. overload=true bypasses ONLY the cap — never the
// per-repo claim or the exclusive floor: two runs never touch the same repo and an exclusive auto run
// stays alone, whatever the urgency.
func (s *Scheduler) admit(r Run, cancel context.CancelCauseFunc, overload bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.active[r.ID]; dup {
		return false // already executing — never double-start
	}
	if s.exclusiveHeld {
		return false // an auto run holds the whole floor — nothing else admits, not even an overload
	}
	ar := &activeRun{stop: cancel, overload: overload}
	if r.IsTodo() {
		keys := claimKeys(r)
		for _, k := range keys {
			if s.claimedRepos[k] {
				return false // a target repo is busy — the hard rule, uncrossable even by overload
			}
		}
		if !overload && len(s.active) >= s.maxConc {
			return false // at the cap (overload bypasses only this)
		}
		for _, k := range keys {
			s.claimedRepos[k] = true
		}
		ar.claims = keys
	} else {
		// An auto run sweeps every repo, so it runs alone: it needs an empty floor and blocks everything
		// while live. There is no "extra exclusive slot", so overload never applies to it.
		if overload || len(s.active) > 0 {
			return false
		}
		s.exclusiveHeld = true
		ar.exclusive = true
	}
	ar.activity = Activity{RunID: r.ID, RunName: r.Name, StartedAt: time.Now().UTC(), Exclusive: ar.exclusive, Overload: ar.overload}
	s.active[r.ID] = ar
	markActive(len(s.active))
	return true
}

// release frees a run's slot (its repo claims / the exclusive floor) and updates the active-run marker.
func (s *Scheduler) release(id string) {
	s.mu.Lock()
	if ar := s.active[id]; ar != nil {
		for _, k := range ar.claims {
			delete(s.claimedRepos, k)
		}
		if ar.exclusive {
			s.exclusiveHeld = false
		}
	}
	delete(s.active, id)
	n := len(s.active)
	s.mu.Unlock()
	markActive(n)
}

// claimKeys is the set of repo-keys a ToDo reserves: one per target (an existing repo, or a to-be-created
// one). Keying on the normalised target string matches how the executor resolves targets; the per-repo
// workspace.Lock is the ultimate backstop, so this claim is a fast heuristic, not the sole guarantee.
func claimKeys(r Run) []string {
	var keys []string
	for _, t := range r.TodoTargets() {
		switch {
		case t.Repo != "":
			keys = append(keys, "repo:"+strings.ToLower(strings.TrimSpace(t.Repo)))
		case t.NewRepo != "":
			keys = append(keys, "new:"+strings.ToLower(strings.TrimSpace(t.NewRepo)))
		}
	}
	return keys
}

// reportResult publishes the live result id on a run's Activity the instant the executor mints it.
func (s *Scheduler) reportResult(id, resultID string) {
	s.mu.Lock()
	if ar := s.active[id]; ar != nil {
		ar.activity.ResultID = resultID
	}
	s.mu.Unlock()
}

// FireNow triggers a run immediately (manual "Jetzt ausführen"), detached from the caller's request so
// the HTTP handler returns at once. It reserves a slot first; only if admitted does it consult the
// executor for what the imminent execution will do — continue an interrupted execution or start fresh —
// and return that ResumePlan so the trigger can report the difference to whoever pressed the button.
// fresh=true forces a fresh start, discarding any resumable execution. ok=false when the run cannot start
// right now (already running, cap reached, a target repo busy, or the exclusive floor held).
func (s *Scheduler) FireNow(id, actor string, fresh bool) (plan ResumePlan, ok bool) {
	return s.start(id, actor, fresh, false)
}

// start reserves a slot (optionally as an overload) and launches the run, returning the resume plan. The
// plan is computed only AFTER admission succeeds, so it is exactly what the detached execution will enact.
func (s *Scheduler) start(id, actor string, fresh, overload bool) (ResumePlan, bool) {
	run, got, err := s.store.Get(id)
	if err != nil || !got || !run.Enabled {
		return ResumePlan{}, false
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	if !s.admit(run, cancel, overload) {
		cancel(nil)
		return ResumePlan{}, false
	}
	plan := s.exec.PlanResume(run, fresh)
	go s.execute(ctx, cancel, id, actor, false)
	return plan, true
}

// runOnce advances the schedule (if scheduled), executes the run, and attaches the result. Concurrency is
// handled by the caller's admit/release — this only owns the one run.
func (s *Scheduler) runOnce(ctx context.Context, id, actor string, advance bool) {
	// An autonomous run must never take the process down. Both call paths are goroutines, where an
	// unrecovered panic in the injected Executor would crash ALL of devlabd — contain it here.
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
	// A suspended run (usage-limit or deferred) resumes the SAME execution: it must not advance its
	// schedule and must skip the freshness re-check (it is legitimately "due" via its ResumeAt).
	resuming := run.Suspended != nil
	// Re-verify freshness for a SCHEDULED fire: fireDue iterates a snapshot taken at tick start, and a
	// long-running first run makes it stale, so a run advanced or disabled meanwhile must not fire. A
	// manual run (advance=false) is an explicit override and skips this check.
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

	report := func(resultID string) { s.reportResult(id, resultID) }
	ref, err := s.exec.Execute(ctx, run, report)
	if err != nil {
		s.logf("devlabd: run %s execution error: %v", id, err)
	}

	// Persist the outcome: either the execution paused (re-arm the suspension so it resumes), or it
	// finished (clear any suspension and attach the result).
	_, _ = s.store.Patch(func(cur []Run) ([]Run, error) {
		for i := range cur {
			if cur[i].ID != id {
				continue
			}
			if ref.Suspended {
				reason := ref.Reason
				if reason == "" {
					reason = ReasonUsageLimit // "" reads as usage-limit for back-compat
				}
				// A defer must NOT consume the usage-limit give-up budget: only a real usage-limit pause counts.
				attempts := 0
				if cur[i].Suspended != nil {
					attempts = cur[i].Suspended.Attempts
				}
				if reason == ReasonUsageLimit {
					attempts++
				}
				resumeAt := now
				if ref.ResumeAt != nil {
					resumeAt = *ref.ResumeAt
				}
				cur[i].Suspended = &Suspension{ResumeAt: resumeAt, ResultID: ref.ResultID, Attempts: attempts, Reason: reason}
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

// Cancel aborts a specific run in progress (kill-switch). Returns false if that run is not executing.
// The abort targets exactly the named run — other concurrent runs keep going.
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

// Active returns a snapshot of EVERY run currently executing, ordered by start time so the UI list is
// stable. Copied out under the lock so callers read freely.
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

// ActiveCount is the active-run marker as a number: the count of runs executing right now. It rises from 0
// to 1 when the first run begins and falls to 0 only when the last one ends.
func (s *Scheduler) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// RunsActive reports whether any run is executing right now (the marker as a boolean view of the count).
func (s *Scheduler) RunsActive() bool { return s.ActiveCount() > 0 }

// Capacity is the current concurrency cap (the standing number of slots).
func (s *Scheduler) Capacity() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxConc
}
