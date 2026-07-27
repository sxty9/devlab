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

func TestSchedulerActiveReflectsRunningRun(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	store := seedStore(t, []Run{
		{ID: "r", Enabled: true, Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, NextFireAt: &past, AxiomIDs: []string{"x"}},
	})
	be := &blockingExec{reported: make(chan string, 1), release: make(chan struct{})}
	s := NewScheduler(store, be, time.Second)
	s.logf = func(string, ...any) {}

	if a := s.Active(); a != nil {
		t.Fatalf("expected no activity before firing, got %+v", a)
	}
	if s.FireNow("r", "t") != StartFired {
		t.Fatal("FireNow did not fire")
	}
	<-be.reported // the run has reported its result id and is now blocked mid-execution

	a := s.Active()
	if a == nil || a.RunID != "r" || a.ResultID != "res_live" {
		t.Fatalf("Active() did not reflect the running run: %+v", a)
	}
	if a.StartedAt.IsZero() {
		t.Error("Active().StartedAt not stamped")
	}

	close(be.release) // let the run finish → Active clears
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.Active() == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Active() did not clear after the run finished")
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
	s.logf = func(string, ...any) {}

	s.fireDue(context.Background())

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
	s.logf = func(string, ...any) {}

	// Fire 1 → suspends.
	s.fireDue(context.Background())
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
	s.logf = func(string, ...any) {}

	if s.FireNow("m", "tester") != StartFired {
		t.Fatal("FireNow returned busy on an idle scheduler")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fe.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if fe.count() != 1 {
		t.Fatalf("expected exactly 1 execution, got %d", fe.count())
	}
	// give the detached goroutine a moment to finish its result-attach patch
	time.Sleep(20 * time.Millisecond)
	m, _, _ := store.Get("m")
	if m.NextFireAt == nil || !m.NextFireAt.Equal(future) {
		t.Errorf("run-now must NOT advance the schedule; got %v want %v", m.NextFireAt, future)
	}
}
