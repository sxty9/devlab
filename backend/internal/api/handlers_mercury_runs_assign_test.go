package api

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// Re-port of the pre-rebuild assigner suite against runs/plan.go (the ONE planning path,
// REQ-004): the background assigner reuses Catalog + UpsertPlannedRun + ComposeInto — the same
// machinery the interactive AI-fill uses — so these tests drive the pass through stubbed
// network seams and assert against the plain run store.

// assignFixture builds a Server backed by temp stores plus an assigner whose network seams are
// stubbed by each test.
func assignFixture(t *testing.T) (*Server, *autoAssigner) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(dir, "notices.json"))
	s := &Server{runs: runs.NewStore(nil), runNotices: runs.NewNoticeStore(nil)}
	a := &autoAssigner{s: s, delay: 0}
	s.assigner = a
	return s, a
}

func assignAx(id, titel string) mercury.RunAxiom {
	return mercury.RunAxiom{ID: id, Titel: titel, Body: "body of " + id}
}

func catalogOf(rules []mercury.RunAxiom, axs ...mercury.RunAxiom) runs.Catalog {
	cat := runs.Catalog{ByID: map[string]mercury.RunAxiom{}, Laufregeln: rules}
	for _, a := range axs {
		cat.ByID[a.ID] = a
	}
	return cat
}

func dailyPlan(name string, ids ...string) mercury.RunPlan {
	return mercury.RunPlan{Runs: []mercury.PlannedRun{{
		Name: name, AxiomIDs: ids, Schedule: mercury.PlanSchedule{Kind: "daily", TimeOfDay: "03:00"},
	}}}
}

