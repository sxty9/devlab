package api

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// assignFixture builds a Server backed by temp stores plus an assigner whose network seams are stubbed.
func assignFixture(t *testing.T) (*Server, *autoAssigner) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(dir, "notices.json"))
	s := &Server{runs: runs.NewStore(), runNotices: runs.NewNoticeStore()}
	a := &autoAssigner{s: s, delay: 0}
	s.assigner = a
	return s, a
}

func ax(id, titel string) mercury.RunAxiom {
	return mercury.RunAxiom{ID: id, Titel: titel, Body: "body of " + id}
}

func dailyPlan(name string, ids ...string) mercury.RunPlan {
	return mercury.RunPlan{Runs: []mercury.PlannedRun{{
		Name: name, AxiomIDs: ids, Schedule: mercury.PlanSchedule{Kind: "daily", TimeOfDay: "03:00"},
	}}}
}

// A brand-new (uncovered) axiom is assigned to a fresh run, its snapshot composed, and the user notified.
func TestAutoAssignCreatesRunAndNotice(t *testing.T) {
	s, a := assignFixture(t)
	byID := map[string]mercury.RunAxiom{"ax1": ax("ax1", "Alpha")}
	a.catalog = func(context.Context, string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error) {
		return byID, nil, nil
	}
	a.plan = func(_ context.Context, _, _ string, uncovered []mercury.RunAxiom, _, _ []string) (mercury.RunPlan, error) {
		if len(uncovered) != 1 || uncovered[0].ID != "ax1" {
			t.Errorf("planner got uncovered=%v, want just ax1", uncovered)
		}
		return dailyPlan("Architektur", "ax1"), nil
	}

	a.runPass("cookie", "csrf", "tester")

	all, _ := s.runs.List()
	if len(all) != 1 || all[0].Name != "Architektur" {
		t.Fatalf("want one run 'Architektur', got %+v", all)
	}
	got := all[0]
	if len(got.AxiomIDs) != 1 || got.AxiomIDs[0] != "ax1" {
		t.Errorf("run axioms = %v, want [ax1]", got.AxiomIDs)
	}
	if !got.Enabled || got.NextFireAt == nil || got.Prompt == "" {
		t.Errorf("run must be enabled, scheduled and composed: enabled=%v next=%v promptLen=%d", got.Enabled, got.NextFireAt, len(got.Prompt))
	}
	notes, _ := s.runNotices.List()
	if len(notes) != 1 {
		t.Fatalf("want 1 notice, got %d", len(notes))
	}
	n := notes[0]
	if n.Kind != runs.NoticeAssigned || !n.NewRun || n.RunName != "Architektur" || n.RunID != got.ID {
		t.Errorf("notice = %+v, want assigned/new/Architektur/%s", n, got.ID)
	}
	if len(n.Axioms) != 1 || n.Axioms[0] != "Alpha" {
		t.Errorf("notice titles = %v, want [Alpha]", n.Axioms)
	}
}

