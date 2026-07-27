package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func noopLog(string, ...any) {}

// ── test executors ──────────────────────────────────────────────────────────────────────────────────

type fakeExec struct {
	mu       sync.Mutex
	executed []string
	maintain int
}

func (f *fakeExec) Execute(_ context.Context, run Run, report func(string)) (ResultRef, error) {
	f.mu.Lock()
	f.executed = append(f.executed, run.ID)
	f.mu.Unlock()
	report("res_1")
	return ResultRef{ResultID: "res_1", At: time.Now(), OK: true}, nil
}
func (f *fakeExec) Maintain(context.Context)          {
	f.mu.Lock()
	f.maintain++
	f.mu.Unlock()
}
func (f *fakeExec) PlanResume(Run, bool) ResumePlan { return ResumePlan{Action: ResumeFresh} }
func (f *fakeExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.executed)
}

// blockingExec lets a test hold runs mid-flight so it can OBSERVE concurrency: each Execute reports its
// result id, announces it started (on `started`), then blocks until the test releases it by id — or the
// run's context is cancelled (then it announces on `cancelled`). This is how the concurrency, exclusivity
// and cancel proofs pin down whether two runs actually overlap.
type blockingExec struct {
	mu        sync.Mutex
	started   chan string
	cancelled chan string
	releases  map[string]chan struct{}
}

func newBlockingExec() *blockingExec {
	return &blockingExec{
		started:   make(chan string, 16),
		cancelled: make(chan string, 16),
		releases:  map[string]chan struct{}{},
	}
}
func (b *blockingExec) gate(id string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.releases[id]
	if !ok {
		c = make(chan struct{})
		b.releases[id] = c
	}
	return c
}
func (b *blockingExec) Execute(ctx context.Context, run Run, report func(string)) (ResultRef, error) {
	report("res_" + run.ID)
	b.started <- run.ID
	select {
	case <-b.gate(run.ID):
		return ResultRef{ResultID: "res_" + run.ID, OK: true}, nil
	case <-ctx.Done():
		b.cancelled <- run.ID
		// Mirror the real executor: a DEFER stands the run down as a resumable, deferred suspension
		// (resumes ASAP at the next free slot); any other cancel is a plain abort/shutdown.
		if errors.Is(context.Cause(ctx), ErrRunDeferred) {
			now := time.Now()
			return ResultRef{ResultID: "res_" + run.ID, Suspended: true, ResumeAt: &now, Reason: ReasonDeferred}, nil
		}
		return ResultRef{ResultID: "res_" + run.ID, OK: false}, ctx.Err()
	}
}
func (b *blockingExec) Maintain(context.Context)        {}
func (b *blockingExec) PlanResume(Run, bool) ResumePlan { return ResumePlan{Action: ResumeFresh} }
func (b *blockingExec) release(id string)               { close(b.gate(id)) }

// ── helpers ─────────────────────────────────────────────────────────────────────────────────────────

func seedStore(t *testing.T, runsIn []Run) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	// Keep the deferred-restart + restart-pending markers inside the temp dir so a test never touches a real one.
	t.Setenv("DEVLAB_MERCURY_BUSY", filepath.Join(dir, "run-active"))
	t.Setenv("DEVLAB_MERCURY_RESTART_PENDING", filepath.Join(dir, "restart-pending"))
	s := NewStore()
	if _, err := s.Mutate("seed", "t", func([]Run) ([]Run, error) { return runsIn, nil }); err != nil {
		t.Fatal(err)
	}
	return s
}

func autoRun(id string) Run {
	past := time.Now().Add(-time.Minute)
	return Run{ID: id, Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &past, AxiomIDs: []string{"x"}}
}

func todoRun(id string, targets ...string) Run {
	past := time.Now().Add(-time.Minute)
	ts := make([]Target, 0, len(targets))
	for _, r := range targets {
		ts = append(ts, Target{Repo: r})
	}
	return Run{ID: id, Type: TypeTodo, Enabled: true, DueAt: &past, Targets: ts, Task: "do it"}
}

func recvWithin(t *testing.T, ch chan string, d time.Duration) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatal("timed out waiting for a run signal")
		return ""
	}
}

