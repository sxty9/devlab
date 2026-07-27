package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Test harness ───────────────────────────────────────────────────────────

// gateExec blocks each Execute on a per-run gate so a test can observe how many runs are live at once and
// release them in a chosen order. It records the ids it was handed and whether each was a resume (Suspended).
type gateExec struct {
	mu        sync.Mutex
	started   []string
	suspended map[string]bool
	gates     map[string]chan struct{}
	closed    map[string]bool
}

func newGateExec() *gateExec {
	return &gateExec{suspended: map[string]bool{}, gates: map[string]chan struct{}{}, closed: map[string]bool{}}
}

func (g *gateExec) gateLocked(id string) chan struct{} {
	if g.gates[id] == nil {
		g.gates[id] = make(chan struct{})
	}
	return g.gates[id]
}

func (g *gateExec) Execute(_ context.Context, run Run, report func(string)) (ResultRef, error) {
	g.mu.Lock()
	g.started = append(g.started, run.ID)
	g.suspended[run.ID] = run.Suspended != nil
	rel := g.gateLocked(run.ID)
	g.mu.Unlock()
	report("res_" + run.ID)
	<-rel
	return ResultRef{ResultID: "res_" + run.ID, At: time.Now(), OK: true}, nil
}
func (g *gateExec) Maintain(context.Context)        {}
func (g *gateExec) PlanResume(Run, bool) ResumePlan { return ResumePlan{Action: ResumeFresh} }

// release lets a gated run finish (idempotent).
func (g *gateExec) release(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ch := g.gateLocked(id)
	if !g.closed[id] {
		g.closed[id] = true
		close(ch)
	}
}

func (g *gateExec) startedIDs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.started...)
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

func todoRun(id, repo string, due time.Time) Run {
	return Run{ID: id, Type: TypeTodo, Enabled: true, Targets: []Target{{Repo: repo}}, DueAt: &due}
}

func autoRun(id string, due time.Time) Run {
	return Run{ID: id, Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &due, AxiomIDs: []string{"x"}}
}

// waitFor polls cond up to 2s, failing with `what` if it never holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func quiet(s *Scheduler) *Scheduler { s.logf = func(string, ...any) {}; return s }

// settle waits for every run goroutine to finish (floor idle) so the test's temp dir is not removed while
// a run is still writing runs.json or the marker.
func settle(t *testing.T, s *Scheduler) {
	t.Helper()
	waitFor(t, "floor idle", func() bool { return s.ActiveCount() == 0 })
}

// ─── Concurrency ────────────────────────────────────────────────────────────

// TestSchedulerRunsDifferentReposConcurrently: two ToDos on different repos run at once; the marker counts
// down as each finishes and only clears at zero.
func TestSchedulerRunsDifferentReposConcurrently(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{todoRun("a", "repoA", past), todoRun("b", "repoB", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))

	s.fireDue(context.Background())
	if s.ActiveCount() != 2 {
		t.Fatalf("two different-repo ToDos should run concurrently, ActiveCount=%d", s.ActiveCount())
	}
	waitFor(t, "both executing", func() bool { return len(ge.startedIDs()) == 2 })

	ge.release("a")
	waitFor(t, "one finished", func() bool { return s.ActiveCount() == 1 })
	if !s.RunsActive() {
		t.Error("RunsActive must stay true while the second run continues (marker counts, not switches)")
	}
	ge.release("b")
	waitFor(t, "floor empty", func() bool { return s.ActiveCount() == 0 })
}

// TestSchedulerSerializesSameRepo: two ToDos on the SAME repo never run at once, even under a cap that
// would otherwise allow it — the per-repo claim is the hard rule.
func TestSchedulerSerializesSameRepo(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{todoRun("a", "shared", past), todoRun("b", "shared", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2)) // cap 2 → only the repo rule can serialize

	s.fireDue(context.Background())
	if s.ActiveCount() != 1 {
		t.Fatalf("same-repo ToDos must serialize, ActiveCount=%d", s.ActiveCount())
	}
	// The sibling is deferred, not merely slow: it must not start while the first holds the repo.
	time.Sleep(30 * time.Millisecond)
	if s.ActiveCount() != 1 {
		t.Fatalf("the same-repo sibling must stay deferred, ActiveCount=%d", s.ActiveCount())
	}
	first := ge.startedIDs()[0]

	ge.release(first)
	waitFor(t, "first released", func() bool { return s.ActiveCount() == 0 })
	s.fireDue(context.Background()) // now the repo is free → the sibling starts
	waitFor(t, "sibling started", func() bool { return s.ActiveCount() == 1 })
	ge.release("a")
	ge.release("b")
	waitFor(t, "both ran exactly once", func() bool { return len(ge.startedIDs()) == 2 })
	settle(t, s)
}

