package runs

import (
	"context"
	"errors"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/live"
)

// ErrRunAborted is the cancellation cause the kill-switch (Cancel) attaches to a run's context. It lets
// the executor tell a DELIBERATE abort (stop for good — finalise the run) apart from a process shutdown
// (a plain context cancel), which must instead leave the run resumable so the next fire continues it
// rather than redoing every repo into duplicate PRs.
var ErrRunAborted = errors.New("run aborted by kill-switch")

// ErrRunDeferred is the cancellation cause Defer attaches to a run's context. It tells the executor to
// stand down WITHOUT finalising and WITHOUT falling back to a plain shutdown carry-over: it re-arms the
// run as a deferred suspension (the same pause concept as the usage limit) so it gives up its slot,
// keeps every completed repo, and resumes the SAME execution at the next free slot. Distinct from
// ErrRunAborted (stop for good) and from a plain context cancel (process shutdown).
var ErrRunDeferred = errors.New("run deferred to free a slot")

// Executor runs a run end-to-end (across all target repos) and performs periodic maintenance
// (auto-merging overdue PRs). It is injected by the api layer so the runs package never imports it —
// the runs package owns scheduling + persistence, the api layer owns side effects.
type Executor interface {
	// Execute runs a run end-to-end. report is invoked as soon as the execution's result id is known
	// (freshly minted or resumed) so the scheduler can publish it as this run's live Activity — the UI
	// locates the in-flight result document by that id. The scheduler always passes a non-nil report; it
	// may go uncalled if the run fails before a result exists.
	Execute(ctx context.Context, run Run, report func(resultID string)) (ResultRef, error)
	Maintain(ctx context.Context)
	// PlanResume decides — synchronously, before the run executes — whether the imminent execution will
	// continue an interrupted one or start fresh, and why (a ResumePlan). With fresh=true it additionally
	// DISCARDS any resumable execution (marking it as such) so the run deliberately starts over. The
	// scheduler consults it when a manual trigger starts a run so the trigger can report the decision to
	// its caller; the execution path re-derives the same decision, so the two never diverge.
	PlanResume(run Run, fresh bool) ResumePlan
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
	// Exclusive is set for an auto (all-repos) run — it holds the whole floor. Overload is set for a run
	// admitted in a temporary extra slot BEYOND the cap; the extra slot vanishes when the run ends. Both
	// surface so the overview can portion "belegt / frei / Überladung" (task point 8).
	Exclusive bool `json:"exclusive,omitempty"`
	Overload  bool `json:"overload,omitempty"`
}