// expectNoStart asserts NO run begins within d — used to prove a run was DEFERRED (repo busy, exclusive
// floor held, or cap reached) rather than started.
func expectNoStart(t *testing.T, b *blockingExec, d time.Duration) {
	t.Helper()
	select {
	case id := <-b.started:
		t.Fatalf("expected no run to start, but %s did", id)
	case <-time.After(d):
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition never held: %s", msg)
}

func markerCount(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	// The marker is "<pid> <n> run(s) since <ts>"; the refcount is field 2.
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return "", false
	}
	return fields[1], true
}

// ── adapted existing tests ──────────────────────────────────────────────────────────────────────────

func TestSchedulerActiveReflectsRunningRun(t *testing.T) {
	store := seedStore(t, []Run{autoRun("r")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	if a := s.Active(); len(a) != 0 {
		t.Fatalf("expected no activity before firing, got %+v", a)
	}
	if _, ok := s.FireNow("r", "t", false); !ok {
		t.Fatal("FireNow returned false")
	}
	if id := recvWithin(t, be.started, time.Second); id != "r" {
		t.Fatalf("expected run r to start, got %s", id)
	}

	a := s.Active()
	if len(a) != 1 || a[0].RunID != "r" || a[0].ResultID != "res_r" {
		t.Fatalf("Active() did not reflect the running run: %+v", a)
	}
	if a[0].StartedAt.IsZero() {
		t.Error("Active()[0].StartedAt not stamped")
	}

	be.release("r")
	waitFor(t, 2*time.Second, func() bool { return len(s.Active()) == 0 }, "Active() did not clear after the run finished")
}

func TestSchedulerFiresOnlyDueEnabledRunsAndAdvances(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	sc := Schedule{Kind: Daily, TimeOfDay: "03:00"}
	store := seedStore(t, []Run{
		{ID: "due", Enabled: true, Schedule: sc, NextFireAt: &past, AxiomIDs: []string{"x"}},
		{ID: "later", Enabled: true, Schedule: sc, NextFireAt: &future, AxiomIDs: []string{"x"}},
		{ID: "off", Enabled: false, Schedule: sc, NextFireAt: &past, AxiomIDs: []string{"x"}},
	})
	fe := &fakeExec{}
	s := NewScheduler(store, fe, time.Second)
	s.logf = noopLog

	s.fireDue(context.Background())
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 && fe.count() == 1 }, "the due run to finish")

	fe.mu.Lock()
	got := append([]string(nil), fe.executed...)
	fe.mu.Unlock()
	if len(got) != 1 || got[0] != "due" {
		t.Fatalf("expected only 'due' fired, got %v", got)
	}
	d, _, _ := store.Get("due")
	if d.NextFireAt == nil || !d.NextFireAt.After(time.Now()) {
		t.Errorf("due NextFireAt not advanced forward: %v", d.NextFireAt)
	}
	if d.LastFiredAt == nil {
		t.Error("LastFiredAt not set")
	}
	if d.LastResult == nil || d.LastResult.ResultID != "res_1" {
		t.Errorf("result not attached: %+v", d.LastResult)
	}
}

// scriptedExec returns a programmed sequence of ResultRefs and records the Run it was handed each call
// (so a test can assert the resume passed a Suspended run through).
type scriptedExec struct {
	mu    sync.Mutex
	calls []Run
	resps []ResultRef
}

func (f *scriptedExec) Execute(_ context.Context, run Run, report func(string)) (ResultRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.calls)
	f.calls = append(f.calls, run)
	report("res")
	if i < len(f.resps) {
		return f.resps[i], nil
	}
	return ResultRef{ResultID: "res", OK: true}, nil
}
func (f *scriptedExec) Maintain(context.Context) {}
func (f *scriptedExec) PlanResume(run Run, fresh bool) ResumePlan {
	// Mirror the real executor's shape closely enough for scheduler tests: a run suspended on the usage
	// limit resumes its named result; anything else (or an explicit fresh start) starts fresh.
	if run.Suspended != nil && run.Suspended.ResultID != "" && !fresh {
		return ResumePlan{Action: ResumeContinue, ResultID: run.Suspended.ResultID}
	}
	return ResumePlan{Action: ResumeFresh}
}

