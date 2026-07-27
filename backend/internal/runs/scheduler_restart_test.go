package runs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// drainExec blocks in Execute until released OR its context is cancelled, recording — per execution —
// whether the context was cancelled and whether the cancel was a deliberate abort. It is the instrument
// for "a restart drains a run, it does not kill it": a drained run returns via release with no ctx error.
type drainExec struct {
	reported chan string
	release  chan struct{}

	mu      sync.Mutex
	execd   int
	ctxErr  error
	aborted bool
}

func (d *drainExec) Execute(ctx context.Context, run Run, _ Trigger, report func(string)) (ResultRef, error) {
	report("res_" + run.ID)
	select {
	case d.reported <- run.ID: // publish that this run is now live and about to block
	default:
	}
	select {
	case <-d.release:
	case <-ctx.Done():
	}
	d.mu.Lock()
	d.execd++
	d.ctxErr = ctx.Err()
	if errors.Is(context.Cause(ctx), ErrRunAborted) {
		d.aborted = true
	}
	d.mu.Unlock()
	if ctx.Err() != nil {
		return ResultRef{ResultID: "res_" + run.ID, OK: false}, ctx.Err()
	}
	return ResultRef{ResultID: "res_" + run.ID, OK: true}, nil
}
func (d *drainExec) Maintain(context.Context) {}
func (d *drainExec) wasAborted() bool         { d.mu.Lock(); defer d.mu.Unlock(); return d.aborted }
func (d *drainExec) count() int               { d.mu.Lock(); defer d.mu.Unlock(); return d.execd }

// TestRestartDrainsRunningRunWithoutKilling proves requirement 1+2: once a restart is requested, the
// in-flight run is DRAINED (allowed to finish), a concurrent start attempt is refused-and-queued (never
// started-then-killed), and the running run is never aborted.
func TestRestartDrainsRunningRunWithoutKilling(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		{ID: "r", Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &past, AxiomIDs: []string{"x"}},
		{ID: "q", Enabled: true, Type: TypeTodo, Task: "t", Targets: []Target{{Repo: "a"}}, DueAt: &past},
	})
	de := &drainExec{reported: make(chan string, 1), release: make(chan struct{})}
	s := NewScheduler(store, de, time.Second)
	s.logf = func(string, ...any) {}

	if s.FireNow("r", "t") != StartFired {
		t.Fatal("run r did not start")
	}
	<-de.reported // r is live and blocked mid-execution

	// A restart requested WHILE r runs must SEE it draining (return its id), not cancel it.
	if active := s.RequestRestart(); active != "r" {
		t.Fatalf("RequestRestart must report run r draining, got %q", active)
	}
	// The gate is closed: a start attempt now is deferred and QUEUED, not run (and not lost).
	if got := s.FireNow("q", "t"); got != StartDeferred {
		t.Fatalf("a start during drain must be deferred, got %v", got)
	}
	if q, _, _ := store.Get("q"); !q.StartPending {
		t.Error("the deferred run must be queued (StartPending), never silently dropped")
	}

	// Drain must WAIT for r (not abort it): a short grace elapses with r still running.
	shortCtx, c1 := context.WithTimeout(context.Background(), 80*time.Millisecond)
	if s.AwaitDrain(shortCtx) {
		t.Error("AwaitDrain returned before the run finished — it must drain, not kill")
	}
	c1()
	if de.count() != 0 {
		t.Error("the running run was ended prematurely")
	}

	// Let r finish on its own — it completes; it was never killed.
	close(de.release)
	longCtx, c2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer c2()
	if !s.AwaitDrain(longCtx) {
		t.Fatal("run did not drain after it was allowed to finish")
	}
	if de.wasAborted() {
		t.Error("the running run was killed (aborted) by the restart — it must drain to completion")
	}
}

// TestRestartStartRaceKillsNoRun runs the restart request and the start attempt GENUINELY concurrently
// (many times, best under -race) and asserts the mutual-exclusion invariant holds under every
// interleaving: a run that started is drained (never aborted), and a run that was deferred never ran and
// is queued instead. Either way, no run is killed.
func TestRestartStartRaceKillsNoRun(t *testing.T) {
	for i := 0; i < 40; i++ {
		past := time.Now().Add(-time.Minute)
		store := seedStore(t, []Run{
			{ID: "r", Enabled: true, Type: TypeTodo, Task: "t", Targets: []Target{{Repo: "a"}}, DueAt: &past},
		})
		de := &drainExec{reported: make(chan string, 1), release: make(chan struct{})}
		s := NewScheduler(store, de, time.Second)
		s.logf = func(string, ...any) {}

		var wg sync.WaitGroup
		var outcome StartOutcome
		wg.Add(2)
		go func() { defer wg.Done(); s.RequestRestart() }()
		go func() { defer wg.Done(); outcome = s.FireNow("r", "op") }()
		wg.Wait()

		switch outcome {
		case StartFired:
			<-de.reported
			close(de.release)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			s.AwaitDrain(ctx)
			cancel()
			if de.wasAborted() {
				t.Fatalf("iter %d: a started run was killed by the concurrent restart", i)
			}
		case StartDeferred:
			if de.count() != 0 {
				t.Fatalf("iter %d: a deferred run executed anyway", i)
			}
			if r, _, _ := store.Get("r"); !r.StartPending {
				t.Fatalf("iter %d: a deferred run was not queued", i)
			}
		default:
			t.Fatalf("iter %d: unexpected StartBusy on an idle scheduler", i)
		}
	}
}

