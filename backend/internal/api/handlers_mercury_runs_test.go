package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/axiomrepo"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/statepath"
)

// seedRunsRemote builds a bare "remote" constitution repo carrying n axioms (ids ax_01…) and
// one global run rule, and returns the remote's path plus the minted axiom ids.
func seedRunsRemote(t *testing.T, n int) (string, []string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, "", "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	runGit(t, "", "git", "clone", "--quiet", remote, seed)

	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("ax_%02d", i)
		ids = append(ids, id)
		p := filepath.Join(seed, "axiome", "general", id+".md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("---\nid: %s\ntitel: Axiom %02d\n---\nBody of axiom %02d.\n", id, i, i)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rule := filepath.Join(seed, "laeufe", "regeln", "work-incrementally.md")
	if err := os.MkdirAll(filepath.Dir(rule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rule, []byte("---\nid: lr_01\ntitel: Work incrementally\n---\nOnly commits since the last check.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "git", "add", "-A")
	runGit(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "seed")
	runGit(t, seed, "git", "push", "--quiet", "origin", "HEAD:main")
	return remote, ids
}

// newRunsTestServer wires a Server with a real run store and a seeded constitution repo.
func newRunsTestServer(t *testing.T, axiomCount int) (*Server, []string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	remote, ids := seedRunsRemote(t, axiomCount)
	s := &Server{
		runs:   runs.NewStore(nil),
		axioms: axiomrepo.New(filepath.Join(dir, "work"), remote, func() (string, error) { return "", nil }),
	}
	return s, ids
}

func doJSON(t *testing.T, h http.HandlerFunc, method, target, user, pathID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, target, &buf)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &auth.User{Username: user}))
	if pathID != "" {
		req.SetPathValue("id", pathID)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// The seven legacy auto runs (A appendix) are expressible on the new machinery (REQ-005.3):
// weekly rhythm, their axiom counts, active — created through the real handler, snapshot
// composed in the same pass.
func TestSevenLegacyRunsCreatable(t *testing.T) {
	legacy := []struct {
		title  string
		axioms int
	}{
		{"SDK & reusability", 7},
		{"Code structure & repo hygiene", 9},
		{"Service interface completeness", 7},
		{"UI/UX interaction standards", 7},
		{"AI integration & access control", 5},
		{"Mobile & native platform", 5},
		{"Axiom quality & internationalization", 5},
	}
	total := 0
	for _, l := range legacy {
		total += l.axioms
	}
	s, ids := newRunsTestServer(t, total) // 45 axioms — the legacy constitution's size
	next := 0
	for _, l := range legacy {
		in := map[string]any{
			"kind": "auto", "title": l.title, "active": true,
			"schedule": map[string]any{"kind": "weekly", "timeOfDay": "03:00", "weekdays": []int{1}},
			"axiomIds": ids[next : next+l.axioms],
		}
		next += l.axioms
		rec := doJSON(t, s.runCreate, http.MethodPost, "/api/mercury/runs", "operator", "", in)
		if rec.Code != http.StatusOK {
			t.Fatalf("create %q: status %d: %s", l.title, rec.Code, rec.Body)
		}
		var created runs.Run
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if created.Kind != model.KindAuto || !created.Active || created.Schedule == nil || created.Schedule.Kind != runs.Weekly {
			t.Fatalf("%q: not a weekly active auto run: %+v", l.title, created)
		}
		if len(created.AxiomIDs) != l.axioms {
			t.Fatalf("%q: %d axioms stored, want %d", l.title, len(created.AxiomIDs), l.axioms)
		}
		if created.PromptSnapshot == "" || created.PromptInputHash == "" {
			t.Fatalf("%q: snapshot must be composed on create", l.title)
		}
	}
	all, err := s.runs.List()
	if err != nil || len(all) != len(legacy) {
		t.Fatalf("stored %d runs, want %d (%v)", len(all), len(legacy), err)
	}
}

// The input validation of the ONE parse target: both kinds on one machinery, each with its
// structural minimum; the three tunables guarded at the same access point.
func TestValidateRunInput(t *testing.T) {
	cat := runs.Catalog{ByID: map[string]mercury.RunAxiom{"ax_a": {ID: "ax_a"}}}
	sched := &runs.ScheduleSpec{Kind: runs.Daily, TimeOfDay: "03:00"}
	neg := model.Duration(-time.Hour)
	zero := model.Duration(0)

	cases := []struct {
		name string
		in   runs.RunInput
		code int // 0 = valid
	}{
		{"auto ok", runs.RunInput{Title: "A", Kind: model.KindAuto, Schedule: sched, AxiomIDs: []string{"ax_a"}}, 0},
		{"kind defaults to auto", runs.RunInput{Title: "A", Schedule: sched, AxiomIDs: []string{"ax_a"}}, 0},
		{"todo ok", runs.RunInput{Title: "T", Kind: model.KindTodo, Task: "do", Targets: []runs.Target{{Repo: "svc"}}}, 0},
		{"title required", runs.RunInput{Kind: model.KindTodo, Task: "do", Targets: []runs.Target{{Repo: "svc"}}}, http.StatusBadRequest},
		{"todo needs a task", runs.RunInput{Title: "T", Kind: model.KindTodo, Targets: []runs.Target{{Repo: "svc"}}}, http.StatusBadRequest},
		{"todo needs a target", runs.RunInput{Title: "T", Kind: model.KindTodo, Task: "do"}, http.StatusBadRequest},
		{"auto needs a schedule", runs.RunInput{Title: "A", Kind: model.KindAuto, AxiomIDs: []string{"ax_a"}}, http.StatusBadRequest},
		{"auto needs an axiom", runs.RunInput{Title: "A", Kind: model.KindAuto, Schedule: sched}, http.StatusBadRequest},
		{"unknown axiom refused", runs.RunInput{Title: "A", Kind: model.KindAuto, Schedule: sched, AxiomIDs: []string{"ax_ghost"}}, http.StatusBadRequest},
		{"bogus kind refused", runs.RunInput{Title: "A", Kind: "cron", Schedule: sched, AxiomIDs: []string{"ax_a"}}, http.StatusBadRequest},
		{"ultracode allowed", runs.RunInput{Title: "T", Kind: model.KindTodo, Task: "do", Targets: []runs.Target{{Repo: "svc"}}, Tuning: &runs.Tuning{Effort: "ultracode"}}, 0},
		{"foreign effort refused", runs.RunInput{Title: "T", Kind: model.KindTodo, Task: "do", Targets: []runs.Target{{Repo: "svc"}}, Tuning: &runs.Tuning{Effort: "extreme"}}, http.StatusBadRequest},
		{"model smuggle refused", runs.RunInput{Title: "T", Kind: model.KindTodo, Task: "do", Targets: []runs.Target{{Repo: "svc"}}, Tuning: &runs.Tuning{Model: "opus --dangerously"}}, http.StatusBadRequest},
		{"negative budget refused", runs.RunInput{Title: "T", Kind: model.KindTodo, Task: "do", Targets: []runs.Target{{Repo: "svc"}}, Tuning: &runs.Tuning{TimeBudget: &neg}}, http.StatusBadRequest},
		{"zero budget (no budget) allowed", runs.RunInput{Title: "T", Kind: model.KindTodo, Task: "do", Targets: []runs.Target{{Repo: "svc"}}, Tuning: &runs.Tuning{TimeBudget: &zero}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := c.in
			code, msg := validateRunInput(&in, cat)
			if code != c.code {
				t.Fatalf("code = %d (%s), want %d", code, msg, c.code)
			}
		})
	}
}

// Multi-target parsing (REQ-006.1): every named repo survives, duplicates collapse in order,
// a to-be-created repo's name is bounded, and an empty list is refused.
func TestNormalizeTargets(t *testing.T) {
	got, code, _ := normalizeTargets([]runs.Target{{Repo: " a "}, {Repo: "b", Create: true}, {Repo: "a"}, {Repo: "c"}})
	if code != 0 || len(got) != 3 || got[0].Repo != "a" || got[1].Repo != "b" || !got[1].Create || got[2].Repo != "c" {
		t.Fatalf("normalizeTargets = %+v (code %d)", got, code)
	}
	if _, code, _ := normalizeTargets(nil); code != http.StatusBadRequest {
		t.Fatal("an empty target list must be refused")
	}
	if _, code, _ := normalizeTargets([]runs.Target{{Repo: "../evil", Create: true}}); code != http.StatusBadRequest {
		t.Fatal("a traversal-shaped new-repo name must be refused")
	}
	if _, code, _ := normalizeTargets([]runs.Target{{Repo: ""}}); code != http.StatusBadRequest {
		t.Fatal("a blank target must be refused")
	}
}

// The calendar is the union of upcoming firings and stored past executions (REQ-012), from the
// SAME source the history reads — with ?type scoping each surface.
func TestRunsCalendarUnion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	s := &Server{runs: runs.NewStore(nil), results: runs.NewResultStore(&statepath.Paths{Root: dir})}

	due := time.Now().Add(24 * time.Hour).UTC()
	seedRuns := []runs.Run{
		{ID: "run_auto1", Kind: model.KindAuto, Title: "Sweep", Active: true,
			Schedule: &runs.ScheduleSpec{Kind: runs.Daily, TimeOfDay: "03:00"}},
		{ID: "run_todo1", Kind: model.KindTodo, Title: "Fix", Task: "x", DueAt: &due,
			Targets: []runs.Target{{Repo: "svc"}}},
	}
	for _, r := range seedRuns {
		if err := s.runs.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	ended := time.Now().Add(-time.Hour).UTC()
	past := runs.Result{
		ID: runs.NewResultID(ended), RunID: "run_todo1", RunTitle: "Fix", Kind: model.KindTodo,
		StartedAt: ended, EndedAt: &ended,
		Repos: []model.RepoPipeline{{Repo: "svc", Stages: []model.StageView{{Stage: model.StageImplement, State: model.StepExecuted}}}},
	}
	if err := s.results.Put(past); err != nil {
		t.Fatal(err)
	}

	fetch := func(query string) model.RunCalendar {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/mercury/runs/calendar"+query, nil)
		rec := httptest.NewRecorder()
		s.runsCalendar(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("calendar: %d %s", rec.Code, rec.Body)
		}
		var cal model.RunCalendar
		if err := json.Unmarshal(rec.Body.Bytes(), &cal); err != nil {
			t.Fatal(err)
		}
		return cal
	}

	cal := fetch("?days=7")
	var future, pastN, todoDue int
	for _, o := range cal.Occurrences {
		switch {
		case o.ResultID != "":
			pastN++
			if o.Succeeded == nil || !*o.Succeeded {
				t.Errorf("past occurrence must carry its honest success state: %+v", o)
			}
		case o.Kind == model.KindTodo:
			todoDue++
		default:
			future++
		}
	}
	if future == 0 || pastN != 1 || todoDue != 1 {
		t.Fatalf("union incomplete: future=%d past=%d todoDue=%d (%d total)", future, pastN, todoDue, len(cal.Occurrences))
	}

	// ?type scopes per surface: the todo calendar carries no auto firings.
	for _, o := range fetch("?days=7&type=todo").Occurrences {
		if o.Kind != model.KindTodo {
			t.Fatalf("type=todo leaked a %s occurrence: %+v", o.Kind, o)
		}
	}
}

// The execution history endpoint scopes by kind and resolves unstamped (legacy) results via
// their still-existing run — newest first, straight off the result pool.
func TestRunsExecutionsTypeScoping(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	s := &Server{runs: runs.NewStore(nil), results: runs.NewResultStore(&statepath.Paths{Root: dir})}

	if err := s.runs.Put(runs.Run{ID: "run_t", Kind: model.KindTodo, Title: "T", Task: "x"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.results.Put(runs.Result{ID: runs.NewResultID(now), RunID: "run_a", Kind: model.KindAuto, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Unstamped result: kind resolves through the run store (a legacy document).
	if err := s.results.Put(runs.Result{ID: runs.NewResultID(now.Add(time.Second)), RunID: "run_t", StartedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mercury/runs/executions?type=todo", nil)
	rec := httptest.NewRecorder()
	s.runsExecutions(rec, req)
	var out struct {
		Executions []runs.Result `json:"executions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Executions) != 1 || out.Executions[0].RunID != "run_t" || out.Executions[0].Kind != model.KindTodo {
		t.Fatalf("type=todo must yield exactly the resolved todo execution: %+v", out.Executions)
	}
}