// TestSchedulerAutoRunSharesFloor (Part D, overrides Part B point 7): an auto run over all repos is NOT
// exclusive — it occupies one slot, so a ToDo in another repo runs alongside it. Two runs in the SAME repo
// still never run at once. This is req 15's second test.
func TestSchedulerAutoRunSharesFloor(t *testing.T) {
	past := time.Now().Add(-time.Minute)

	t.Run("an auto run and a ToDo in another repo run concurrently", func(t *testing.T) {
		store := seedStore(t, []Run{autoRun("auto", past), todoRun("todo", "repoX", past)})
		ge := newGateExec()
		s := quiet(NewScheduler(store, ge, time.Second, 4))
		s.fireDue(context.Background())
		if s.ActiveCount() != 2 {
			t.Fatalf("an auto run must NOT hold the whole floor — it and a ToDo in another repo run together, ActiveCount=%d", s.ActiveCount())
		}
		waitFor(t, "both live", func() bool { return len(ge.startedIDs()) == 2 })
		ge.release("auto")
		ge.release("todo")
		settle(t, s)
	})

	t.Run("two runs in the same repo still never run at once", func(t *testing.T) {
		store := seedStore(t, []Run{todoRun("a", "same", past), todoRun("b", "same", past)})
		ge := newGateExec()
		s := quiet(NewScheduler(store, ge, time.Second, 4))
		s.fireDue(context.Background())
		if s.ActiveCount() != 1 {
			t.Fatalf("two runs in the same repo must serialize, ActiveCount=%d", s.ActiveCount())
		}
		time.Sleep(30 * time.Millisecond)
		if s.ActiveCount() != 1 {
			t.Fatalf("the same-repo sibling must stay deferred, ActiveCount=%d", s.ActiveCount())
		}
		ge.release(ge.startedIDs()[0])
		waitFor(t, "first done", func() bool { return s.ActiveCount() == 0 })
		s.fireDue(context.Background())
		waitFor(t, "sibling runs after", func() bool { return s.ActiveCount() == 1 })
		ge.release("a")
		ge.release("b")
		settle(t, s)
	})
}

// TestSchedulerConcurrencyCeiling: with cap 2 and three free-repo ToDos, only two run; the third waits for
// a slot and then starts.
func TestSchedulerConcurrencyCeiling(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		todoRun("a", "r1", past), todoRun("b", "r2", past), todoRun("c", "r3", past),
	})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))

	s.fireDue(context.Background())
	if s.ActiveCount() != 2 {
		t.Fatalf("the cap must hold ActiveCount at 2, got %d", s.ActiveCount())
	}
	time.Sleep(30 * time.Millisecond)
	if s.ActiveCount() != 2 {
		t.Fatalf("the third run must be deferred by the ceiling, ActiveCount=%d", s.ActiveCount())
	}
	// Free a slot → the third admits on the next fire.
	ge.release(ge.startedIDs()[0])
	waitFor(t, "a slot freed", func() bool { return s.ActiveCount() == 1 })
	s.fireDue(context.Background())
	waitFor(t, "third started", func() bool { return s.ActiveCount() == 2 })
	for _, id := range []string{"a", "b", "c"} {
		ge.release(id)
	}
	waitFor(t, "all three ran", func() bool { return len(ge.startedIDs()) == 3 })
	settle(t, s)
}

