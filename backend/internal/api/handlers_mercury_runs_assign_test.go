package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

// A kick without a session is refused instead of scheduled: the pass authenticates as the caller,
// so there is nothing it could do — and arming the debounce anyway would make the coverage view
// announce an assignment that then silently does nothing.
func TestKickWithoutASessionIsRefused(t *testing.T) {
	_, a := assignFixture(t)
	a.delay = time.Hour
	if a.kick("", "csrf", "tester") {
		t.Error("a kick without a session cookie must report that nothing was scheduled")
	}
	if a.Pending() {
		t.Error("a refused kick must not leave the surface claiming an assignment is under way")
	}
	if !a.kick("cookie", "csrf", "tester") {
		t.Fatal("a kick WITH a session must schedule a pass")
	}
	if !a.Pending() {
		t.Error("Pending() must be true after a session-bearing kick")
	}
}

// The plan may only extend runs that can CARRY axioms. A todo cannot (UpsertPlannedRun refuses to
// fold axioms into one), so offering its title to the planner invites a plan naming a run that can
// never be extended — and the apply then creates a second run under the same name.
func TestOnlyAutoRunsAreOfferedAsExtendable(t *testing.T) {
	got := extendableRunTitles([]runs.Run{
		{ID: "run_a", Kind: model.KindAuto, Title: "Architecture"},
		{ID: "run_b", Kind: model.KindTodo, Title: "Fix the login"},
		{ID: "run_c", Kind: model.KindAuto, Title: "Interfaces"},
	})
	if len(got) != 2 || got[0] != "Architecture" || got[1] != "Interfaces" {
		t.Errorf("extendable titles = %v, want the two auto runs", got)
	}
}

// The uncovered set is ONE definition and it is deterministic: map iteration is random, so an
// unsorted answer would give the planner a different prompt on every pass.
func TestUncoveredAxiomsIsSortedAndExcludesCovered(t *testing.T) {
	cat := catalogOf(nil, assignAx("ax_c", "C"), assignAx("ax_a", "A"), assignAx("ax_b", "B"))
	got := uncoveredAxioms(cat, []runs.Run{{ID: "run_a", Kind: model.KindAuto, AxiomIDs: []string{"ax_b"}}})
	if len(got) != 2 || got[0].ID != "ax_a" || got[1].ID != "ax_c" {
		t.Errorf("uncovered = %v, want [ax_a ax_c] in that order", got)
	}
	if len(uncoveredAxioms(runs.Catalog{ByID: map[string]mercury.RunAxiom{}}, nil)) != 0 {
		t.Error("an empty catalog leaves nothing uncovered")
	}
}

// ── the takeover dead end (REQ-004, REQ-040.1) ────────────────────────────────────────────────
//
// The state below is the one the cutover actually produced: seven recurring runs taken over WITHOUT
// axioms, and a constitution whose axioms no run carries. It was a closed circle, walled in three
// places:
//
//	1. the run could not be saved       every axiom-less auto run was refused, so putting a
//	                                    taken-over run back UNCHANGED answered 400.
//	2. nothing could start a pass       the assignment was kicked from exactly one place — the
//	                                    creation of a NEW axiom — and no access started it.
//	3. no other axiom write kicked      editing or re-filing an axiom left the assignment alone
//	                                    even when it added a member to the catalog.
//
// So the runs waited for axioms, the axioms waited for the assignment, and the assignment waited
// for an axiom nobody wanted to write. Each test below fails on the pre-fix code and names which
// wall it pins.

// takenOverRuns is the shape the migration leaves behind: recurring runs, scheduled, carrying NO
// axioms and therefore inactive — a definition the scheduler can never fire (sched.IsDue).
func takenOverRuns() []runs.Run {
	titles := []string{
		"SDK & reusability", "Code structure & repo hygiene", "Service interface completeness",
		"UI/UX interaction standards", "AI integration & access control", "Mobile & native platform",
		"Axiom quality & internationalization",
	}
	out := make([]runs.Run, 0, len(titles))
	for _, title := range titles {
		out = append(out, runs.Run{
			ID: runs.NewID(), Kind: model.KindAuto, Title: title, Active: false,
			Schedule: &runs.ScheduleSpec{Kind: runs.Weekly, TimeOfDay: "03:00", Weekdays: []time.Weekday{time.Monday}},
		})
	}
	return out
}