func TestSchedulerSuspendsThenResumesOnUsageLimit(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	resumeAt := time.Now().Add(-time.Second) // already reset → immediately resumable
	sc := Schedule{Kind: Daily, TimeOfDay: "03:00"}
	store := seedStore(t, []Run{
		{ID: "r", Enabled: true, Schedule: sc, NextFireAt: &past, AxiomIDs: []string{"x"}},
	})
	ra := resumeAt
	fe := &scriptedExec{resps: []ResultRef{
		{ResultID: "res_1", Suspended: true, ResumeAt: &ra}, // 1st fire: hits the limit
		{ResultID: "res_1", OK: true},                       // resume: completes
	}}
	s := NewScheduler(store, fe, time.Second)
	s.logf = noopLog

	// Fire 1 → suspends.
	s.fireDue(context.Background())
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "fire 1 to finish")
	r, _, _ := store.Get("r")
	if r.Suspended == nil {
		t.Fatal("run should be suspended after the executor reported a usage limit")
	}
	if r.Suspended.Attempts != 1 || r.Suspended.ResultID != "res_1" {
		t.Fatalf("suspension = %+v, want attempts 1 / resultId res_1", r.Suspended)
	}
	if !isDue(r, time.Now()) {
		t.Error("a suspended run whose ResumeAt has passed must be due")
	}
	if r.NextFireAt == nil || !r.NextFireAt.After(time.Now()) {
		t.Error("schedule should have advanced (dormant while suspended)")
	}

	// Fire 2 → resumes the same execution and finishes.
	s.fireDue(context.Background())
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "resume to finish")
	fe.mu.Lock()
	nCalls := len(fe.calls)
	resumedSuspended := nCalls == 2 && fe.calls[1].Suspended != nil
	fe.mu.Unlock()
	if nCalls != 2 {
		t.Fatalf("expected 2 executions (fire + resume), got %d", nCalls)
	}
	if !resumedSuspended {
		t.Error("resume must hand the executor a run with Suspended set")
	}
	r2, _, _ := store.Get("r")
	if r2.Suspended != nil {
		t.Errorf("suspension not cleared after a completed resume: %+v", r2.Suspended)
	}
	if r2.LastResult == nil || r2.LastResult.ResultID != "res_1" {
		t.Errorf("completed result not attached: %+v", r2.LastResult)
	}
	if r2.NextFireAt == nil || !r2.NextFireAt.After(time.Now()) {
		t.Error("NextFireAt must be re-anchored forward after a resume")
	}
}

func TestSchedulerFireNowRunsOnceWithoutAdvancing(t *testing.T) {
	future := time.Now().Add(time.Hour)
	store := seedStore(t, []Run{
		{ID: "m", Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &future, AxiomIDs: []string{"x"}},
	})
	fe := &fakeExec{}
	s := NewScheduler(store, fe, time.Second)
	s.logf = noopLog

	if _, ok := s.FireNow("m", "tester", false); !ok {
		t.Fatal("FireNow returned busy on an idle scheduler")
	}
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 && fe.count() == 1 }, "the manual run to finish")
	m, _, _ := store.Get("m")
	if m.NextFireAt == nil || !m.NextFireAt.Equal(future) {
		t.Errorf("run-now must NOT advance the schedule; got %v want %v", m.NextFireAt, future)
	}
}

// TestFireNowReturnsResumePlan is the resume-vs-fresh decision at the scheduler seam: a manual trigger
// reports SYNCHRONOUSLY — before the detached run begins — whether it will continue an interrupted
// execution or start fresh, so the caller can tell the user which happened. The decision comes from the
// executor (here the scripted fake), the same source the execution path uses.
func TestFireNowReturnsResumePlan(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		{ID: "sus", Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &past,
			Suspended: &Suspension{ResumeAt: past, ResultID: "res_1"}, AxiomIDs: []string{"x"}},
		{ID: "plain", Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &past, AxiomIDs: []string{"x"}},
	})
	s := NewScheduler(store, &scriptedExec{}, time.Second)
	s.logf = noopLog

	fire := func(id string, fresh bool) ResumePlan {
		// Both are auto (exclusive) runs, so wait for the floor to clear before firing the next.
		waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "the previous run to release its slot")
		plan, ok := s.FireNow(id, "t", fresh)
		if !ok {
			t.Fatalf("FireNow(%s) unexpectedly blocked", id)
		}
		return plan
	}

	if p := fire("sus", false); p.Action != ResumeContinue || p.ResultID != "res_1" {
		t.Errorf("a suspended run must report a continuation of its execution: %+v", p)
	}
	if p := fire("plain", false); p.Action != ResumeFresh {
		t.Errorf("a run with nothing interrupted must report a fresh start: %+v", p)
	}
}

