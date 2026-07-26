package runs

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

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

// blockingExec reports a result id, signals it has started, then blocks until released — so a test can
// observe Active() WHILE a run is in flight.
type blockingExec struct {
	reported chan string
	release  chan struct{}
}

func (b *blockingExec) Execute(_ context.Context, run Run, report func(string)) (ResultRef, error) {
	report("res_live")
	b.reported <- run.ID
	<-b.release
	return ResultRef{ResultID: "res_live", OK: true}, nil
}
func (b *blockingExec) Maintain(context.Context) {}

// gateExec lets a test hold each run inside Execute (blocked on a per-run gate) so it can observe how many
// runs are live at once and release them in a chosen order. startCh gets one send per Execute entry.
type gateExec struct {
	mu       sync.Mutex
	started  []string
	startCh  chan string
	releases map[string]chan struct{}
	closed   map[string]bool
}

func newGateExec() *gateExec {
	return &gateExec{startCh: make(chan string, 32), releases: map[string]chan struct{}{}, closed: map[string]bool{}}
}

func (g *gateExec) gate(id string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.releases[id] == nil {
		g.releases[id] = make(chan struct{})
	}
	return g.releases[id]
}

func (g *gateExec) Execute(_ context.Context, run Run, report func(string)) (ResultRef, error) {
	rel := g.gate(run.ID)
	g.mu.Lock()
	g.started = append(g.started, run.ID)
	g.mu.Unlock()
	report("res_" + run.ID)
	g.startCh <- run.ID
	<-rel
	return ResultRef{ResultID: "res_" + run.ID, At: time.Now(), OK: true}, nil
}
func (g *gateExec) Maintain(context.Context) {}

// releaseRun unblocks a run's Execute; idempotent so a test can release everything at teardown safely.
func (g *gateExec) releaseRun(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.releases[id] == nil {
		g.releases[id] = make(chan struct{})
	}
	if !g.closed[id] {
		g.closed[id] = true
		close(g.releases[id])
	}
}
func (g *gateExec) startedIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.started...)
}

func todoRun(id, repo string, due time.Time) Run {
	d := due
	return Run{ID: id, Name: id, Type: TypeTodo, Enabled: true, Targets: []Target{{Repo: repo}}, DueAt: &d}
}

func autoRun(id string, due time.Time) Run {
	d := due
	return Run{ID: id, Name: id, Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &d, AxiomIDs: []string{"x"}}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func recvStart(t *testing.T, g *gateExec) string {
	t.Helper()
	select {
	case id := <-g.startCh:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a run to enter Execute")
		return ""
	}
}

func assertNoStart(t *testing.T, g *gateExec, d time.Duration) {
	t.Helper()
	select {
	case id := <-g.startCh:
		t.Fatalf("a run started that should have been deferred: %s", id)
	case <-time.After(d):
	}
}

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

func TestSchedulerActiveReflectsRunningRun(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		{ID: "r", Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &past, AxiomIDs: []string{"x"}},
	})
	be := &blockingExec{reported: make(chan string, 1), release: make(chan struct{})}
	s := NewScheduler(store, be, time.Second, 2)
	s.logf = func(string, ...any) {}

	if a := s.Active(); len(a) != 0 {
		t.Fatalf("expected no activity before firing, got %+v", a)
	}
	if !s.FireNow("r", "t") {
		t.Fatal("FireNow returned false")
	}
	<-be.reported // the run has reported its result id and is now blocked mid-execution

	a := s.Active()
	if len(a) != 1 || a[0].RunID != "r" || a[0].ResultID != "res_live" {
		t.Fatalf("Active() did not reflect the running run: %+v", a)
	}
	if a[0].StartedAt.IsZero() {
		t.Error("Active().StartedAt not stamped")
	}
	if !s.RunsActive() || s.ActiveCount() != 1 {
		t.Errorf("marker should report 1 active run, got count %d", s.ActiveCount())
	}

	close(be.release) // let the run finish → Active clears
	waitFor(t, "Active() to clear after the run finished", func() bool { return len(s.Active()) == 0 })
	if s.RunsActive() {
		t.Error("marker should be clear once the last run ended")
	}
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
	s := NewScheduler(store, fe, time.Second, 2)
	s.logf = func(string, ...any) {}

	s.fireDue(context.Background())
	waitFor(t, "the due run to finish", func() bool { return fe.count() == 1 && s.ActiveCount() == 0 })

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
func (f *scriptedExec) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
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
	s := NewScheduler(store, fe, time.Second, 2)
	s.logf = func(string, ...any) {}

	// Fire 1 → suspends.
	s.fireDue(context.Background())
	waitFor(t, "the first fire to settle", func() bool { return fe.callCount() == 1 && s.ActiveCount() == 0 })
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
	waitFor(t, "the resume to settle", func() bool { return fe.callCount() == 2 && s.ActiveCount() == 0 })
	fe.mu.Lock()
	resumedSuspended := len(fe.calls) == 2 && fe.calls[1].Suspended != nil
	fe.mu.Unlock()
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
	s := NewScheduler(store, fe, time.Second, 2)
	s.logf = func(string, ...any) {}

	if !s.FireNow("m", "tester") {
		t.Fatal("FireNow returned busy on an idle scheduler")
	}
	waitFor(t, "the manual run to finish", func() bool { return fe.count() == 1 && s.ActiveCount() == 0 })
	m, _, _ := store.Get("m")
	if m.NextFireAt == nil || !m.NextFireAt.Equal(future) {
		t.Errorf("run-now must NOT advance the schedule; got %v want %v", m.NextFireAt, future)
	}
}