// deadEndFixture reproduces the whole situation on the PRODUCTION wiring — newAutoAssigner, the real
// axiom store, the real run and notice pools — with only the model call left to the test.
func deadEndFixture(t *testing.T, axiomCount int) (*Server, *autoAssigner, []string) {
	t.Helper()
	s, ids := newRunsTestServer(t, axiomCount)
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(t.TempDir(), "notices.json"))
	s.runNotices = runs.NewNoticeStore(nil)
	a := newAutoAssigner(s)
	a.delay = 0
	s.assigner = a
	seedRuns(t, s, takenOverRuns()...)
	return s, a, ids
}

func decodeBody(t *testing.T, raw []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("undecodable answer %s: %v", raw, err)
	}
}

// settle waits until the assigner has neither a scheduled nor a running pass.
func settle(t *testing.T, a *autoAssigner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Pending() {
		if time.Now().After(deadline) {
			t.Fatal("the assignment did not settle in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// WALL 1 — the taken-over run is an operable state. Saving it back unchanged must work; only
// ACTIVATING it while it carries no axiom stays refused, and the refusal names the way out.
func TestTakenOverRunWithoutAxiomsIsSavableButNotActivatable(t *testing.T) {
	s, a, ids := deadEndFixture(t, 3)
	// Saving a run kicks the assignment; this test is about the SAVE, so the pass is only armed,
	// never run — a pass writing into the fixture while it is torn down proves nothing.
	a.delay = time.Hour
	all, err := s.runs.List()
	if err != nil || len(all) != 7 {
		t.Fatalf("fixture = %d runs (%v), want the seven taken-over ones", len(all), err)
	}
	run := all[0]

	// Exactly what the surface sends when the user opens the run and presses save: its own values.
	save := func(active bool, axiomIDs []string) *httptest.ResponseRecorder {
		return doJSON(t, s.runUpdate, http.MethodPut, "/api/mercury/runs/"+run.ID, "operator", run.ID,
			map[string]any{
				"kind": string(run.Kind), "title": run.Title, "active": active,
				"schedule": run.Schedule, "axiomIds": axiomIDs,
			})
	}

	rec := save(false, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("an inactive, axiom-less run must be savable: %d %s", rec.Code, rec.Body.String())
	}
	after, _, _ := s.runs.Get(run.ID)
	if len(after.AxiomIDs) != 0 || after.Active {
		t.Errorf("the save must change nothing: axioms=%v active=%v", after.AxiomIDs, after.Active)
	}

	// The rule that stays in force: an ACTIVE auto run with no axiom would enforce nothing.
	rec = save(true, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("activating an axiom-less run must be refused, got %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "inactive") || !strings.Contains(body, "axiom") {
		t.Errorf("the refusal must name the way out (save inactive, assign axioms): %s", body)
	}

	// And with axioms the very same activation goes through — the transition the dead end blocked.
	if rec = save(true, ids[:1]); rec.Code != http.StatusOK {
		t.Fatalf("activating a run that carries an axiom: %d %s", rec.Code, rec.Body.String())
	}
	if after, _, _ = s.runs.Get(run.ID); !after.Active || len(after.AxiomIDs) != 1 {
		t.Errorf("run after activation = active %v axioms %v", after.Active, after.AxiomIDs)
	}
}

// WALL 2 — the explicit trigger. From the dead-end state, one call to the new access assigns every
// uncovered axiom to the runs that are already there: no new run, no new axiom, and the seven
// taken-over runs become activatable.
func TestExplicitAssignmentLeavesTheDeadEnd(t *testing.T) {
	s, a, ids := deadEndFixture(t, 6)
	var offered []string
	a.plan = func(_ context.Context, _, _ string, uncovered []mercury.RunAxiom, existing, _ []string) (mercury.RunPlan, error) {
		offered = append([]string(nil), existing...)
		return mercury.RunPlan{Runs: []mercury.PlannedRun{{
			Name: "SDK & reusability", AxiomIDs: idsOf(uncovered),
			Schedule: mercury.PlanSchedule{Kind: "weekly", TimeOfDay: "03:00", Weekdays: []int{1}},
		}}}, nil
	}

	rec := doJSON(t, s.runsAssign, http.MethodPost, "/api/mercury/runs/assign", "operator", "", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("the explicit assignment answered %d %s", rec.Code, rec.Body.String())
	}
	var answer struct {
		Uncovered int  `json:"uncovered"`
		Started   bool `json:"started"`
	}
	decodeBody(t, rec.Body.Bytes(), &answer)
	if answer.Uncovered != len(ids) || !answer.Started {
		t.Fatalf("answer = %+v, want all %d uncovered and a started pass", answer, len(ids))
	}

	settle(t, a)

	// The taken-over runs were offered as extendable, and the plan extended one of them in place.
	if len(offered) != 7 {
		t.Errorf("the planner was offered %v, want the seven taken-over runs", offered)
	}
	all, _ := s.runs.List()
	if len(all) != 7 {
		t.Fatalf("the pass must extend the imported runs, not add an eighth: %d runs", len(all))
	}
	filled := 0
	for _, run := range all {
		filled += len(run.AxiomIDs)
	}
	if filled != len(ids) {
		t.Errorf("the runs carry %d axioms, want all %d", filled, len(ids))
	}

	// The surface the user reads agrees: nothing is uncovered any more.
	rec = doJSON(t, s.runsCoverage, http.MethodGet, "/api/mercury/runs/coverage", "operator", "", nil)
	var cov model.RunCoverage
	decodeBody(t, rec.Body.Bytes(), &cov)
	for _, id := range ids {
		if len(cov.Covered[id]) == 0 {
			t.Errorf("axiom %s is still uncovered after the explicit assignment", id)
		}
	}
	if notes, _ := s.runNotices.List(); len(notes) == 0 {
		t.Error("the pass must report what it did — the notice feed is empty")
	}

	// And the activation lock is open: the run now carries axioms, so it can be switched on.
	target := all[0]
	rec = doJSON(t, s.runUpdate, http.MethodPut, "/api/mercury/runs/"+target.ID, "operator", target.ID,
		map[string]any{
			"kind": string(target.Kind), "title": target.Title, "active": true,
			"schedule": target.Schedule, "axiomIds": target.AxiomIDs,
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("after the assignment the run must be activatable: %d %s", rec.Code, rec.Body.String())
	}
}

// The explicit trigger never claims work it is not doing: with everything covered it answers with a
// plain zero instead of announcing a pass.
func TestExplicitAssignmentIsHonestWhenThereIsNothingToDo(t *testing.T) {
	s, a, ids := deadEndFixture(t, 2)
	a.delay = time.Hour // an armed pass would be visible as Pending()
	seedRuns(t, s, runs.Run{
		ID: runs.NewID(), Kind: model.KindAuto, Title: "Everything", Active: true,
		Schedule: &runs.ScheduleSpec{Kind: runs.Daily, TimeOfDay: "03:00"}, AxiomIDs: ids,
	})

	rec := doJSON(t, s.runsAssign, http.MethodPost, "/api/mercury/runs/assign", "operator", "", map[string]any{})
	var answer struct {
		Uncovered int  `json:"uncovered"`
		Started   bool `json:"started"`
	}
	decodeBody(t, rec.Body.Bytes(), &answer)
	if answer.Uncovered != 0 || answer.Started {
		t.Errorf("answer = %+v, want nothing uncovered and no pass", answer)
	}
	if a.Pending() {
		t.Error("nothing to assign must leave no pass armed")
	}
}

// WALL 3 — the other axiom write paths. The question each is judged by is the same: can this write
// put an id into the catalog that no run carries? A path that can must kick; a path that cannot must
// not, because a kick makes the coverage view announce an assignment with nothing to do.
func TestAxiomWritePathsKickExactlyWhenCoverageCanChange(t *testing.T) {
	const withID = "---\nid: ax_seeded\ntitel: Seeded\n---\nSeeded wording.\n"
	const withoutID = "---\ntitel: Ancient\n---\nWritten before ids existed.\n"

	cases := []struct {
		name string
		seed map[string]string
		call func(t *testing.T, s *Server) *httptest.ResponseRecorder
		kick bool
		why  string
	}{
		{
			name: "an edit that mints an id",
			seed: map[string]string{"axiome/architecture/ancient.md": withoutID},
			call: func(t *testing.T, s *Server) *httptest.ResponseRecorder {
				return doJSON(t, s.editAxiom, http.MethodPut, "/api/mercury/axiom", "operator", "",
					map[string]any{"path": "axiome/architecture/ancient.md", "titel": "Ancient", "body": "Reworded."})
			},
			kick: true,
			why:  "the record becomes a catalog member for the first time, and no run carries it",
		},
		{
			name: "an edit that keeps the id",
			seed: map[string]string{"axiome/architecture/seeded.md": withID},
			call: func(t *testing.T, s *Server) *httptest.ResponseRecorder {
				return doJSON(t, s.editAxiom, http.MethodPut, "/api/mercury/axiom", "operator", "",
					map[string]any{"path": "axiome/architecture/seeded.md", "titel": "Seeded", "body": "Reworded."})
			},
			why: "coverage is keyed by the id, which the edit preserves — only the prompts change",
		},
		{
			name: "a move INTO the axiom namespace",
			seed: map[string]string{"regeln/process/seeded.md": withID},
			call: func(t *testing.T, s *Server) *httptest.ResponseRecorder {
				return doJSON(t, s.moveAxiom, http.MethodPost, "/api/mercury/move", "operator", "",
					map[string]any{"from": "regeln/process/seeded.md", "to": "axiome/process/seeded.md"})
			},
			kick: true,
			why:  "the id enters the catalog with the record",
		},
		{
			name: "a move inside the axiom namespace",
			seed: map[string]string{"axiome/architecture/seeded.md": withID},
			call: func(t *testing.T, s *Server) *httptest.ResponseRecorder {
				return doJSON(t, s.moveAxiom, http.MethodPost, "/api/mercury/move", "operator", "",
					map[string]any{"from": "axiome/architecture/seeded.md", "to": "axiome/interfaces/seeded.md"})
			},
			why: "the move carries the id along; the catalog membership is unchanged",
		},
		{
			name: "a move OUT of the axiom namespace",
			seed: map[string]string{"axiome/architecture/seeded.md": withID},
			call: func(t *testing.T, s *Server) *httptest.ResponseRecorder {
				return doJSON(t, s.moveAxiom, http.MethodPost, "/api/mercury/move", "operator", "",
					map[string]any{"from": "axiome/architecture/seeded.md", "to": "regeln/process/seeded.md"})
			},
			why: "a member leaves the catalog — the uncovered set can only shrink",
		},
		{
			name: "a delete",
			seed: map[string]string{"axiome/architecture/seeded.md": withID},
			call: func(t *testing.T, s *Server) *httptest.ResponseRecorder {
				return doJSON(t, s.deleteAxiom, http.MethodDelete,
					"/api/mercury/axiom?path=axiome/architecture/seeded.md", "operator", "", nil)
			},
			why: "deleting removes a member; nothing is left uncovered by it",
		},
		{
			name: "a category moved INTO the axiom namespace",
			seed: map[string]string{"regeln/process/seeded.md": withID},
			call: func(t *testing.T, s *Server) *httptest.ResponseRecorder {
				return doJSON(t, s.moveCategory, http.MethodPost, "/api/mercury/move-category", "operator", "",
					map[string]any{"from": "regeln/process", "to": "axiome/process"})
			},
			kick: true,
			why:  "every record beneath it enters the catalog",
		},
		{
			name: "a category moved inside the axiom namespace",
			seed: map[string]string{"axiome/architecture/seeded.md": withID},
			call: func(t *testing.T, s *Server) *httptest.ResponseRecorder {
				return doJSON(t, s.moveCategory, http.MethodPost, "/api/mercury/move-category", "operator", "",
					map[string]any{"from": "axiome/architecture", "to": "axiome/interfaces"})
			},
			why: "a rename inside the namespace changes no membership",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, a, _ := deadEndFixture(t, 1)
			a.delay = time.Hour // observe the ARMING, never a racing pass
			for path, content := range c.seed {
				if err := s.axioms.Put(context.Background(), path, content, "seed", "seed", false); err != nil {
					t.Fatal(err)
				}
			}
			if rec := c.call(t, s); rec.Code != http.StatusOK {
				t.Fatalf("the write itself failed: %d %s", rec.Code, rec.Body.String())
			}
			if got := a.Pending(); got != c.kick {
				t.Errorf("kicked = %v, want %v — %s", got, c.kick, c.why)
			}
		})
	}
}

func idsOf(axs []mercury.RunAxiom) []string {
	out := make([]string, 0, len(axs))
	for _, a := range axs {
		out = append(out, a.ID)
	}
	return out
}