// ── concurrency proofs (task points 4, 7) ─────────────────────────────────────────────────────────────

// Two ToDos on DIFFERENT repositories run at the same time.
func TestConcurrentDifferentReposRunTogether(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a"), todoRun("t2", "repo-b")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	s.fireDue(context.Background())
	first := recvWithin(t, be.started, time.Second)
	second := recvWithin(t, be.started, time.Second) // both start before either is released → they overlap
	if first == second {
		t.Fatalf("the same run started twice: %s", first)
	}
	waitFor(t, time.Second, func() bool { return s.ActiveCount() == 2 }, "both runs to be active at once")

	be.release("t1")
	be.release("t2")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "both runs to finish")
}

// Two ToDos on the SAME repository run one after the other (task point 7).
func TestSameRepoRunsSequentially(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a"), todoRun("t2", "repo-a")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	s.fireDue(context.Background())
	first := recvWithin(t, be.started, time.Second)
	if first != "t1" {
		t.Fatalf("expected t1 to start first, got %s", first)
	}
	expectNoStart(t, be, 120*time.Millisecond) // t2 targets the busy repo → deferred, never overlaps
	if n := s.ActiveCount(); n != 1 {
		t.Fatalf("expected exactly one run on the shared repo, got %d", n)
	}

	be.release("t1")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "t1 to finish")

	s.fireDue(context.Background()) // t1 is now done; the repo is free → t2 runs
	if second := recvWithin(t, be.started, time.Second); second != "t2" {
		t.Fatalf("expected t2 to run after t1 released the repo, got %s", second)
	}
	be.release("t2")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "t2 to finish")
}

// A running ToDo blocks an auto (all-repos) run from starting — the auto is exclusive (task point 7).
func TestRunningTodoBlocksExclusiveAutoRun(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a"), autoRun("auto")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	s.fireDue(context.Background())
	if id := recvWithin(t, be.started, time.Second); id != "t1" {
		t.Fatalf("expected the ToDo to start, got %s", id)
	}
	expectNoStart(t, be, 120*time.Millisecond) // auto must wait for the floor to clear
	if n := s.ActiveCount(); n != 1 {
		t.Fatalf("auto run must not start while a ToDo runs; active=%d", n)
	}

	be.release("t1")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "the ToDo to finish")

	s.fireDue(context.Background())
	if id := recvWithin(t, be.started, time.Second); id != "auto" {
		t.Fatalf("expected the auto run once the floor was clear, got %s", id)
	}
	be.release("auto")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "the auto run to finish")
}

// A running auto (exclusive) run blocks every other run from starting (task point 7).
func TestRunningAutoRunBlocksEverythingElse(t *testing.T) {
	store := seedStore(t, []Run{autoRun("auto"), todoRun("t1", "repo-a")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	s.fireDue(context.Background())
	if id := recvWithin(t, be.started, time.Second); id != "auto" {
		t.Fatalf("expected the auto run to start, got %s", id)
	}
	expectNoStart(t, be, 120*time.Millisecond) // nothing starts while the exclusive run holds the floor
	if n := s.ActiveCount(); n != 1 {
		t.Fatalf("nothing may run beside an exclusive auto run; active=%d", n)
	}

	be.release("auto")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "the auto run to finish")

	s.fireDue(context.Background())
	if id := recvWithin(t, be.started, time.Second); id != "t1" {
		t.Fatalf("expected the ToDo once the auto run finished, got %s", id)
	}
	be.release("t1")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "the ToDo to finish")
}