// The planner may fold an uncovered axiom into an EXISTING run by name — the assigner extends it in place.
func TestAutoAssignExtendsExistingRun(t *testing.T) {
	s, a := assignFixture(t)
	if _, err := s.runs.Mutate("create", "t", func(cur []runs.Run) ([]runs.Run, error) {
		return append(cur, runs.Run{
			ID: "run_a", Name: "Architektur", Type: runs.TypeAuto, Enabled: true,
			Schedule: runs.Schedule{Kind: runs.Daily, TimeOfDay: "03:00"}, AxiomIDs: []string{"ax0"},
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	byID := map[string]mercury.RunAxiom{"ax0": ax("ax0", "Zero"), "ax1": ax("ax1", "Alpha")}
	a.catalog = func(context.Context, string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error) {
		return byID, nil, nil
	}
	a.plan = func(_ context.Context, _, _ string, _ []mercury.RunAxiom, existing, _ []string) (mercury.RunPlan, error) {
		if len(existing) != 1 || existing[0] != "Architektur" {
			t.Errorf("planner existing runs = %v, want [Architektur]", existing)
		}
		return dailyPlan("Architektur", "ax1"), nil // extend the existing run
	}

	a.runPass("cookie", "csrf", "tester")

	all, _ := s.runs.List()
	if len(all) != 1 {
		t.Fatalf("want the run extended in place (1 run), got %d", len(all))
	}
	if len(all[0].AxiomIDs) != 2 {
		t.Errorf("run axioms = %v, want [ax0 ax1]", all[0].AxiomIDs)
	}
	notes, _ := s.runNotices.List()
	if len(notes) != 1 || notes[0].NewRun {
		t.Fatalf("want 1 notice with NewRun=false, got %+v", notes)
	}
}

// Idempotent: with every axiom already covered, the pass never plans and never writes.
func TestAutoAssignNoopWhenCovered(t *testing.T) {
	s, a := assignFixture(t)
	if _, err := s.runs.Mutate("create", "t", func(cur []runs.Run) ([]runs.Run, error) {
		return append(cur, runs.Run{ID: "run_a", Name: "R", Type: runs.TypeAuto, Enabled: true, AxiomIDs: []string{"ax1"}}), nil
	}); err != nil {
		t.Fatal(err)
	}
	a.catalog = func(context.Context, string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error) {
		return map[string]mercury.RunAxiom{"ax1": ax("ax1", "Alpha")}, nil, nil
	}
	planned := false
	a.plan = func(context.Context, string, string, []mercury.RunAxiom, []string, []string) (mercury.RunPlan, error) {
		planned = true
		return mercury.RunPlan{}, nil
	}

	a.runPass("cookie", "csrf", "tester")

	if planned {
		t.Error("planner must not be called when nothing is uncovered")
	}
	if notes, _ := s.runNotices.List(); len(notes) != 0 {
		t.Errorf("no-op pass must record no notice, got %d", len(notes))
	}
}

// A planner failure leaves the axiom uncovered and records a visible failure notice — without a raw log.
func TestAutoAssignFailureNotice(t *testing.T) {
	s, a := assignFixture(t)
	a.catalog = func(context.Context, string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error) {
		return map[string]mercury.RunAxiom{"ax1": ax("ax1", "Alpha")}, nil, nil
	}
	a.plan = func(context.Context, string, string, []mercury.RunAxiom, []string, []string) (mercury.RunPlan, error) {
		return mercury.RunPlan{}, errors.New("aigentic: 502 Bad Gateway")
	}

	a.runPass("cookie", "csrf", "tester")

	if all, _ := s.runs.List(); len(all) != 0 {
		t.Errorf("failed pass must not create a run, got %d", len(all))
	}
	notes, _ := s.runNotices.List()
	if len(notes) != 1 || notes[0].Kind != runs.NoticeFailed {
		t.Fatalf("want 1 failure notice, got %+v", notes)
	}
	if notes[0].Reason == "" || len(notes[0].AxiomIDs) != 1 || notes[0].AxiomIDs[0] != "ax1" {
		t.Errorf("failure notice = %+v, want a reason and ax1", notes[0])
	}
}

// A plan that references only unknown/already-covered ids is a genuine no-op: no run, no notice, no snapshot.
func TestAutoAssignHallucinatedIDsNoop(t *testing.T) {
	s, a := assignFixture(t)
	if _, err := s.runs.Mutate("create", "t", func(cur []runs.Run) ([]runs.Run, error) {
		return append(cur, runs.Run{ID: "run_a", Name: "R", Type: runs.TypeAuto, Enabled: true, AxiomIDs: []string{"ax1"}}), nil
	}); err != nil {
		t.Fatal(err)
	}
	a.catalog = func(context.Context, string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error) {
		return map[string]mercury.RunAxiom{"ax1": ax("ax1", "A"), "ax2": ax("ax2", "B")}, nil, nil
	}
	a.plan = func(context.Context, string, string, []mercury.RunAxiom, []string, []string) (mercury.RunPlan, error) {
		return dailyPlan("Bogus", "does-not-exist"), nil // model hallucinated an id
	}

	a.runPass("cookie", "csrf", "tester")

	if all, _ := s.runs.List(); len(all) != 1 {
		t.Errorf("no spurious run: want the 1 pre-existing run, got %d", len(all))
	}
	if notes, _ := s.runNotices.List(); len(notes) != 0 {
		t.Errorf("hallucinated-only plan must record no notice, got %d", len(notes))
	}
}

// No session cookie → aigentic is unreachable, so the pass makes no attempt at all (no noisy failure).
func TestAutoAssignSkipsWithoutCookie(t *testing.T) {
	s, a := assignFixture(t)
	called := false
	a.catalog = func(context.Context, string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error) {
		called = true
		return nil, nil, nil
	}
	a.runPass("", "", "tester")
	if called {
		t.Error("no cookie → the pass must not reach the catalog")
	}
	if notes, _ := s.runNotices.List(); len(notes) != 0 {
		t.Errorf("no-cookie skip must be silent, got %d notices", len(notes))
	}
}

// Several kicks in quick succession coalesce into ONE bundled assignment pass.
func TestAutoAssignDebounceCoalesces(t *testing.T) {
	s, a := assignFixture(t)
	a.delay = 25 * time.Millisecond
	a.catalog = func(context.Context, string) (map[string]mercury.RunAxiom, []mercury.RunAxiom, error) {
		return map[string]mercury.RunAxiom{"ax1": ax("ax1", "Alpha")}, nil, nil
	}
	var planCalls int32
	var mu sync.Mutex // serialize the (here trivial) plan body across the AfterFunc goroutine
	a.plan = func(context.Context, string, string, []mercury.RunAxiom, []string, []string) (mercury.RunPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		atomic.AddInt32(&planCalls, 1)
		return dailyPlan("Architektur", "ax1"), nil
	}

	for i := 0; i < 4; i++ {
		a.kick("cookie", "csrf", "tester")
	}
	if !a.Pending() {
		t.Fatal("Pending() must be true right after a kick")
	}

	deadline := time.Now().Add(2 * time.Second)
	for a.Pending() {
		if time.Now().After(deadline) {
			t.Fatal("assignment did not settle in time")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&planCalls); got != 1 {
		t.Errorf("four kicks must coalesce to one plan call, got %d", got)
	}
	if all, _ := s.runs.List(); len(all) != 1 {
		t.Errorf("want exactly one run created, got %d", len(all))
	}
}