// TestSchedulerCancelTargetsOneRun: cancelling one concurrent run leaves the other running.
func TestSchedulerCancelTargetsOneRun(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{todoRun("a", "r1", past), todoRun("b", "r2", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))

	s.fireDue(context.Background())
	waitFor(t, "both live", func() bool { return len(ge.startedIDs()) == 2 })
	if !s.Cancel("a") {
		t.Fatal("Cancel(a) should target the live run a")
	}
	if s.Cancel("zzz") {
		t.Error("Cancel of a non-active run must return false")
	}
	// a's context was aborted → its gateExec still blocks until released, but b is untouched.
	ge.release("a")
	waitFor(t, "a released", func() bool { return s.ActiveCount() == 1 })
	if act := s.Active(); len(act) != 1 || act[0].RunID != "b" {
		t.Errorf("b must still be running after a was cancelled, active=%+v", act)
	}
	ge.release("b")
	waitFor(t, "floor empty", func() bool { return s.ActiveCount() == 0 })
}

// TestSchedulerMarkerRefcount: the on-disk marker holds the active COUNT and clears only at zero.
func TestSchedulerMarkerRefcount(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{todoRun("a", "r1", past), todoRun("b", "r2", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))
	marker := BusyMarkerPath()

	s.fireDue(context.Background())
	waitFor(t, "marker shows 2", func() bool { return markerCount(marker) == 2 })
	ge.release("a")
	waitFor(t, "marker shows 1", func() bool { return markerCount(marker) == 1 })
	ge.release("b")
	waitFor(t, "marker gone", func() bool { _, err := os.Stat(marker); return os.IsNotExist(err) })
}

// markerCount reads the count (second field) out of the busy marker, or -1 if absent/unreadable.
func markerCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return -1
	}
	var n int
	for _, r := range fields[1] {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// ─── Live-activity projection ───────────────────────────────────────────────

func TestSchedulerActiveReflectsRunningRun(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{autoRun("r", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))

	if a := s.Active(); len(a) != 0 {
		t.Fatalf("expected no activity before firing, got %+v", a)
	}
	if _, ok := s.FireNow("r", "t", false); !ok {
		t.Fatal("FireNow returned false")
	}
	waitFor(t, "run started", func() bool { return len(ge.startedIDs()) == 1 })
	a := s.Active()
	if len(a) != 1 || a[0].RunID != "r" || a[0].ResultID != "res_r" {
		t.Fatalf("Active() did not reflect the running run: %+v", a)
	}
	if a[0].StartedAt.IsZero() {
		t.Error("Active().StartedAt not stamped")
	}
	ge.release("r")
	waitFor(t, "activity cleared", func() bool { return len(s.Active()) == 0 })
}

// ─── Behavioural (schedule / suspend / fire-now) ────────────────────────────

func TestSchedulerFiresOnlyDueEnabledRunsAndAdvances(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	sc := Schedule{Kind: Daily, TimeOfDay: "03:00"}
	store := seedStore(t, []Run{
		{ID: "due", Enabled: true, Schedule: sc, NextFireAt: &past, AxiomIDs: []string{"x"}},
		{ID: "later", Enabled: true, Schedule: sc, NextFireAt: &future, AxiomIDs: []string{"x"}},
		{ID: "off", Enabled: false, Schedule: sc, NextFireAt: &past, AxiomIDs: []string{"x"}},
	})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))

	s.fireDue(context.Background())
	waitFor(t, "due started", func() bool { return len(ge.startedIDs()) == 1 })
	if got := ge.startedIDs(); got[0] != "due" {
		t.Fatalf("expected only 'due' fired, got %v", got)
	}
	ge.release("due")
	waitFor(t, "due finished", func() bool {
		d, _, _ := store.Get("due")
		return d.LastResult != nil
	})
	d, _, _ := store.Get("due")
	if d.NextFireAt == nil || !d.NextFireAt.After(time.Now()) {
		t.Errorf("due NextFireAt not advanced forward: %v", d.NextFireAt)
	}
	if d.LastFiredAt == nil {
		t.Error("LastFiredAt not set")
	}
	if d.LastResult == nil || d.LastResult.ResultID != "res_due" {
		t.Errorf("result not attached: %+v", d.LastResult)
	}
	settle(t, s)
}