// The concurrency cap defers extra runs even when their repositories are free (task point 5).
func TestConcurrencyCapDefersExtraRuns(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_CONCURRENCY", "1")
	store := seedStore(t, []Run{todoRun("t1", "repo-a"), todoRun("t2", "repo-b")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog
	if s.maxConc != 1 {
		t.Fatalf("expected cap 1 from env, got %d", s.maxConc)
	}

	s.fireDue(context.Background())
	recvWithin(t, be.started, time.Second)     // one run starts
	expectNoStart(t, be, 120*time.Millisecond) // the second waits for the slot despite a free repo
	if n := s.ActiveCount(); n != 1 {
		t.Fatalf("cap of 1 must allow only one run; active=%d", n)
	}
	be.release("t1")
	be.release("t2")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "runs to finish")
}

// Cancel aborts exactly the named run; the others keep going (task point 4).
func TestCancelTargetsSpecificRun(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a"), todoRun("t2", "repo-b")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	s.fireDue(context.Background())
	recvWithin(t, be.started, time.Second)
	recvWithin(t, be.started, time.Second)
	waitFor(t, time.Second, func() bool { return s.ActiveCount() == 2 }, "both runs active")

	if s.Cancel("nope") {
		t.Fatal("Cancel of an unknown id must return false")
	}
	if !s.Cancel("t2") {
		t.Fatal("Cancel of an active run must return true")
	}
	if id := recvWithin(t, be.cancelled, time.Second); id != "t2" {
		t.Fatalf("expected t2 to observe the cancel, got %s", id)
	}
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 1 }, "t2 to end after cancel")
	if a := s.Active(); len(a) != 1 || a[0].RunID != "t1" {
		t.Fatalf("only t1 should remain active, got %+v", a)
	}

	be.release("t1")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "t1 to finish")
}

// The deferred-restart marker COUNTS: it is written when the first run starts, still present (decremented)
// when one of several finishes, and removed only when the LAST run ends.
func TestActiveMarkerRefcounts(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a"), todoRun("t2", "repo-b")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog
	path := BusyMarkerPath()
	if _, ok := markerCount(path); ok {
		t.Fatal("marker must not exist before any run starts")
	}

	s.fireDue(context.Background())
	recvWithin(t, be.started, time.Second)
	recvWithin(t, be.started, time.Second)
	waitFor(t, time.Second, func() bool {
		v, ok := markerCount(path)
		return s.ActiveCount() == 2 && ok && v == "2"
	}, "marker to read 2 while two runs are active")

	be.release("t1")
	waitFor(t, 2*time.Second, func() bool {
		v, ok := markerCount(path)
		return s.ActiveCount() == 1 && ok && v == "1"
	}, "marker to decrement to 1 (not be removed) while one run remains")

	be.release("t2")
	waitFor(t, 2*time.Second, func() bool {
		_, ok := markerCount(path)
		return s.ActiveCount() == 0 && !ok
	}, "marker to be removed once the last run ends")
}

// ── slot management: defer & resume (task points 4, 7) ────────────────────────────────────────────────

// Defer stands a live run down: it frees its slot and re-arms as a DEFERRED suspension (the same pause
// concept as the usage limit), immediately due so it reclaims the next free slot and resumes the SAME
// execution — never counting a usage-limit attempt.
func TestDeferReArmsRunAsResumableDeferredSuspension(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	if _, ok := s.FireNow("t1", "user", false); !ok {
		t.Fatal("FireNow on an idle scheduler must start the run")
	}
	recvWithin(t, be.started, time.Second)
	waitFor(t, time.Second, func() bool { return s.ActiveCount() == 1 }, "t1 to be active")

	if s.Defer("nope") {
		t.Fatal("Defer of an unknown id must return false")
	}
	if !s.Defer("t1") {
		t.Fatal("Defer of an active run must return true")
	}
	if id := recvWithin(t, be.cancelled, time.Second); id != "t1" {
		t.Fatalf("t1 must observe the defer, got %s", id)
	}
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "t1 to release its slot")

	r, _, _ := store.Get("t1")
	if r.Suspended == nil || r.Suspended.Reason != ReasonDeferred {
		t.Fatalf("t1 must be re-armed as a deferred suspension, got %+v", r.Suspended)
	}
	if !r.Suspended.IsDeferred() {
		t.Error("IsDeferred() must recognise a deferred suspension")
	}
	if r.Suspended.ResultID != "res_t1" {
		t.Fatalf("the suspension must point at the open result, got %q", r.Suspended.ResultID)
	}
	if r.Suspended.Attempts != 0 {
		t.Errorf("a defer must NOT consume a usage-limit attempt, got %d", r.Suspended.Attempts)
	}
	if !isDue(r, time.Now()) {
		t.Error("a deferred run is immediately due — it competes for the next free slot")
	}

	// The freed slot lets it resume: fireDue re-admits it and hands the executor a Suspended run (a resume
	// of the same execution, not a fresh start).
	s.fireDue(context.Background())
	if id := recvWithin(t, be.started, time.Second); id != "t1" {
		t.Fatalf("expected t1 to resume, got %s", id)
	}
	be.release("t1")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "the resume to finish")

	r2, _, _ := store.Get("t1")
	if r2.Suspended != nil {
		t.Errorf("the suspension must be cleared after a completed resume, got %+v", r2.Suspended)
	}
	if !r2.Done {
		t.Error("a ToDo that finished on resume must be checked off")
	}
}