// activeRun is the live bookkeeping for one in-flight run: its published Activity, its kill-switch, and
// the exclusivity / per-repo claims it holds so admission can keep two runs off the same repository.
type activeRun struct {
	activity  Activity
	cancel    context.CancelCauseFunc
	exclusive bool     // holds the whole-floor claim (an auto run sweeps every repo)
	overload  bool     // admitted in a temporary extra slot beyond the cap — self-heals on release
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
// defer, overload, and periodic maintenance.
type Scheduler struct {
	store *Store
	exec  Executor
	tick  time.Duration
	logf  func(string, ...any)

	maxConc int // ceiling on simultaneous runs

	// mu guards the whole admission table: which runs are live, which repositories they have claimed,
	// and whether an exclusive (auto) run holds the floor. One lock keeps admission atomic — a run is
	// admitted (slot + claims reserved) or not, with no observable half state.
	mu            sync.Mutex
	active        map[string]*activeRun
	claimedRepos  map[string]bool
	exclusiveHeld bool

	// pub broadcasts a "live-run set changed" signal on start / result-id known / end, so open UIs track
	// the running runs without a resting poll. Optional (nil = no-op). Set by SetPublisher.
	pub *live.Broker
}

// SetPublisher wires the live-change broker so the scheduler notifies open UIs when a run starts, gets
// its result id, or ends. Nil is allowed (publishing off).
func (s *Scheduler) SetPublisher(b *live.Broker) { s.pub = b }

// NewScheduler builds a scheduler. tick defaults to 30s.
func NewScheduler(store *Store, exec Executor, tick time.Duration) *Scheduler {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	return &Scheduler{
		store: store, exec: exec, tick: tick, logf: log.Printf,
		maxConc:      maxConcurrency(),
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

// Run blocks until ctx is done: each tick it auto-merges overdue PRs and fires any due runs.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	// A marker left behind by a killed predecessor would defer every deploy restart forever; at start-up
	// no run can be in flight, so the slot is provably free.
	clearBusy()
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
// suspended (usage limit OR a deliberate defer) overrides its schedule entirely — it is due at its
// ResumeAt (now, for a defer), and resumes the same execution rather than starting a new one.
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
		rctx, cancel := context.WithCancelCause(ctx)
		if !s.admit(r, cancel, false) {
			cancel(nil)
			continue // leave it due; caught next tick when a slot frees
		}
		s.launch(rctx, cancel, r.ID, "scheduler", true) // scheduled: advance the schedule
	}
}

// FireNow triggers a run immediately (manual "Jetzt ausführen"), detached from the caller's request so
// the HTTP handler returns at once. It first consults the executor for what the imminent execution will
// do — continue an interrupted execution or start fresh — and returns that ResumePlan so the trigger can
// report the difference. fresh=true forces a fresh start (the executor discards the resumable husk).
// ok=false when the run cannot start right now — already running, the cap is reached, an exclusive run
// holds the floor, or a target repo is busy — and NOTHING is decided or discarded in that case (so a
// blocked fresh start does not silently throw away the husk it never replaced). A manual run does NOT
// advance the schedule (it is out of band).
func (s *Scheduler) FireNow(id, actor string, fresh bool) (ResumePlan, bool) {
	return s.startManual(id, actor, fresh, false)
}

// StartOverload starts a run in a TEMPORARY extra slot beyond the concurrency cap (task point 5:
// "überladen"). It is the ONLY admission path that ignores the cap — and only the cap: it still refuses
// to put two runs on the same repository or to run alongside an exclusive auto run (task point 7, the
// limits overload never crosses). The extra slot is not persisted anywhere and disappears the instant
// this run ends (task point 5), so repeated overloads can never quietly raise the standing ceiling.
// Returns the ResumePlan and true on start; ({}, false) if it still cannot start.
func (s *Scheduler) StartOverload(id, actor string, fresh bool) (ResumePlan, bool) {
	return s.startManual(id, actor, fresh, true)
}

// startManual admits a run (optionally past the cap, for an overload) and, if admitted, previews the
// resume decision and launches it. The plan is computed AFTER admission so a blocked start neither
// decides nor discards anything.
func (s *Scheduler) startManual(id, actor string, fresh, overload bool) (ResumePlan, bool) {
	run, ok, err := s.store.Get(id)
	if err != nil || !ok || !run.Enabled {
		return ResumePlan{}, false
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	if !s.admit(run, cancel, overload) {
		cancel(nil)
		return ResumePlan{}, false
	}
	plan := s.exec.PlanResume(run, fresh) // preview + discard-on-fresh; the execution path re-derives it
	s.launch(ctx, cancel, run.ID, actor, false)
	return plan, true
}

// DiscardResumable discards a run's resumable husk so its NEXT fire starts over — the fresh-start intent
// preserved when a fresh start is QUEUED (no slot free now) rather than started immediately, so the
// intent survives to the actual fire. Returns the resulting plan (naming what it discarded).
func (s *Scheduler) DiscardResumable(id string) (ResumePlan, bool) {
	run, ok, err := s.store.Get(id)
	if err != nil || !ok {
		return ResumePlan{}, false
	}
	return s.exec.PlanResume(run, true), true
}

// launch runs an admitted run in its own goroutine and releases its slot/claims when it ends.
func (s *Scheduler) launch(ctx context.Context, cancel context.CancelCauseFunc, id, actor string, advance bool) {
	go func() {
		defer s.release(id)
		defer cancel(nil)
		s.runOnce(ctx, id, actor, advance)
	}()
}

// Defer stands a live run down to free its slot (task point 4): it cancels that run with ErrRunDeferred,
// which the executor turns into a deferred SUSPENSION — the run keeps every completed repo and resumes
// the same execution at the next free slot, redoing only the repo it was mid-way through. Returns false
// if that run is not currently running. An explicit user choice; the automatic suggestion is computed a
// layer up (it needs per-run progress from the results pool).
func (s *Scheduler) Defer(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ar := s.active[id]
	if ar == nil || ar.cancel == nil {
		return false
	}
	ar.cancel(ErrRunDeferred)
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
//
// overload lifts ONLY the cap check (task point 5) — the two limits above urgency (task point 7) always
// hold: an overload never joins an exclusive floor and never lands on a claimed repository. An auto run
// is inherently exclusive, so it never overloads (it needs an empty floor regardless).
func (s *Scheduler) admit(r Run, cancel context.CancelCauseFunc, overload bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.active[r.ID]; ok {
		return false // already running — never double-start the same run
	}
	if RestartPending() {
		return false // a devlabd restart is queued and waiting for the slot — don't start a new run into it
	}
	if !overload && len(s.active) >= s.maxConc {
		return false // cap reached (overload takes a temporary extra slot past it)
	}
	if s.exclusiveHeld {
		return false // an exclusive (auto) run holds the floor — nothing else starts, not even an overload
	}
	ar := &activeRun{cancel: cancel, overload: overload}
	if r.IsTodo() {
		keys := claimKeys(r)
		for _, k := range keys {
			if s.claimedRepos[k] {
				return false // a target repo is busy → defer this ToDo, don't block on it (overload can't cross this)
			}
		}
		for _, k := range keys {
			s.claimedRepos[k] = true
		}
		ar.claims = keys
	} else {
		if overload {
			return false // an auto run is exclusive by nature — there is no "extra exclusive slot" to grant
		}
		if len(s.active) > 0 {
			return false // auto is exclusive — wait until the floor is clear
		}
		s.exclusiveHeld = true
		ar.exclusive = true
	}
	ar.activity = Activity{RunID: r.ID, RunName: r.Name, StartedAt: time.Now().UTC(), Exclusive: ar.exclusive, Overload: ar.overload}
	s.active[r.ID] = ar
	markActive(len(s.active))
	s.pub.Publish(live.TopicActive) // a run just went live
	return true
}

// AdmitBlock classifies WHY a run cannot start right now, so the start-decision layer can offer the right
// choices (task point 5) and target the right suggestion (task point 6). Reason is "" when the run would
// admit immediately; otherwise one of the constants below. Conflicts lists the live run ids that must
// stand down to unblock this one — the exclusive holder, or the runs occupying a needed repository. For a
// plain cap block any run frees a slot, so Conflicts is empty there.
type AdmitBlock struct {
	Reason    string
	Conflicts []string
}

// Admission block reasons.
const (
	AdmitRunning        = "running"         // this run is already live
	AdmitRestartPending = "restart-pending" // a devlabd restart is queued — new starts are held (mutual exclusion)
	AdmitExclusive      = "exclusive"       // an auto run holds the whole floor
	AdmitRepoBusy       = "repo-busy"       // a target repository is occupied by another run
	AdmitCap            = "cap"             // every slot is taken (but no repo/exclusivity conflict)
)

// Admissibility reports whether r could start now and, if not, why — WITHOUT reserving anything. It
// mirrors admit's checks in the same priority order. The repo-busy and exclusive blocks stand above the
// cap: they cannot be overloaded past (task point 7), so they are reported even when the cap also blocks.
func (s *Scheduler) Admissibility(r Run) AdmitBlock {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.active[r.ID]; ok {
		return AdmitBlock{Reason: AdmitRunning}
	}
	if RestartPending() {
		return AdmitBlock{Reason: AdmitRestartPending}
	}
	if s.exclusiveHeld {
		var holder []string
		for id, ar := range s.active {
			if ar.exclusive {
				holder = append(holder, id)
			}
		}
		sort.Strings(holder)
		return AdmitBlock{Reason: AdmitExclusive, Conflicts: holder}
	}
	if r.IsTodo() {
		if busy := s.repoConflicts(claimKeys(r)); len(busy) > 0 {
			return AdmitBlock{Reason: AdmitRepoBusy, Conflicts: busy}
		}
	} else if len(s.active) > 0 {
		// A fresh auto run needs an empty floor; every live run stands between it and starting.
		return AdmitBlock{Reason: AdmitExclusive, Conflicts: s.activeIDsLocked()}
	}
	if len(s.active) >= s.maxConc {
		return AdmitBlock{Reason: AdmitCap}
	}
	return AdmitBlock{}
}

// repoConflicts returns the ids of live runs whose claimed repositories intersect keys. The caller holds
// s.mu. Used to name exactly which runs to defer to free a needed repository (task point 6).
func (s *Scheduler) repoConflicts(keys []string) []string {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	seen := map[string]bool{}
	var out []string
	for id, ar := range s.active {
		for _, k := range ar.claims {
			if want[k] && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}

// activeIDsLocked lists every live run id (sorted). The caller holds s.mu.
func (s *Scheduler) activeIDsLocked() []string {
	out := make([]string, 0, len(s.active))
	for id := range s.active {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Capacity is the nominal concurrency ceiling (the number of standing slots), so the overview can show
// "belegt / frei" against it. Overloads run BEYOND this and are counted separately.
func (s *Scheduler) Capacity() int { return s.maxConc }

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
	markActive(len(s.active))
	s.pub.Publish(live.TopicActive) // a run ended (its slot freed)
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
	// and the manual-start worker), where an unrecovered panic in the injected Executor would crash ALL
	// of devlabd — not just this run. Contain it here.
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
	// A suspended run (usage limit or defer) resumes the SAME execution: it must not advance its schedule
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
		s.pub.Publish(live.TopicActive) // the live result id is now known — point the UI at it
	}
	ref, err := s.exec.Execute(ctx, run, report)
	if err != nil {
		s.logf("devlabd: run %s execution error: %v", id, err)
	}

	// Persist the outcome: either the execution paused (usage limit or defer — re-arm the suspension so
	// it resumes), or it finished (clear any suspension and attach the result).
	_, _ = s.store.Patch(func(cur []Run) ([]Run, error) {
		for i := range cur {
			if cur[i].ID != id {
				continue
			}
			if ref.Suspended {
				reason := ref.Reason
				if reason == "" {
					reason = ReasonUsageLimit
				}
				// A defer is a deliberate stand-down, not a failed retry — it must NOT consume the
				// usage-limit give-up budget (else repeatedly deferring an urgent run would eventually make
				// it give up). Only a usage-limit pause counts an attempt.
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
// deploy that would restart devlabd holds off while this is above zero (see markActive).
func (s *Scheduler) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}