// scriptedExec returns a programmed sequence of ResultRefs and records the Run it was handed each call (so
// a test can assert the resume passed a Suspended run through).
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
	if run.Suspended != nil && run.Suspended.ResultID != "" && !fresh {
		return ResumePlan{Action: ResumeContinue, ResultID: run.Suspended.ResultID}
	}
	return ResumePlan{Action: ResumeFresh}
}
func (f *scriptedExec) nCalls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }
func (f *scriptedExec) call(i int) Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func TestSchedulerSuspendsThenResumesOnUsageLimit(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	resumeAt := time.Now().Add(-time.Second) // already reset → immediately resumable
	store := seedStore(t, []Run{autoRun("r", past)})
	ra := resumeAt
	fe := &scriptedExec{resps: []ResultRef{
		{ResultID: "res_1", Suspended: true, ResumeAt: &ra}, // 1st fire: hits the limit
		{ResultID: "res_1", OK: true},                       // resume: completes
	}}
	s := quiet(NewScheduler(store, fe, time.Second, 2))

	s.fireDue(context.Background())
	waitFor(t, "suspended", func() bool { r, _, _ := store.Get("r"); return r.Suspended != nil })
	r, _, _ := store.Get("r")
	if r.Suspended.Attempts != 1 || r.Suspended.ResultID != "res_1" {
		t.Fatalf("suspension = %+v, want attempts 1 / resultId res_1", r.Suspended)
	}
	if !isDue(r, time.Now()) {
		t.Error("a suspended run whose ResumeAt has passed must be due")
	}
	if r.NextFireAt == nil || !r.NextFireAt.After(time.Now()) {
		t.Error("schedule should have advanced (dormant while suspended)")
	}

	s.fireDue(context.Background())
	waitFor(t, "resumed & finished", func() bool { r, _, _ := store.Get("r"); return r.Suspended == nil && r.LastResult != nil })
	if n := fe.nCalls(); n != 2 {
		t.Fatalf("expected 2 executions (fire + resume), got %d", n)
	}
	if fe.call(1).Suspended == nil {
		t.Error("resume must hand the executor a run with Suspended set")
	}
	r2, _, _ := store.Get("r")
	if r2.LastResult == nil || r2.LastResult.ResultID != "res_1" {
		t.Errorf("completed result not attached: %+v", r2.LastResult)
	}
	if r2.NextFireAt == nil || !r2.NextFireAt.After(time.Now()) {
		t.Error("NextFireAt must be re-anchored forward after a resume")
	}
	settle(t, s)
}

func TestSchedulerFireNowRunsOnceWithoutAdvancing(t *testing.T) {
	future := time.Now().Add(time.Hour)
	store := seedStore(t, []Run{autoRun("m", future)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))

	if _, ok := s.FireNow("m", "tester", false); !ok {
		t.Fatal("FireNow returned busy on an idle scheduler")
	}
	waitFor(t, "started", func() bool { return len(ge.startedIDs()) == 1 })
	ge.release("m")
	waitFor(t, "finished", func() bool { m, _, _ := store.Get("m"); return m.LastResult != nil })
	m, _, _ := store.Get("m")
	if m.NextFireAt == nil || !m.NextFireAt.Equal(future) {
		t.Errorf("run-now must NOT advance the schedule; got %v want %v", m.NextFireAt, future)
	}
	settle(t, s)
}

// TestFireNowReturnsResumePlan: a manual trigger reports SYNCHRONOUSLY whether it will continue an
// interrupted execution or start fresh, decided by the executor (the scripted fake).
func TestFireNowReturnsResumePlan(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		{ID: "sus", Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &past,
			Suspended: &Suspension{ResumeAt: past, ResultID: "res_1"}, AxiomIDs: []string{"x"}},
		{ID: "plain", Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &past, AxiomIDs: []string{"x"}},
	})
	s := quiet(NewScheduler(store, &scriptedExec{}, time.Second, 2))

	// Both are auto runs (exclusive), so wait for the floor to clear between them.
	fire := func(id string, fresh bool) (ResumePlan, bool) {
		waitFor(t, "idle before "+id, func() bool { return s.ActiveCount() == 0 })
		return s.FireNow(id, "t", fresh)
	}
	if p, ok := fire("sus", false); !ok || p.Action != ResumeContinue || p.ResultID != "res_1" {
		t.Errorf("a suspended run must report a continuation: ok=%v plan=%+v", ok, p)
	}
	if p, ok := fire("plain", false); !ok || p.Action != ResumeFresh {
		t.Errorf("a run with nothing interrupted must report a fresh start: ok=%v plan=%+v", ok, p)
	}
	settle(t, s)
}

// ─── Slot management: defer / overload / admissibility ───────────────────────