// progressExec drives the defer→resume round-trip against a REAL results store, honouring the executor
// contract: it records each repo it completes into the result and, on resume (handed a Suspended run),
// continues the SAME result and SKIPS the repos already recorded. After each repo it waits on a gate so
// the test can inject a defer at a known point. This proves a deferred run resumes at the same place and
// never repeats completed work (task point 4).
type progressExec struct {
	results *Results
	repos   []string // the sweep, in order

	mu        sync.Mutex
	worked    map[string][]string // runID → repos actually worked, across ALL passes
	afterRepo chan string         // one signal per completed repo
	proceed   chan struct{}       // test lets the executor continue to the next repo
}

func newProgressExec(results *Results, repos ...string) *progressExec {
	return &progressExec{
		results: results, repos: repos,
		worked:    map[string][]string{},
		afterRepo: make(chan string, 16),
		proceed:   make(chan struct{}, 16),
	}
}

func (p *progressExec) Maintain(context.Context)          {}
func (p *progressExec) PlanResume(run Run, fresh bool) ResumePlan {
	if run.Suspended != nil && run.Suspended.ResultID != "" && !fresh {
		return ResumePlan{Action: ResumeContinue, ResultID: run.Suspended.ResultID}
	}
	return ResumePlan{Action: ResumeFresh}
}

func (p *progressExec) Execute(ctx context.Context, run Run, report func(string)) (ResultRef, error) {
	var res Result
	if run.Suspended != nil && run.Suspended.ResultID != "" {
		if existing, ok, _ := p.results.Get(run.ID, run.Suspended.ResultID); ok {
			res = existing // resume the open result
		}
	}
	if res.ResultID == "" {
		res = Result{RunID: run.ID, ResultID: "res_" + run.ID, StartedAt: time.Now()}
	}
	res.Suspended, res.ResumeAt = false, nil
	report(res.ResultID)
	_ = p.results.Save(res)

	done := res.DoneRepos()
	for _, repo := range p.repos {
		if done[repo] {
			continue // a resume skips exactly the repos already completed
		}
		p.mu.Lock()
		p.worked[run.ID] = append(p.worked[run.ID], repo)
		p.mu.Unlock()
		res.Repos = append(res.Repos, RepoResult{Repo: repo, OK: true})
		_ = p.results.Save(res) // persist after every repo, like the real executor

		p.afterRepo <- repo
		select {
		case <-p.proceed:
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), ErrRunDeferred) {
				now := time.Now()
				res.Suspended, res.ResumeAt = true, &now
				_ = p.results.Save(res)
				return ResultRef{ResultID: res.ResultID, Suspended: true, ResumeAt: &now, Reason: ReasonDeferred}, nil
			}
			return ResultRef{ResultID: res.ResultID, OK: false}, ctx.Err()
		}
	}
	res.FinishedAt = time.Now()
	res.OK = true
	_ = p.results.Save(res)
	return ResultRef{ResultID: res.ResultID, OK: true}, nil
}

func TestDeferResumeSkipsCompletedRepos(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "results"))
	results := NewResults()
	store := seedStore(t, []Run{todoRun("t1", "repo-a")})
	pe := newProgressExec(results, "r1", "r2", "r3")
	s := NewScheduler(store, pe, time.Second)
	s.logf = noopLog

	if _, ok := s.FireNow("t1", "user", false); !ok {
		t.Fatal("FireNow must start the run")
	}
	// It completes r1, then waits at the gate.
	if repo := recvWithin(t, pe.afterRepo, time.Second); repo != "r1" {
		t.Fatalf("expected r1 first, got %s", repo)
	}
	// Defer mid-sweep — after r1 is done but before r2.
	if !s.Defer("t1") {
		t.Fatal("Defer must return true for an active run")
	}
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "t1 to defer and free its slot")

	r, _, _ := store.Get("t1")
	if r.Suspended == nil || !r.Suspended.IsDeferred() {
		t.Fatalf("t1 must be a deferred suspension after the defer, got %+v", r.Suspended)
	}

	// Resume: it must pick up at r2 and never redo r1.
	s.fireDue(context.Background())
	if repo := recvWithin(t, pe.afterRepo, 2*time.Second); repo != "r2" {
		t.Fatalf("resume must continue at r2 (r1 already done), got %s", repo)
	}
	pe.proceed <- struct{}{} // r2 → r3
	if repo := recvWithin(t, pe.afterRepo, 2*time.Second); repo != "r3" {
		t.Fatalf("expected r3 next, got %s", repo)
	}
	pe.proceed <- struct{}{} // r3 → finish
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "the resume to finish")

	pe.mu.Lock()
	worked := append([]string(nil), pe.worked["t1"]...)
	pe.mu.Unlock()
	want := []string{"r1", "r2", "r3"} // r1 exactly once — never repeated on the resume
	if strings.Join(worked, ",") != strings.Join(want, ",") {
		t.Fatalf("deferred run repeated or skipped work: worked %v, want %v", worked, want)
	}

	r2, _, _ := store.Get("t1")
	if !r2.Done || r2.Suspended != nil {
		t.Errorf("t1 must finish cleanly after the resume: done=%v suspended=%+v", r2.Done, r2.Suspended)
	}
}