// TestQueuedRunStartsAfterRestart proves requirement 3: a run triggered while a restart drains is
// queued (persisted) and starts BY ITSELF after the restart — modelled as a fresh scheduler over the
// same store (the restart wiped in-memory state, so the gate is gone).
func TestQueuedRunStartsAfterRestart(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		{ID: "q", Enabled: true, Type: TypeTodo, Task: "t", Targets: []Target{{Repo: "a"}}, DueAt: &past},
	})

	// Draining process: the trigger is deferred + queued, never executed here.
	fe1 := &fakeExec{}
	s1 := NewScheduler(store, fe1, time.Second)
	s1.logf = func(string, ...any) {}
	s1.RequestRestart()
	if s1.FireNow("q", "operator") != StartDeferred {
		t.Fatal("a trigger during drain must be deferred")
	}
	if fe1.count() != 0 {
		t.Fatal("a deferred run must not execute in the draining process")
	}
	if q, _, _ := store.Get("q"); !q.StartPending {
		t.Fatal("the deferred run was not queued for after the restart")
	}

	// After the restart: a fresh scheduler on the same store starts the queued run on its next tick,
	// with nobody re-triggering it.
	fe2 := &fakeExec{}
	s2 := NewScheduler(store, fe2, time.Second)
	s2.logf = func(string, ...any) {}
	s2.fireDue(context.Background())

	eventually(t, "the queued run auto-starts after the restart", func() bool { return fe2.count() == 1 })
	q, _, _ := store.Get("q")
	if q.StartPending {
		t.Error("StartPending must be cleared once the queued run starts")
	}
	if !q.Done {
		t.Error("the completed ToDo should be marked done")
	}
}

// resumingExec models the real executor's resume CONTRACT using the same primitives it uses
// (FindResumable + DoneRepos): it picks up the interrupted husk and works only the repos not already
// finished. It lets a scheduler-level test prove "continues at the same place" without the Claude CLI.
type resumingExec struct {
	results *Results
	repos   []string

	mu     sync.Mutex
	worked []string
}

func (e *resumingExec) Execute(_ context.Context, run Run, _ Trigger, report func(string)) (ResultRef, error) {
	res := Result{RunID: run.ID, ResultID: NewResultID(time.Now()), Type: run.Type}
	if h, ok := e.results.FindResumable(run.ID, time.Now().Add(-10*24*time.Hour)); ok {
		res = h // resume the SAME result — its already-done repos are skipped below
	}
	report(res.ResultID)
	done := res.DoneRepos()
	e.mu.Lock()
	for _, repo := range e.repos {
		if done[repo] {
			continue
		}
		e.worked = append(e.worked, repo)
		res.Repos = append(res.Repos, RepoResult{Repo: repo, OK: true})
	}
	e.mu.Unlock()
	res.FinishedAt = time.Now().UTC()
	res.OK = true
	_ = e.results.Save(res)
	return ResultRef{ResultID: res.ResultID, OK: true, RepoCount: len(res.Repos)}, nil
}
func (e *resumingExec) Maintain(context.Context) {}
func (e *resumingExec) workedRepos() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.worked...)
}

// TestInterruptedExecutionResumesAfterRestart proves requirement 4: a ToDo whose one-time DueAt was
// already consumed and whose execution was interrupted mid-sweep (repo "a" done, "b" not) resumes by
// itself after a restart, continuing AT THE SAME PLACE — the finished repo stays finished, only the
// half-done one is re-attempted — instead of lying unfinished forever.
func TestInterruptedExecutionResumesAfterRestart(t *testing.T) {
	store := seedStore(t, []Run{
		{ID: "q", Enabled: true, Type: TypeTodo, Task: "t", Targets: []Target{{Repo: "a"}, {Repo: "b"}}}, // no DueAt: the freeze case
	})
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(t.TempDir(), "res"))
	results := NewResults()
	// The stranded husk left behind by the interrupted execution: unfinished, repo "a" already done.
	if err := results.Save(Result{
		RunID: "q", ResultID: "res_prev", Type: TypeTodo,
		Repos: []RepoResult{{Repo: "a", OK: true}},
	}); err != nil {
		t.Fatal(err)
	}

	re := &resumingExec{results: results, repos: []string{"a", "b"}}
	s := NewScheduler(store, re, time.Second)
	s.logf = func(string, ...any) {}

	// Startup self-heal re-queues the interrupted ToDo even though its DueAt is gone.
	s.Reconcile(results, 10*24*time.Hour)
	if q, _, _ := store.Get("q"); !q.StartPending {
		t.Fatal("startup self-heal did not re-queue the interrupted ToDo")
	}

	// The next tick resumes it, skipping the finished repo and working only the half-done one.
	s.fireDue(context.Background())
	eventually(t, "the resume works only the half-done repo", func() bool {
		w := re.workedRepos()
		return len(w) == 1 && w[0] == "b"
	})
	eventually(t, "the resumed ToDo is marked done", func() bool { q, _, _ := store.Get("q"); return q.Done })
	q, _, _ := store.Get("q")
	if !q.Done {
		t.Error("the resumed ToDo should be marked done after completing")
	}
	if q.StartPending {
		t.Error("StartPending must be cleared once the resumed run starts")
	}
}

// eventually waits briefly for a condition: with concurrent runs a start is asynchronous, so asserting
// immediately after fireDue would test the goroutine scheduler, not the behaviour.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", what)
}
