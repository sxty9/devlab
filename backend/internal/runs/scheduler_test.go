package runs

import (
	"context"
	"os"
	"path/filepath"
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

func (f *fakeExec) Execute(_ context.Context, run Run, _ Trigger, report func(string)) (ResultRef, error) {
	f.mu.Lock()
	f.executed = append(f.executed, run.ID)
	f.mu.Unlock()
	report("res_1")
	return ResultRef{ResultID: "res_1", At: time.Now(), OK: true}, nil
}
func (f *fakeExec) Maintain(context.Context) {
	f.mu.Lock()
	f.maintain++
	f.mu.Unlock()
}
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
func (b *blockingExec) Execute(ctx context.Context, run Run, _ Trigger, report func(string)) (ResultRef, error) {
	report("res_" + run.ID)
	b.started <- run.ID
	select {
	case <-b.gate(run.ID):
		return ResultRef{ResultID: "res_" + run.ID, OK: true}, nil
	case <-ctx.Done():
		b.cancelled <- run.ID
		return ResultRef{ResultID: "res_" + run.ID, OK: false}, ctx.Err()
	}
}
func (b *blockingExec) Maintain(context.Context) {}
func (b *blockingExec) release(id string)        { close(b.gate(id)) }

// ── helpers ─────────────────────────────────────────────────────────────────────────────────────────

func seedStore(t *testing.T, runsIn []Run) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
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

func marker(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// ── adapted existing tests ──────────────────────────────────────────────────────────────────────────

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
	resps []ResultRef          // positional (serial expectations)
	byRun map[string]ResultRef // by run id — the reliable form once runs are concurrent
}

func (f *scriptedExec) Execute(_ context.Context, run Run, _ Trigger, report func(string)) (ResultRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.calls)
	f.calls = append(f.calls, run)
	report("res")
	if r, ok := f.byRun[run.ID]; ok {
		return r, nil
	}
	if i < len(f.resps) {
		return f.resps[i], nil
	}
	return ResultRef{ResultID: "res", OK: true}, nil
}
func (f *scriptedExec) Maintain(context.Context) {}

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

// TestSchedulerTodoDoneOnlyWhenNoPRsOpen pins the "in die History erst nach dem Merge"-rule: a ToDo whose
// execution opened PRs is NOT checked off — it stays in the active list awaiting its merge — while a ToDo
// with nothing to merge (report mode / no changes → PRsOpen false) is done at once.
func TestSchedulerTodoDoneOnlyWhenNoPRsOpen(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		{ID: "a", Type: TypeTodo, Enabled: true, DueAt: &past, Task: "x"},
		{ID: "b", Type: TypeTodo, Enabled: true, DueAt: &past, Task: "x"},
	})
	fe := &scriptedExec{byRun: map[string]ResultRef{
		"a": {ResultID: "r", OK: true, PRsOpen: true},  // opened PRs, awaits merge
		"b": {ResultID: "r", OK: true, PRsOpen: false}, // nothing to merge
	}}
	s := NewScheduler(store, fe, time.Second)
	s.logf = func(string, ...any) {}

	s.fireDue(context.Background())
	eventually(t, "both ToDos finished", func() bool {
		a, _, _ := store.Get("a")
		b, _, _ := store.Get("b")
		return a.LastResult != nil && b.LastResult != nil
	})

	a, _, _ := store.Get("a")
	if a.Done {
		t.Error("a ToDo with open PRs must stay in the list (not done) until the main-merge is through")
	}
	b, _, _ := store.Get("b")
	if !b.Done {
		t.Error("a ToDo with nothing to merge must be checked off at once")
	}
}

// TestSchedulerFireNowRestartsDoneTodo pins the restart: manually re-running an already-erledigt ToDo
// reopens it (Done cleared) and — because the fresh run opens PRs — it awaits merge again rather than
// snapping straight back to done. This is the "wieder anstartbar" path for a completed/stuck ToDo.
func TestSchedulerFireNowRestartsDoneTodo(t *testing.T) {
	store := seedStore(t, []Run{{ID: "t", Type: TypeTodo, Enabled: true, Done: true, Task: "x"}})
	fe := &scriptedExec{resps: []ResultRef{{ResultID: "r", OK: true, PRsOpen: true}}}
	s := NewScheduler(store, fe, time.Second)
	s.logf = func(string, ...any) {}

	if s.FireNow("t", "tester") != StartFired {
		t.Fatal("FireNow returned busy on an idle scheduler")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fe.mu.Lock()
		done := len(fe.calls) >= 1
		fe.mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the detached result-attach patch land
	got, _, _ := store.Get("t")
	if got.Done {
		t.Error("re-running a done ToDo must reopen it (Done cleared), not leave it checked off")
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

	if s.FireNow("m", "tester") != StartFired {
		t.Fatal("FireNow returned busy on an idle scheduler")
	}
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 && fe.count() == 1 }, "the manual run to finish")
	m, _, _ := store.Get("m")
	if m.NextFireAt == nil || !m.NextFireAt.Equal(future) {
		t.Errorf("run-now must NOT advance the schedule; got %v want %v", m.NextFireAt, future)
	}
}

// ── concurrency proofs (task point 6) ─────────────────────────────────────────────────────────────────

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

// Two ToDos on the SAME repository run one after the other.
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

// A running ToDo blocks an auto (all-repos) run from starting — the auto is exclusive.
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

// A running auto (exclusive) run blocks every other run from starting.
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

// The concurrency cap defers extra runs even when their repositories are free.
func TestConcurrencyCapDefersExtraRuns(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a"), todoRun("t2", "repo-b")})
	t.Setenv("DEVLAB_RUNS_MAX_CONCURRENCY", "1")
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

// Cancel aborts exactly the named run; the others keep going.
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
	if _, ok := marker(s.marker); ok {
		t.Fatal("marker must not exist before any run starts")
	}

	s.fireDue(context.Background())
	recvWithin(t, be.started, time.Second)
	recvWithin(t, be.started, time.Second)
	waitFor(t, time.Second, func() bool {
		v, ok := marker(s.marker)
		return s.ActiveCount() == 2 && ok && v == "2"
	}, "marker to read 2 while two runs are active")

	be.release("t1")
	waitFor(t, 2*time.Second, func() bool {
		v, ok := marker(s.marker)
		return s.ActiveCount() == 1 && ok && v == "1"
	}, "marker to decrement to 1 (not be removed) while one run remains")

	be.release("t2")
	waitFor(t, 2*time.Second, func() bool {
		_, ok := marker(s.marker)
		return s.ActiveCount() == 0 && !ok
	}, "marker to be removed once the last run ends")
}