// deferExec blocks the first execution on ctx; a defer (ErrRunDeferred) makes it return an immediately-due
// deferred suspension. Once finishNow is set, later executions complete straight away (the resume path).
type deferExec struct {
	mu        sync.Mutex
	calls     []Run
	finishNow bool
}

func (d *deferExec) Execute(ctx context.Context, run Run, report func(string)) (ResultRef, error) {
	d.mu.Lock()
	d.calls = append(d.calls, run)
	finish := d.finishNow
	d.mu.Unlock()
	report("res_1")
	if finish {
		return ResultRef{ResultID: "res_1", OK: true}, nil
	}
	<-ctx.Done()
	if errors.Is(context.Cause(ctx), ErrRunDeferred) {
		now := time.Now()
		return ResultRef{ResultID: "res_1", ResumeAt: &now, Suspended: true, Reason: ReasonDeferred}, nil
	}
	return ResultRef{ResultID: "res_1", OK: false}, nil
}
func (d *deferExec) Maintain(context.Context)        {}
func (d *deferExec) PlanResume(Run, bool) ResumePlan { return ResumePlan{Action: ResumeFresh} }
func (d *deferExec) handed(i int) Run                { d.mu.Lock(); defer d.mu.Unlock(); return d.calls[i] }
func (d *deferExec) nCalls() int                     { d.mu.Lock(); defer d.mu.Unlock(); return len(d.calls) }
func (d *deferExec) setFinish()                      { d.mu.Lock(); d.finishNow = true; d.mu.Unlock() }

// TestDeferReArmsRunAsResumable: Defer frees the slot and re-arms the run as an immediately-due deferred
// suspension; a re-fire resumes the SAME execution and finishes it. Reuses the ONE suspension mechanism.
func TestDeferReArmsRunAsResumable(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{todoRun("t1", "r1", past)})
	de := &deferExec{}
	s := quiet(NewScheduler(store, de, time.Second, 2))

	if !s.FireNowStart("t1") {
		t.Fatal("t1 should start")
	}
	waitFor(t, "t1 executing", func() bool { return de.nCalls() == 1 })

	if s.Defer("nope") {
		t.Error("Defer of a non-active run must be false")
	}
	if !s.Defer("t1") {
		t.Fatal("Defer(t1) should stand the running run down")
	}
	waitFor(t, "slot freed", func() bool { return s.ActiveCount() == 0 })
	waitFor(t, "re-armed as deferred", func() bool {
		r, _, _ := store.Get("t1")
		return r.Suspended != nil && r.Suspended.IsDeferred()
	})
	r, _, _ := store.Get("t1")
	if r.Suspended.ResumeAt.After(time.Now()) {
		t.Error("a deferred suspension must be immediately due")
	}

	// Resume: it must be handed a Suspended run (a continuation), not a fresh start, and finish.
	de.setFinish()
	s.fireDue(context.Background())
	waitFor(t, "resumed & done", func() bool { r, _, _ := store.Get("t1"); return r.Suspended == nil && r.Done })
	if de.nCalls() != 2 || de.handed(1).Suspended == nil {
		t.Errorf("resume must continue the same execution (Suspended run), calls=%d", de.nCalls())
	}
	settle(t, s)
}

// FireNowStart is a tiny test convenience: start a run, ignoring the resume plan.
func (s *Scheduler) FireNowStart(id string) bool { _, ok := s.FireNow(id, "t", false); return ok }

// TestRestartPendingQueuesStarts: while a restart drains, no new run is admitted; a start is queued
// (StartPending), and the drain waits for the in-flight run then completes.
func TestRestartPendingQueuesStarts(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{todoRun("a", "r1", past), todoRun("b", "r2", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 4))

	s.FireNowStart("a")
	waitFor(t, "a live", func() bool { return s.ActiveCount() == 1 })

	if liveID := s.RequestRestart(); liveID != "a" {
		t.Errorf("RequestRestart should name the in-flight run, got %q", liveID)
	}
	if !s.RestartPending() {
		t.Error("RestartPending must be true after RequestRestart")
	}
	if s.FireNowStart("b") {
		t.Error("no new run may start while a restart is draining")
	}
	s.QueueStart("b")
	if b, _, _ := store.Get("b"); !b.StartPending || !isDue(b, time.Now()) {
		t.Error("a queued start must be StartPending and due")
	}

	// The drain does not complete while a runs; it does once a is released.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if s.AwaitDrain(ctx) {
		t.Error("drain must not complete while a run is live")
	}
	cancel()
	ge.release("a")
	dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dcancel()
	if !s.AwaitDrain(dctx) {
		t.Error("drain must complete once the floor is empty")
	}
}