// TestSchedulerRunsDifferentReposConcurrently proves two ToDos on different repos run at the same time,
// and that the active-run marker counts down (set on the first, cleared only on the last).
func TestSchedulerRunsDifferentReposConcurrently(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		todoRun("a", "alpha", past),
		todoRun("b", "beta", past),
	})
	g := newGateExec()
	s := NewScheduler(store, g, time.Second, 2)
	s.logf = func(string, ...any) {}

	s.fireDue(context.Background())

	// Both reserve synchronously (tryStart reserves under the lock before launching), so the marker is 2.
	if n := s.ActiveCount(); n != 2 {
		t.Fatalf("expected 2 concurrent runs, got %d", n)
	}
	got := map[string]bool{recvStart(t, g): true, recvStart(t, g): true}
	if !got["a"] || !got["b"] {
		t.Fatalf("expected both a and b executing concurrently, got %v", got)
	}

	// The marker counts, it does not switch: releasing one leaves it set; only the last release clears it.
	g.releaseRun("a")
	waitFor(t, "one of two runs to finish", func() bool { return s.ActiveCount() == 1 })
	if !s.RunsActive() {
		t.Error("marker must stay set while a second run is still working")
	}
	g.releaseRun("b")
	waitFor(t, "all runs to finish", func() bool { return s.ActiveCount() == 0 })
}

// TestSchedulerSerializesSameRepo proves two ToDos on the SAME repo run one after another — the second is
// deferred (not blocked in flight) while the first holds the repo, then starts once the repo is free.
func TestSchedulerSerializesSameRepo(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		todoRun("a", "shared", past),
		todoRun("b", "shared", past),
	})
	g := newGateExec()
	s := NewScheduler(store, g, time.Second, 2) // cap 2 — so ONLY the same-repo rule can serialize them
	s.logf = func(string, ...any) {}

	s.fireDue(context.Background())
	if n := s.ActiveCount(); n != 1 {
		t.Fatalf("two runs on the same repo must not run at once; active=%d", n)
	}
	first := recvStart(t, g)
	assertNoStart(t, g, 50*time.Millisecond) // the sibling is deferred, not blocked-in-flight

	g.releaseRun(first)
	waitFor(t, "the first same-repo run to finish", func() bool { return s.ActiveCount() == 0 })

	s.fireDue(context.Background()) // repo is free now → the sibling starts
	second := recvStart(t, g)
	if second == first {
		t.Fatalf("second fire re-ran the same run %q instead of the sibling", first)
	}
	g.releaseRun(second)
	waitFor(t, "the second same-repo run to finish", func() bool { return s.ActiveCount() == 0 })

	if ids := g.startedIDs(); len(ids) != 2 {
		t.Fatalf("expected exactly 2 sequential executions, got %v", ids)
	}
}