// ── slot management: overload (task points 5, 7) ─────────────────────────────────────────────────────

// Overload takes a TEMPORARY extra slot past the cap, and the slot self-heals when the run ends — the
// standing ceiling is never raised.
func TestOverloadAdmitsPastCapAndSelfHeals(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_CONCURRENCY", "1")
	store := seedStore(t, []Run{todoRun("a", "repo-a"), todoRun("b", "repo-b"), todoRun("c", "repo-c")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	if _, ok := s.FireNow("a", "user", false); !ok {
		t.Fatal("a must start on the idle scheduler")
	}
	recvWithin(t, be.started, time.Second)

	if _, ok := s.FireNow("b", "user", false); ok {
		t.Fatal("b must NOT start normally — the cap of 1 is reached")
	}
	expectNoStart(t, be, 120*time.Millisecond)

	if _, ok := s.StartOverload("b", "user", false); !ok {
		t.Fatal("overload must start b in a temporary extra slot past the cap")
	}
	if id := recvWithin(t, be.started, time.Second); id != "b" {
		t.Fatalf("expected b to overload-start, got %s", id)
	}
	waitFor(t, time.Second, func() bool { return s.ActiveCount() == 2 }, "a + overloaded b to run past cap 1")

	flags := map[string]bool{}
	for _, act := range s.Active() {
		flags[act.RunID] = act.Overload
	}
	if !flags["b"] || flags["a"] {
		t.Fatalf("only b must carry the overload flag, got %+v", flags)
	}

	// Self-heal: b's extra slot vanishes when it ends — the cap is 1 again, so c still waits behind a.
	be.release("b")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 1 }, "overloaded b to release its extra slot")
	if _, ok := s.FireNow("c", "user", false); ok {
		t.Fatal("the overload must not raise the standing ceiling — at cap 1, c must wait behind a")
	}

	be.release("a")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "a to finish")
}