// TestStartPendingFiresAfterRestart: the fresh process fires a persisted StartPending run once and clears
// the flag (so it does not re-fire).
func TestStartPendingFiresAfterRestart(t *testing.T) {
	store := seedStore(t, []Run{{ID: "q", Type: TypeTodo, Enabled: true, Targets: []Target{{Repo: "r1"}}, StartPending: true}})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))

	s.fireDue(context.Background())
	waitFor(t, "queued start fired", func() bool { return len(ge.startedIDs()) == 1 })
	ge.release("q")
	waitFor(t, "cleared", func() bool { q, _, _ := store.Get("q"); return !q.StartPending && s.ActiveCount() == 0 })

	// A second pass must not re-fire it.
	s.fireDue(context.Background())
	time.Sleep(20 * time.Millisecond)
	if n := len(ge.startedIDs()); n != 1 {
		t.Errorf("a queued start must fire exactly once, got %d", n)
	}
	settle(t, s)
}

// TestSchedulerTenConcurrent (req 15 test 1): with ten slots, ten different-repo ToDos all run at once.
func TestSchedulerTenConcurrent(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	in := make([]Run, 0, 10)
	for i := 0; i < 10; i++ {
		in = append(in, todoRun("t"+string(rune('0'+i)), "repo"+string(rune('0'+i)), past))
	}
	store := seedStore(t, in)
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 10))

	s.fireDue(context.Background())
	if s.ActiveCount() != 10 {
		t.Fatalf("ten slots must run ten concurrent ToDos, ActiveCount=%d", s.ActiveCount())
	}
	waitFor(t, "all ten executing", func() bool { return len(ge.startedIDs()) == 10 })
	for _, r := range in {
		ge.release(r.ID)
	}
	settle(t, s)
}

// TestSetCapacityTakesEffectLive (req 13): raising the cap admits a waiting run at once (no restart);
// lowering it stops new admissions and lets the floor drain naturally (never killing a live run).
func TestSetCapacityTakesEffectLive(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{todoRun("a", "r1", past), todoRun("b", "r2", past), todoRun("c", "r3", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 1))
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go s.Run(runCtx) // the poke path needs the Run loop

	waitFor(t, "one admitted at cap 1", func() bool { return s.ActiveCount() == 1 })
	time.Sleep(30 * time.Millisecond)
	if s.ActiveCount() != 1 {
		t.Fatalf("cap 1 must hold at one, ActiveCount=%d", s.ActiveCount())
	}

	s.SetCapacity(3) // raise → the poke admits the waiting runs immediately
	waitFor(t, "raise admits waiting runs", func() bool { return s.ActiveCount() == 3 })

	// Lower to 1: a live run is never killed — the count only falls by natural completion.
	s.SetCapacity(1)
	if s.ActiveCount() != 3 {
		t.Fatalf("lowering the cap must not kill live runs, ActiveCount=%d", s.ActiveCount())
	}
	for _, id := range []string{"a", "b", "c"} {
		ge.release(id)
	}
	settle(t, s)
}

// TestPerRepoClaimCoordination (req 14): the per-repo claim an auto run takes during execution blocks a
// ToDo targeting that repo (repo-busy) and frees it on release — the mechanism behind "put a busy repo at
// the back and come back", shared with the ToDo admission claim.
func TestPerRepoClaimCoordination(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{autoRun("auto", past), todoRun("todo", "shared", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 4))

	s.FireNowStart("auto")
	waitFor(t, "auto live", func() bool { return s.ActiveCount() == 1 })

	key := RepoClaimKey("shared")
	if !s.TryClaimRepo("auto", key) {
		t.Fatal("the auto run should be able to claim a free repo")
	}
	if s.TryClaimRepo("auto", key) {
		t.Error("a repo already claimed must not be claimable again")
	}
	todo, _, _ := store.Get("todo")
	if blk := s.Admissibility(todo); blk.Reason != AdmitRepoBusy {
		t.Errorf("a ToDo on the repo the auto run holds must be repo-busy, got %+v", blk)
	}
	s.ReleaseRepo("auto", key)
	if blk := s.Admissibility(todo); blk.Reason != AdmitOK {
		t.Errorf("releasing the repo must let the ToDo in, got %+v", blk)
	}
	ge.release("auto")
	settle(t, s)
}

