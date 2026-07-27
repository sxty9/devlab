package runs

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	// (freshly minted or resumed) so the scheduler can publish it as this run's live Activity — the UI
	// locates the in-flight result document by that id. The scheduler always passes a non-nil report; it
	// may go uncalled if the run fails before a result exists.
	Execute(ctx context.Context, run Run, trigger Trigger, report func(resultID string)) (ResultRef, error)
	Maintain(ctx context.Context)
}

// Activity is a snapshot of one run currently executing: its id and name, the live result id (once the
// executor reports it), and when this attempt started. Held in memory only — it exists exactly while a
// run's goroutine is alive, so it is the honest "is this run live right now" signal: a restart clears it
// because the goroutine dies with the process. The UI reads the set of them (via the /active endpoint)
// to restore the "Lauf aktiv" state after a reload and to follow each live result.
type Activity struct {
	RunID     string    `json:"runId"`
	RunName   string    `json:"runName,omitempty"`
	ResultID  string    `json:"resultId,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// activeRun is the live bookkeeping for one in-flight run: its published Activity, its kill-switch, and
// the exclusivity / per-repo claims it holds so admission can keep two runs off the same repository.
type activeRun struct {
	activity  Activity
	cancel    context.CancelCauseFunc
	exclusive bool     // holds the whole-floor claim (an auto run sweeps every repo)
	claims    []string // the repository keys a ToDo occupies while it runs
}

// defaultMaxConcurrency is the conservative cap on simultaneous runs when DEVLAB_RUNS_MAX_CONCURRENCY is
// unset. It protects the subscription quota, CPU and memory: without a ceiling a burst of ToDos would
// launch a Claude CLI per run at once and exhaust the account and the host together. Two already doubles
// throughput over the old one-at-a-time scheduler while staying modest; raise it once headroom is known.
const defaultMaxConcurrency = 2

// Scheduler fires due runs on a ticker and advances each run's schedule forward from now (so downtime
// never causes a storm of missed firings). It runs several runs CONCURRENTLY up to a cap, keeps two runs
// off the same repository, and runs an auto (all-repos) run exclusively. It also drives run-now, cancel,
// and periodic maintenance.
type Scheduler struct {
	store *Store
	exec  Executor
	tick  time.Duration
	logf  func(string, ...any)

	maxConc int    // ceiling on simultaneous runs
	marker  string // on-disk active-run marker path ("" disables it)

	// mu guards the whole admission table: which runs are live, which repositories they have claimed,
	// and whether an exclusive (auto) run holds the floor. One lock keeps admission atomic — a run is
	// admitted (slot + claims reserved) or not, with no observable half state.
	mu            sync.Mutex
	active        map[string]*activeRun
	claimedRepos  map[string]bool
	exclusiveHeld bool
}

// NewScheduler builds a scheduler. tick defaults to 30s.
func NewScheduler(store *Store, exec Executor, tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	return &Scheduler{
		store: store, exec: exec, tick: tick, logf: log.Printf,
		maxConc:      maxConcurrency(),
		marker:       activeMarkerPath(),
		active:       map[string]*activeRun{},
		claimedRepos: map[string]bool{},
	}
}

// maxConcurrency reads the simultaneous-run ceiling from DEVLAB_RUNS_MAX_CONCURRENCY (a positive
// integer); anything else keeps the conservative default.
func maxConcurrency() int {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_MAX_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxConcurrency
}

// activeMarkerPath is where the deferred-restart marker lives — beside runs.json by default, overridable
// via DEVLAB_MERCURY_RUNS_ACTIVE_MARKER. Empty disables the marker.
func activeMarkerPath() string {
	if p := os.Getenv("DEVLAB_MERCURY_RUNS_ACTIVE_MARKER"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(runsPath()), "runs-active")
}

// Run blocks until ctx is done: each tick it auto-merges overdue PRs and fires any due runs.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	s.logf("devlabd: runs scheduler started (tick %s, max-concurrency %d)", s.tick, s.maxConc)
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

// fireDue starts every due, enabled run it can admit this tick. Admission is non-blocking: a run that
// cannot start right now (cap reached, an exclusive run holds the floor, or a target repo is busy) is
// left due and retried next tick — it never blocks the loop or occupies a slot while it waits.
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

// FireNow triggers a run immediately (manual "Jetzt ausführen"), detached from the caller's request so
// the HTTP handler returns at once. Returns false if the run cannot start right now — already running,
// the concurrency cap is reached, an exclusive run holds the floor, or one of its target repos is busy.
// A manual run does NOT advance the schedule (it is out of band).
func (s *Scheduler) FireNow(id, actor string) bool {
	run, ok, err := s.store.Get(id)
	if err != nil || !ok || !run.Enabled {
		return false
	}
	// Detached from any request context so the run outlives the HTTP handler.
	return s.tryStart(context.Background(), run, actor, false)
}

// tryStart admits a run under the concurrency + exclusivity rules and, if admitted, launches it in its
// own goroutine. Returns whether the run was started.
func (s *Scheduler) tryStart(parent context.Context, r Run, actor string, advance bool) bool {
	rctx, cancel := context.WithCancelCause(parent)
	if !s.admit(r, cancel) {
		cancel(nil)
		return false
	}
	go func() {
		defer s.release(r.ID)
		defer cancel(nil)
		s.runOnce(rctx, r.ID, actor, advance)
	}()
	return true
}

// admit atomically reserves a concurrency slot and the exclusivity / repo claims a run needs. The rules:
//   - never start the same run twice — a long run still in flight is skipped, not re-fired;
//   - never exceed the concurrency cap;
//   - an auto run is EXCLUSIVE: it sweeps every repo, so it starts only when nothing else runs, and
//     while it runs nothing else starts;
//   - a ToDo claims exactly its target repos: it starts only if none of them is already claimed, so two
//     runs never work the same repository at once.
//
// It returns false (reserving nothing) when the run cannot start now, so the caller leaves it due.
func (s *Scheduler) admit(r Run, cancel context.CancelCauseFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.active[r.ID]; ok {
		return false // already running — never double-start the same run
	}
	if len(s.active) >= s.maxConc {
		return false // cap reached
	}
	if s.exclusiveHeld {
		return false // an exclusive (auto) run holds the floor — nothing else starts
	}
	ar := &activeRun{cancel: cancel}
	if r.IsTodo() {
		keys := claimKeys(r)
		for _, k := range keys {
			if s.claimedRepos[k] {
				return false // a target repo is busy → defer this ToDo, don't block on it
			}
		}
		for _, k := range keys {
			s.claimedRepos[k] = true
		}
		ar.claims = keys
	} else {
		if len(s.active) > 0 {
			return false // auto is exclusive — wait until the floor is clear
		}
		s.exclusiveHeld = true
		ar.exclusive = true
	}
	ar.activity = Activity{RunID: r.ID, RunName: r.Name, StartedAt: time.Now().UTC()}
	s.active[r.ID] = ar
	s.writeMarkerLocked()
	return true
}

// release frees the slot, claims and exclusivity a run held once its goroutine ends.
func (s *Scheduler) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ar := s.active[id]
	if ar == nil {
		return
	}
	delete(s.active, id)
	if ar.exclusive {
		s.exclusiveHeld = false
	}
	for _, k := range ar.claims {
		delete(s.claimedRepos, k)
	}
	s.writeMarkerLocked()
}

// claimKeys is the set of repository keys a ToDo occupies while it runs — one per target, an existing
// repo or a to-be-created one, normalised so the same repo always maps to the same key. Admission uses
// it to keep two ToDos off the same repository; the per-repo workspace lock inside the executor remains
// the hard guarantee, this is the non-blocking admission heuristic in front of it.
func claimKeys(r Run) []string {
	var out []string
	for _, t := range r.TodoTargets() {
		k := t.Repo
		if k == "" {
			k = t.NewRepo
		}
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// runOnce advances the schedule (if scheduled), executes the run, and attaches the result. It runs in
// its own goroutine (one per active run); it touches shared scheduler state only through the mutexed
// store and the report closure, so several runOnce may run concurrently.
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

	// Publish the live result id the instant the executor knows it, so /active can point the UI at this
	// run's in-flight result document (single source of truth: the scheduler owns "what is live").
	report := func(resultID string) {
		s.mu.Lock()
		if ar := s.active[id]; ar != nil {
			ar.activity.ResultID = resultID
		}
		s.mu.Unlock()
	}
	ref, err := s.exec.Execute(ctx, run, triggerFor(actor, advance), report)
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

// Cancel aborts a specific in-flight run (kill-switch). Returns false if that run is not currently
// running. The abort targets exactly that run's context — the others keep going.
func (s *Scheduler) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ar := s.active[id]
	if ar == nil || ar.cancel == nil {
		return false
	}
	ar.cancel(ErrRunAborted) // deliberate abort → the executor finalises rather than carrying over
	return true
}

// Active returns a snapshot of every run currently executing (each with its id, name, live result id and
// start), oldest first. Copied out under the lock so callers can read the slice freely.
func (s *Scheduler) Active() []Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Activity, 0, len(s.active))
	for _, ar := range s.active {
		out = append(out, ar.activity)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].RunID < out[j].RunID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// ActiveCount is the number of runs executing right now — the counter behind the deferred restart: a
// deploy that would restart devlabd holds off while this is above zero (see writeMarkerLocked).
func (s *Scheduler) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// writeMarkerLocked keeps the on-disk active-run marker in step with the live count. It is the counter
// behind the DEFERRED RESTART: written when the first run starts and removed only when the LAST run
// ends, so a deploy that would restart devlabd can hold off while ANY run is still in flight. A marker
// that merely toggled would clear the instant the first of several concurrent runs finished — and a
// deploy would then restart the service out from under the ones still working. Best-effort: a marker IO
// error is logged, never allowed to disrupt scheduling. The caller holds s.mu.
func (s *Scheduler) writeMarkerLocked() {
	if s.marker == "" {
		return
	}
	n := len(s.active)
	if n == 0 {
		if err := os.Remove(s.marker); err != nil && !os.IsNotExist(err) {
			s.logf("devlabd: runs active-marker remove: %v", err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.marker), 0o700); err != nil {
		s.logf("devlabd: runs active-marker mkdir: %v", err)
		return
	}
	tmp := s.marker + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(n)+"\n"), 0o644); err != nil {
		s.logf("devlabd: runs active-marker write: %v", err)
		return
	}
	if err := os.Rename(tmp, s.marker); err != nil {
		s.logf("devlabd: runs active-marker rename: %v", err)
	}
}


// triggerFor turns the scheduler's own knowledge of a firing into the authorship stamp: a scheduled
// firing is autonomous, a manual one carries the person who asked for it.
func triggerFor(actor string, scheduled bool) Trigger {
	if scheduled || actor == "" || actor == "scheduler" {
		return Trigger{Auto: true}
	}
	return Trigger{By: actor}
}