// Overload never crosses the two hard limits (task point 7): it refuses to put two runs on the same
// repository, and it refuses to join an exclusive auto run.
func TestOverloadRefusedOnRepoConflict(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_CONCURRENCY", "1")
	store := seedStore(t, []Run{todoRun("a", "repo-x"), todoRun("b", "repo-x"), todoRun("c", "repo-y")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	if _, ok := s.FireNow("a", "user", false); !ok {
		t.Fatal("a must start")
	}
	recvWithin(t, be.started, time.Second)

	if _, ok := s.StartOverload("b", "user", false); ok {
		t.Fatal("overload MUST be refused — b targets repo-x, which a already holds")
	}
	expectNoStart(t, be, 120*time.Millisecond)

	if _, ok := s.StartOverload("c", "user", false); !ok {
		t.Fatal("overload of c (free repo) must be allowed")
	}
	if id := recvWithin(t, be.started, time.Second); id != "c" {
		t.Fatalf("expected c to overload-start, got %s", id)
	}

	be.release("a")
	be.release("c")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "runs to finish")
}

func TestOverloadRefusedUnderExclusiveAuto(t *testing.T) {
	store := seedStore(t, []Run{autoRun("auto"), todoRun("t", "repo-a")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	if _, ok := s.FireNow("auto", "user", false); !ok {
		t.Fatal("auto must start")
	}
	recvWithin(t, be.started, time.Second)

	if _, ok := s.StartOverload("t", "user", false); ok {
		t.Fatal("overload MUST be refused while an exclusive auto run holds the whole floor")
	}
	expectNoStart(t, be, 120*time.Millisecond)

	be.release("auto")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "auto to finish")
}

// ── restart / run-start mutual exclusion (NACHZUHOLEN) ───────────────────────────────────────────────

// While a devlabd restart is pending (the deferred-restart helper holds the restart-pending marker), no
// new run may start — it is held (queued) until the restart happens. A stale marker (writer gone) is
// ignored, so a crashed helper never wedges the scheduler.
func TestRestartPendingHoldsNewStarts(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog

	pend := RestartPendingPath()
	// A marker with THIS (alive) process's PID → a restart is pending.
	if err := os.WriteFile(pend, []byte(strconv.Itoa(os.Getpid())+" 2026-07-27T00:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !RestartPending() {
		t.Fatal("a marker with a live PID must read as pending")
	}
	if _, ok := s.FireNow("t1", "user", false); ok {
		t.Fatal("no run may start while a restart is pending")
	}
	r, _, _ := store.Get("t1")
	if b := s.Admissibility(r); b.Reason != AdmitRestartPending {
		t.Fatalf("want a restart-pending block, got %q", b.Reason)
	}
	expectNoStart(t, be, 120*time.Millisecond)

	// Clearing the marker lets the run start.
	_ = os.Remove(pend)
	if RestartPending() {
		t.Fatal("removing the marker must clear the pending state")
	}
	if _, ok := s.FireNow("t1", "user", false); !ok {
		t.Fatal("the run must start once no restart is pending")
	}
	recvWithin(t, be.started, time.Second)
	be.release("t1")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "t1 to finish")

	// A stale marker (dead PID, far above any real one) is ignored — never wedges the scheduler.
	if err := os.WriteFile(pend, []byte("2147480000 stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if RestartPending() {
		t.Error("a marker whose writer is gone must be treated as not pending")
	}
	_ = os.Remove(pend)
}

// Admissibility classifies exactly WHY a run cannot start, and names the runs that must stand down to
// unblock it — the input to the start-decision options and the automatic suggestion (task point 6).
func TestAdmissibilityClassifiesBlocks(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_CONCURRENCY", "1")
	store := seedStore(t, []Run{todoRun("a", "repo-x"), todoRun("b", "repo-x"), todoRun("c", "repo-y"), autoRun("auto")})
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog
	getRun := func(id string) Run { r, _, _ := store.Get(id); return r }

	if b := s.Admissibility(getRun("a")); b.Reason != "" {
		t.Fatalf("an idle scheduler must admit, got %q", b.Reason)
	}

	if _, ok := s.FireNow("a", "user", false); !ok {
		t.Fatal("a must start")
	}
	recvWithin(t, be.started, time.Second)

	if b := s.Admissibility(getRun("a")); b.Reason != AdmitRunning {
		t.Fatalf("a is live → %q, want %q", b.Reason, AdmitRunning)
	}
	if b := s.Admissibility(getRun("b")); b.Reason != AdmitRepoBusy || len(b.Conflicts) != 1 || b.Conflicts[0] != "a" {
		t.Fatalf("b shares repo-x with a → want repo-busy conflict [a], got %+v", b)
	}
	if b := s.Admissibility(getRun("c")); b.Reason != AdmitCap || len(b.Conflicts) != 0 {
		t.Fatalf("c has a free repo but the cap is full → want cap/no-conflict, got %+v", b)
	}
	if b := s.Admissibility(getRun("auto")); b.Reason != AdmitExclusive || len(b.Conflicts) != 1 || b.Conflicts[0] != "a" {
		t.Fatalf("auto needs an empty floor → want exclusive conflict [a], got %+v", b)
	}

	be.release("a")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "a to finish")

	if _, ok := s.FireNow("auto", "user", false); !ok {
		t.Fatal("auto must start on the clear floor")
	}
	recvWithin(t, be.started, time.Second)
	if b := s.Admissibility(getRun("c")); b.Reason != AdmitExclusive || len(b.Conflicts) != 1 || b.Conflicts[0] != "auto" {
		t.Fatalf("a running auto run blocks c exclusively → want conflict [auto], got %+v", b)
	}
	be.release("auto")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "auto to finish")
}