// TestSchedulerAutoRunExclusive proves an auto run (which sweeps every repo) runs strictly alone: it does
// not start while another run is active, and no run starts while it is active — in both orderings.
func TestSchedulerAutoRunExclusive(t *testing.T) {
	past := time.Now().Add(-time.Minute)

	t.Run("active auto run blocks a ToDo", func(t *testing.T) {
		store := seedStore(t, []Run{
			autoRun("auto", past),
			todoRun("todo", "alpha", past),
		})
		g := newGateExec()
		s := NewScheduler(store, g, time.Second, 4)
		s.logf = func(string, ...any) {}

		s.fireDue(context.Background())
		if n := s.ActiveCount(); n != 1 {
			t.Fatalf("an auto run must run alone; active=%d", n)
		}
		if id := recvStart(t, g); id != "auto" {
			t.Fatalf("expected the auto run to start, got %q", id)
		}
		assertNoStart(t, g, 50*time.Millisecond) // the ToDo is deferred while the auto run holds the box

		g.releaseRun("auto")
		waitFor(t, "the auto run to finish", func() bool { return s.ActiveCount() == 0 })

		s.fireDue(context.Background())
		if id := recvStart(t, g); id != "todo" {
			t.Fatalf("expected the ToDo to start once the box is free, got %q", id)
		}
		g.releaseRun("todo")
		waitFor(t, "the ToDo to finish", func() bool { return s.ActiveCount() == 0 })
	})

	t.Run("active ToDo blocks an auto run", func(t *testing.T) {
		store := seedStore(t, []Run{
			todoRun("todo", "alpha", past),
			autoRun("auto", past),
		})
		g := newGateExec()
		s := NewScheduler(store, g, time.Second, 4)
		s.logf = func(string, ...any) {}

		s.fireDue(context.Background())
		if n := s.ActiveCount(); n != 1 {
			t.Fatalf("an auto run must not join an active ToDo; active=%d", n)
		}
		if id := recvStart(t, g); id != "todo" {
			t.Fatalf("expected the ToDo to start, got %q", id)
		}
		assertNoStart(t, g, 50*time.Millisecond) // the exclusive auto run waits for the box to empty

		g.releaseRun("todo")
		waitFor(t, "the ToDo to finish", func() bool { return s.ActiveCount() == 0 })

		s.fireDue(context.Background())
		if id := recvStart(t, g); id != "auto" {
			t.Fatalf("expected the auto run to start once the box is free, got %q", id)
		}
		g.releaseRun("auto")
		waitFor(t, "the auto run to finish", func() bool { return s.ActiveCount() == 0 })
	})
}

// TestSchedulerConcurrencyCeiling proves the configurable cap bounds how many runs execute at once, even
// when their repos are all distinct and free — the surplus is deferred, then admitted as slots free up.
func TestSchedulerConcurrencyCeiling(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		todoRun("a", "alpha", past),
		todoRun("b", "beta", past),
		todoRun("c", "gamma", past),
	})
	g := newGateExec()
	s := NewScheduler(store, g, time.Second, 2) // ceiling of 2
	s.logf = func(string, ...any) {}

	s.fireDue(context.Background())
	if n := s.ActiveCount(); n != 2 {
		t.Fatalf("ceiling of 2 must cap concurrent runs; active=%d", n)
	}
	recvStart(t, g)
	recvStart(t, g)
	assertNoStart(t, g, 50*time.Millisecond) // the 3rd is deferred by the ceiling, three free repos notwithstanding

	started := g.startedIDs()
	g.releaseRun(started[0])
	waitFor(t, "a concurrency slot to free", func() bool { return s.ActiveCount() == 1 })

	s.fireDue(context.Background()) // a slot is free → the deferred run is admitted
	waitFor(t, "the deferred run to be admitted", func() bool { return s.ActiveCount() == 2 })
	recvStart(t, g)

	for _, id := range []string{"a", "b", "c"} {
		g.releaseRun(id)
	}
	waitFor(t, "all runs to finish", func() bool { return s.ActiveCount() == 0 })
	if ids := g.startedIDs(); len(ids) != 3 {
		t.Fatalf("expected all 3 runs to eventually execute, got %v", ids)
	}
}