func seedRuns(t *testing.T, s *Server, rs ...runs.Run) {
	t.Helper()
	if _, err := s.runs.Patch(func([]runs.Run) ([]runs.Run, error) {
		return append([]runs.Run(nil), rs...), nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A brand-new (uncovered) axiom is assigned to a fresh run, its prompt snapshot composed IN THE
// SAME pass (axiom wording + run rules), and the user notified.
func TestAutoAssignCreatesRunAndNotice(t *testing.T) {
	s, a := assignFixture(t)
	cat := catalogOf([]mercury.RunAxiom{{ID: "lr1", Titel: "Run rule", Body: "Deliver honestly."}},
		assignAx("ax1", "Alpha"))
	a.catalog = func(context.Context, string) (runs.Catalog, error) { return cat, nil }
	a.plan = func(_ context.Context, _, _ string, uncovered []mercury.RunAxiom, _, _ []string) (mercury.RunPlan, error) {
		if len(uncovered) != 1 || uncovered[0].ID != "ax1" {
			t.Errorf("planner got uncovered=%v, want just ax1", uncovered)
		}
		return dailyPlan("Architecture", "ax1"), nil
	}

	a.runPass("cookie", "csrf", "tester")

	all, _ := s.runs.List()
	if len(all) != 1 || all[0].Title != "Architecture" {
		t.Fatalf("want one run 'Architecture', got %+v", all)
	}
	got := all[0]
	if got.Kind != model.KindAuto || len(got.AxiomIDs) != 1 || got.AxiomIDs[0] != "ax1" {
		t.Errorf("run = kind %q axioms %v, want auto + [ax1]", got.Kind, got.AxiomIDs)
	}
	if !got.Active || got.Schedule == nil || got.Schedule.TimeOfDay != "03:00" {
		t.Errorf("run must be active and scheduled: active=%v schedule=%+v", got.Active, got.Schedule)
	}
	// REQ-004: the prompt is recomposed in the same step — full axiom wording plus the run rules.
	if !strings.Contains(got.PromptSnapshot, "body of ax1") || !strings.Contains(got.PromptSnapshot, "Deliver honestly.") {
		t.Errorf("snapshot must carry the axiom wording and the run rules:\n%s", got.PromptSnapshot)
	}
	if got.PromptInputHash == "" {
		t.Error("composed run must carry its input hash")
	}
	notes, _ := s.runNotices.List()
	if len(notes) != 1 {
		t.Fatalf("want 1 notice, got %d", len(notes))
	}
	n := notes[0]
	if n.Kind != runs.NoticeAssigned || !n.NewRun || n.RunName != "Architecture" || n.RunID != got.ID {
		t.Errorf("notice = %+v, want assigned/new/Architecture/%s", n, got.ID)
	}
	if len(n.Axioms) != 1 || n.Axioms[0] != "Alpha" {
		t.Errorf("notice titles = %v, want [Alpha]", n.Axioms)
	}
}

// The planner may fold an uncovered axiom into an EXISTING run by name — the assigner extends it
// in place through the shared UpsertPlannedRun (no duplicate run, snapshot recomposed).
func TestAutoAssignExtendsExistingRun(t *testing.T) {
	s, a := assignFixture(t)
	seedRuns(t, s, runs.Run{
		ID: "run_a", Kind: model.KindAuto, Title: "Architecture", Active: true,
		Schedule: &runs.ScheduleSpec{Kind: runs.Daily, TimeOfDay: "03:00"}, AxiomIDs: []string{"ax0"},
	})
	cat := catalogOf(nil, assignAx("ax0", "Zero"), assignAx("ax1", "Alpha"))
	a.catalog = func(context.Context, string) (runs.Catalog, error) { return cat, nil }
	a.plan = func(_ context.Context, _, _ string, _ []mercury.RunAxiom, existing, _ []string) (mercury.RunPlan, error) {
		if len(existing) != 1 || existing[0] != "Architecture" {
			t.Errorf("planner existing runs = %v, want [Architecture]", existing)
		}
		return dailyPlan("Architecture", "ax1"), nil // extend the existing run
	}

	a.runPass("cookie", "csrf", "tester")

	all, _ := s.runs.List()
	if len(all) != 1 {
		t.Fatalf("want the run extended in place (1 run), got %d", len(all))
	}
	if len(all[0].AxiomIDs) != 2 {
		t.Errorf("run axioms = %v, want [ax0 ax1]", all[0].AxiomIDs)
	}
	if !strings.Contains(all[0].PromptSnapshot, "body of ax1") {
		t.Error("extension must recompose the snapshot in the same step")
	}
	notes, _ := s.runNotices.List()
	if len(notes) != 1 || notes[0].NewRun {
		t.Fatalf("want 1 notice with NewRun=false, got %+v", notes)
	}
}

// The three uncover triggers share ONE behavior: the pass recomputes the uncovered set from the
// stores' current truth, so (1) a new axiom, (2) a run deletion and (3) a history restore that
// drops coverage all converge on the same assignment — and a pass over a covered set is a no-op
// (idempotent, never a re-assignment).
func TestAutoAssignCoversAllThreeTriggers(t *testing.T) {
	s, a := assignFixture(t)
	cat := catalogOf(nil, assignAx("ax1", "Alpha"))
	a.catalog = func(context.Context, string) (runs.Catalog, error) { return cat, nil }
	var planCalls int
	a.plan = func(context.Context, string, string, []mercury.RunAxiom, []string, []string) (mercury.RunPlan, error) {
		planCalls++
		return dailyPlan("Coverage", "ax1"), nil
	}

	// (1) A new axiom with no run yet.
	a.runPass("cookie", "csrf", "tester")
	if all, _ := s.runs.List(); len(all) != 1 {
		t.Fatalf("trigger 1: want 1 run, got %d", len(all))
	}

	// Covered now — an intermediate pass must not plan again (idempotent).
	a.runPass("cookie", "csrf", "tester")
	if planCalls != 1 {
		t.Fatalf("covered set must not re-plan, plan calls = %d", planCalls)
	}

	// (2) The covering run is deleted (the delete handler kicks the assigner; here its effect).
	seedRuns(t, s) // empty
	a.runPass("cookie", "csrf", "tester")
	if all, _ := s.runs.List(); len(all) != 1 || planCalls != 2 {
		t.Fatalf("trigger 2: want re-assignment after deletion, runs=%d planCalls=%d", len(all), planCalls)
	}

	// (3) A history restore lands a constellation without the coverage.
	seedRuns(t, s, runs.Run{ID: "run_old", Kind: model.KindAuto, Title: "Old", Active: true, AxiomIDs: []string{"gone"}})
	a.runPass("cookie", "csrf", "tester")
	all, _ := s.runs.List()
	if len(all) != 2 || planCalls != 3 {
		t.Fatalf("trigger 3: want assignment after restore, runs=%d planCalls=%d", len(all), planCalls)
	}
}

// A planner failure leaves the axiom uncovered and records a visible failure notice with a short
// reason — never a raw log, and never a blocked write (the pass runs detached).
func TestAutoAssignFailureNotice(t *testing.T) {
	s, a := assignFixture(t)
	a.catalog = func(context.Context, string) (runs.Catalog, error) {
		return catalogOf(nil, assignAx("ax1", "Alpha")), nil
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
	if notes[0].Reason == "" || strings.Contains(notes[0].Reason, "502") {
		t.Errorf("reason must be short and human, not a raw log: %q", notes[0].Reason)
	}
	if len(notes[0].AxiomIDs) != 1 || notes[0].AxiomIDs[0] != "ax1" {
		t.Errorf("failure notice must name the axiom: %+v", notes[0])
	}
}

// A plan that references only unknown/already-covered ids is a genuine no-op: no run, no notice.
func TestAutoAssignHallucinatedIDsNoop(t *testing.T) {
	s, a := assignFixture(t)
	seedRuns(t, s, runs.Run{ID: "run_a", Kind: model.KindAuto, Title: "R", Active: true, AxiomIDs: []string{"ax1"}})
	a.catalog = func(context.Context, string) (runs.Catalog, error) {
		return catalogOf(nil, assignAx("ax1", "A"), assignAx("ax2", "B")), nil
	}
	a.plan = func(context.Context, string, string, []mercury.RunAxiom, []string, []string) (mercury.RunPlan, error) {
		return dailyPlan("Bogus", "does-not-exist", "ax1"), nil // hallucinated + already covered
	}

	a.runPass("cookie", "csrf", "tester")

	if all, _ := s.runs.List(); len(all) != 1 {
		t.Errorf("no spurious run: want the 1 pre-existing run, got %d", len(all))
	}
	if notes, _ := s.runNotices.List(); len(notes) != 0 {
		t.Errorf("hallucinated-only plan must record no notice, got %d", len(notes))
	}
}

// No session cookie → aigentic is unreachable, so the pass makes no attempt at all (silent skip).
func TestAutoAssignSkipsWithoutCookie(t *testing.T) {
	s, a := assignFixture(t)
	called := false
	a.catalog = func(context.Context, string) (runs.Catalog, error) {
		called = true
		return runs.Catalog{}, nil
	}
	a.runPass("", "", "tester")
	if called {
		t.Error("no cookie → the pass must not reach the catalog")
	}
	if notes, _ := s.runNotices.List(); len(notes) != 0 {
		t.Errorf("no-cookie skip must be silent, got %d notices", len(notes))
	}
}

// Several kicks in quick succession coalesce into ONE bundled assignment pass — and a kick never
// blocks the caller (the write handler returns while the pass is still pending).
func TestAutoAssignDebounceCoalesces(t *testing.T) {
	s, a := assignFixture(t)
	a.delay = 25 * time.Millisecond
	a.catalog = func(context.Context, string) (runs.Catalog, error) {
		return catalogOf(nil, assignAx("ax1", "Alpha")), nil
	}
	var planCalls int32
	a.plan = func(context.Context, string, string, []mercury.RunAxiom, []string, []string) (mercury.RunPlan, error) {
		atomic.AddInt32(&planCalls, 1)
		return dailyPlan("Architecture", "ax1"), nil
	}

	start := time.Now()
	for i := 0; i < 4; i++ {
		a.kick("cookie", "csrf", "tester")
	}
	if d := time.Since(start); d > a.delay {
		t.Fatalf("kicks must return immediately (non-blocking), took %v", d)
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