// TestOverloadAdmitsPastCapAndSelfHeals: an overload runs past the cap in an extra slot that vanishes on
// release — repeated overloads never raise the standing ceiling.
func TestOverloadAdmitsPastCapAndSelfHeals(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{todoRun("a", "r1", past), todoRun("b", "r2", past), todoRun("c", "r3", past)})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 1)) // cap 1

	if !s.FireNowStart("a") {
		t.Fatal("a should take the single slot")
	}
	waitFor(t, "a live", func() bool { return s.ActiveCount() == 1 })
	if _, ok := s.StartOverload("b", "t", false); !ok {
		t.Fatal("overload b should admit past the cap")
	}
	waitFor(t, "overloaded", func() bool { return s.ActiveCount() == 2 })

	ge.release("b")
	waitFor(t, "overload ended", func() bool { return s.ActiveCount() == 1 })
	// The extra slot vanished: with cap 1 and a still running, c must NOT start.
	s.fireDue(context.Background())
	time.Sleep(30 * time.Millisecond)
	if s.ActiveCount() != 1 {
		t.Fatalf("the ceiling must be back to 1 (overload self-healed), ActiveCount=%d", s.ActiveCount())
	}
	ge.release("a")
	waitFor(t, "a done", func() bool { return s.ActiveCount() == 0 })
	s.fireDue(context.Background())
	waitFor(t, "c starts in the freed slot", func() bool { return s.ActiveCount() == 1 })
	ge.release("c")
	settle(t, s)
}

// TestOverloadRespectsHardLimits: overload never crosses a busy repo or an exclusive floor (req 7).
func TestOverloadRespectsHardLimits(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	t.Run("refused on repo conflict", func(t *testing.T) {
		store := seedStore(t, []Run{todoRun("a", "shared", past), todoRun("b", "shared", past), todoRun("c", "free", past)})
		ge := newGateExec()
		s := quiet(NewScheduler(store, ge, time.Second, 1))
		s.FireNowStart("a")
		waitFor(t, "a live", func() bool { return s.ActiveCount() == 1 })
		if _, ok := s.StartOverload("b", "t", false); ok {
			t.Error("overload onto a claimed repo must be refused")
		}
		if _, ok := s.StartOverload("c", "t", false); !ok {
			t.Error("overload onto a free repo must be allowed")
		}
		ge.release("a")
		ge.release("c")
		settle(t, s)
	})
}

// TestAdmissibilityClassifiesBlocks: the classifier names the block and the conflicting run-ids.
func TestAdmissibilityClassifiesBlocks(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		todoRun("a", "shared", past), todoRun("b", "shared", past), todoRun("c", "free", past),
		autoRun("auto", past),
	})
	ge := newGateExec()
	s := quiet(NewScheduler(store, ge, time.Second, 2))

	// Nothing running → admissible.
	aRun, _, _ := store.Get("a")
	if blk := s.Admissibility(aRun); blk.Reason != AdmitOK {
		t.Fatalf("an idle floor must admit, got %+v", blk)
	}
	s.FireNowStart("a")
	waitFor(t, "a live", func() bool { return s.ActiveCount() == 1 })

	if blk := s.Admissibility(aRun); blk.Reason != AdmitRunning {
		t.Errorf("a live run must classify as running, got %+v", blk)
	}
	bRun, _, _ := store.Get("b")
	if blk := s.Admissibility(bRun); blk.Reason != AdmitRepoBusy || len(blk.Conflicts) != 1 || blk.Conflicts[0] != "a" {
		t.Errorf("a same-repo ToDo must be repo-busy conflicting with a, got %+v", blk)
	}
	// An auto run declares no repos up front, so it is admissible while a ToDo runs (they share the floor).
	autoR, _, _ := store.Get("auto")
	if blk := s.Admissibility(autoR); blk.Reason != AdmitOK {
		t.Errorf("an auto run should be admissible alongside a running ToDo (no exclusivity), got %+v", blk)
	}
	ge.release("a")
	settle(t, s)
}
