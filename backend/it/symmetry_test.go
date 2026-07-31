package it

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
)

// REQ-005 — the reconciliation list FUNCTION × KIND. Automatic runs and todos ride ONE machinery,
// so every function of the machinery must be available to both kinds; a deviation is admissible
// only where it is STRUCTURALLY inapplicable, and then it carries its reason here.
//
// The list is executed, not asserted on paper: each function is exercised against a real auto run
// and a real todo through the HTTP surface, and both answers must be honest.
func TestRunKindSymmetryReconciles(t *testing.T) {
	e := newEnvWithConstitution(t, sched.Config{Tick: time.Hour})
	e.boot(e.ctx)
	e.deps.repo("alpha")

	auto := e.createRun(t, map[string]any{
		"kind": "auto", "title": "Nightly consistency sweep",
		"axiomIds": []string{"ax_no_redundancy"},
		"schedule": map[string]any{"kind": "daily", "timeOfDay": "02:00"},
		"active":   true,
		"tuning":   map[string]any{"model": "opus", "effort": "high", "timeBudget": "2h"},
	})
	todo := e.createRun(t, map[string]any{
		"kind": "todo", "title": "Add the honest stage array", "task": "do the thing",
		"targets": []map[string]any{{"repo": "alpha"}},
		"dueAt":   time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		"tuning":  map[string]any{"model": "opus", "effort": "high", "timeBudget": "2h"},
	})
	byKind := map[model.RunKind]runs.Run{model.KindAuto: auto, model.KindTodo: todo}
	// Both kinds must answer the slot functions identically — collected here, compared at the end.
	deferSeen := map[model.RunKind]int{}
	resumeSeen := map[model.RunKind]int{}

	// The reconciliation list. `both` means the function must work for both kinds; a one-sided
	// function names the structural reason it cannot apply to the other.
	type fn struct {
		name     string
		kinds    []model.RunKind
		why      string // reason for a one-sided function ("" when both)
		exercise func(t *testing.T, r runs.Run)
	}
	list := []fn{
		{name: "create", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			if r.ID == "" || r.Kind == "" {
				t.Errorf("the run was not created with an identity and a kind: %+v", r)
			}
		}},
		{name: "read", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			var out struct {
				Run runs.Run `json:"run"`
			}
			e.getJSON("/api/mercury/runs/"+r.ID, &out)
			if out.Run.ID != r.ID {
				t.Errorf("read answered %q, want %q", out.Run.ID, r.ID)
			}
		}},
		{name: "update", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			body := map[string]any{"kind": string(r.Kind), "title": r.Title + " (edited)", "task": r.Task}
			if r.Kind == model.KindAuto {
				body["axiomIds"] = []string{"ax_no_redundancy"}
				body["schedule"] = map[string]any{"kind": "daily", "timeOfDay": "02:00"}
				body["active"] = true
			} else {
				body["targets"] = []map[string]any{{"repo": "alpha"}}
			}
			code, resp := e.do(e.request(http.MethodPut, "/api/mercury/runs/"+r.ID, body))
			if code != http.StatusOK {
				t.Errorf("update: %d %s", code, trim(resp))
			}
		}},
		{name: "list", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			var out struct {
				Runs []runs.Run `json:"runs"`
			}
			e.getJSON("/api/mercury/runs", &out)
			// ONE machinery: both kinds live in one pool, each stamped with its kind — which is
			// what lets the two surfaces render their own view without a second data path.
			if !containsRun(out.Runs, r.ID) {
				t.Errorf("the run pool does not carry %s", r.ID)
			}
			mine, other := 0, 0
			for _, x := range out.Runs {
				if x.Kind == r.Kind {
					mine++
				} else {
					other++
				}
			}
			if mine == 0 || other == 0 {
				t.Errorf("the pool does not carry both kinds (own %d, other %d) — the views cannot be separated", mine, other)
			}
		}},
		{name: "prompt preview", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			var out struct {
				Prompt string `json:"prompt"`
			}
			e.getJSON("/api/mercury/runs/"+r.ID+"/prompt", &out)
			if strings.TrimSpace(out.Prompt) == "" {
				t.Error("the prompt preview is empty")
			}
		}},
		{name: "history", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			var out struct {
				Executions []runs.Result `json:"executions"`
			}
			e.getJSON("/api/mercury/runs/executions?type="+string(r.Kind), &out)
			for _, x := range out.Executions {
				if x.Kind != r.Kind {
					t.Errorf("the %s history carries a %s execution — the histories are not separate", r.Kind, x.Kind)
				}
			}
		}},
		{name: "calendar", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			var cal model.RunCalendar
			e.getJSON("/api/mercury/runs/calendar?type="+string(r.Kind), &cal)
			for _, occ := range cal.Occurrences {
				if occ.Kind != r.Kind {
					t.Errorf("the %s calendar carries a %s occurrence", r.Kind, occ.Kind)
				}
			}
		}},
		{name: "results", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			var out struct {
				Results []runs.Result `json:"results"`
			}
			e.getJSON("/api/mercury/runs/"+r.ID+"/results", &out)
		}},
		{name: "start now", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			code, body := e.post("/api/mercury/runs/"+r.ID+"/run", map[string]any{})
			if code != http.StatusOK {
				t.Fatalf("start: %d %s", code, trim(body))
			}
			var out model.StartOutcome
			mustJSON(t, body, &out)
			if !out.Started && out.NotStarted == "" && !out.Queued {
				t.Errorf("the start answered neither an execution nor a reason: %+v", out)
			}
		}},
		{name: "cancel", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			code, body := e.post("/api/mercury/runs/"+r.ID+"/cancel", map[string]any{})
			if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusConflict {
				t.Fatalf("cancel: %d %s", code, trim(body))
			}
		}},
		{name: "defer / resume", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			// Both kinds reach the same slot functions and answer the same way. With nothing live
			// the answer must be a NAMED refusal — never a silent success and never a crash. (The
			// working defer/resume of a live execution is proven end to end in race_test.go.)
			deferCode, deferBody := e.post("/api/mercury/runs/"+r.ID+"/defer", map[string]any{})
			resumeCode, resumeBody := e.post("/api/mercury/runs/"+r.ID+"/resume", map[string]any{})
			for _, probe := range []struct {
				name string
				code int
				body []byte
			}{{"defer", deferCode, deferBody}, {"resume", resumeCode, resumeBody}} {
				if probe.code < 400 || probe.code >= 500 {
					t.Errorf("%s with nothing live: %d %s — want a named 4xx refusal", probe.name, probe.code, trim(probe.body))
				}
				if !strings.Contains(string(probe.body), "detail") {
					t.Errorf("%s refusal carries no reason: %s", probe.name, trim(probe.body))
				}
			}
			deferSeen[r.Kind] = deferCode
			resumeSeen[r.Kind] = resumeCode
		}},
		{name: "authorship", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			var out struct {
				Run runs.Run `json:"run"`
			}
			e.getJSON("/api/mercury/runs/"+r.ID, &out)
			a := out.Run.Authorship
			if a.Created.User != e.user || a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
				t.Errorf("authorship is not recorded on both sides: %+v", a)
			}
		}},
		{name: "tuning (model, effort, time budget)", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			if r.Tuning.Model == "" || r.Tuning.Effort == "" || r.Tuning.TimeBudget == nil {
				t.Errorf("the three tunables are not stored for %s: %+v", r.Kind, r.Tuning)
			}
		}},
		{
			name: "media attachments", kinds: []model.RunKind{model.KindTodo},
			why: "an attachment belongs to a concrete task; a recurring run carries axioms, not media",
			exercise: func(t *testing.T, r runs.Run) {
				code, body := e.post("/api/mercury/runs/"+r.ID+"/attachments",
					map[string]any{"filename": "note.txt", "contentB64": "aGVsbG8="})
				if code != http.StatusOK {
					t.Fatalf("attachment upload: %d %s", code, trim(body))
				}
			},
		},
		{
			name: "schedule and active switch", kinds: []model.RunKind{model.KindAuto},
			why: "a one-time task has a due date instead of a rhythm; there is nothing to switch on or off",
			exercise: func(t *testing.T, r runs.Run) {
				if r.Schedule == nil || !r.Active {
					t.Errorf("the rhythm or the active switch is not stored: %+v", r.Schedule)
				}
			},
		},
		{
			name: "axiom coverage", kinds: []model.RunKind{model.KindAuto},
			why: "coverage answers which axioms a recurring run carries; a task is not derived from axioms",
			exercise: func(t *testing.T, r runs.Run) {
				var cov model.RunCoverage
				e.getJSON("/api/mercury/runs/coverage", &cov)
				if len(cov.Axioms) == 0 {
					t.Error("the coverage view knows no axioms at all")
				}
			},
		},
		{
			name: "target repositories", kinds: []model.RunKind{model.KindTodo},
			why: "a task names its destinations; a recurring run visits every repository of the instance",
			exercise: func(t *testing.T, r runs.Run) {
				if len(r.Targets) == 0 {
					t.Error("the targets are not stored")
				}
			},
		},
		{
			name: "due date", kinds: []model.RunKind{model.KindTodo},
			why: "a task is due once; a recurring run is due by its rhythm",
			exercise: func(t *testing.T, r runs.Run) {
				if r.DueAt == nil {
					t.Error("the due date is not stored")
				}
			},
		},
		{name: "delete", kinds: bothKinds(), exercise: func(t *testing.T, r runs.Run) {
			code, body := e.do(e.request(http.MethodDelete, "/api/mercury/runs/"+r.ID, nil))
			if code != http.StatusOK && code != http.StatusNoContent {
				t.Fatalf("delete: %d %s", code, trim(body))
			}
		}},
	}

	// The list must actually be a list: a function nobody exercises proves nothing.
	if len(list) < 15 {
		t.Fatalf("the reconciliation list holds only %d functions — that cannot be the machinery", len(list))
	}

	e.deps.hold("alpha") // the defer/resume function needs a running execution to work on
	for _, f := range list {
		for _, kind := range []model.RunKind{model.KindAuto, model.KindTodo} {
			applies := false
			for _, k := range f.kinds {
				if k == kind {
					applies = true
				}
			}
			if !applies {
				if f.why == "" {
					t.Errorf("%q does not apply to %s and gives no structural reason", f.name, kind)
				} else {
					t.Logf("%-38s %-4s not applicable — %s", f.name, kind, f.why)
				}
				continue
			}
			t.Run(fmt.Sprintf("%s/%s", strings.ReplaceAll(f.name, " ", "-"), kind), func(t *testing.T) {
				f.exercise(t, byKind[kind])
			})
		}
	}
	e.deps.release("alpha")

	if deferSeen[model.KindAuto] != deferSeen[model.KindTodo] {
		t.Errorf("defer answers %d for auto but %d for todo — the kinds are not symmetric",
			deferSeen[model.KindAuto], deferSeen[model.KindTodo])
	}
	if resumeSeen[model.KindAuto] != resumeSeen[model.KindTodo] {
		t.Errorf("resume answers %d for auto but %d for todo — the kinds are not symmetric",
			resumeSeen[model.KindAuto], resumeSeen[model.KindTodo])
	}
}

// REQ-005.3: the seven automatic runs of the pre-rebuild instance are expressible — rhythm plus
// axioms plus the active switch, nothing else was needed.
func TestTheSevenAutomaticRunsAreExpressible(t *testing.T) {
	e := newEnvWithConstitution(t, sched.Config{Tick: time.Hour})
	e.boot(e.ctx)

	for i, spec := range []struct {
		title    string
		schedule map[string]any
	}{
		{"Consistency sweep", map[string]any{"kind": "daily", "timeOfDay": "02:00"}},
		{"Reuse and SDK audit", map[string]any{"kind": "daily", "timeOfDay": "03:00"}},
		{"Translation pass", map[string]any{"kind": "daily", "timeOfDay": "04:00"}},
		{"Uniformity check", map[string]any{"kind": "weekly", "timeOfDay": "02:00", "weekdays": []int{1}}},
		{"Rights and interfaces audit", map[string]any{"kind": "weekly", "timeOfDay": "02:00", "weekdays": []int{3}}},
		{"Minimalism review", map[string]any{"kind": "weekly", "timeOfDay": "02:00", "weekdays": []int{5}}},
		{"Documentation sweep", map[string]any{"kind": "weekly", "timeOfDay": "02:00", "weekdays": []int{6}}},
	} {
		r := e.createRun(t, map[string]any{
			"kind": "auto", "title": spec.title,
			"axiomIds": []string{"ax_no_redundancy"}, "schedule": spec.schedule, "active": true,
		})
		if r.Schedule == nil || r.PromptSnapshot == "" {
			t.Fatalf("run %d (%s) is incomplete: schedule=%v promptSnapshot=%d bytes",
				i+1, spec.title, r.Schedule, len(r.PromptSnapshot))
		}
		// The composed prompt carries the constitution in full wording (REQ-002.1).
		if !strings.Contains(r.PromptSnapshot, "No change may create redundancy") {
			t.Errorf("run %d does not carry the axiom wording in its prompt", i+1)
		}
		if !strings.Contains(r.PromptSnapshot, "Every execution reports") {
			t.Errorf("run %d does not carry the run rules in its prompt", i+1)
		}
	}

	var out struct {
		Runs []runs.Run `json:"runs"`
	}
	e.getJSON("/api/mercury/runs", &out)
	if len(out.Runs) != 7 {
		t.Fatalf("the instance carries %d automatic runs, want the 7 that were expressible", len(out.Runs))
	}
}

// createRun creates a run through the real route and returns it.
func (e *env) createRun(t *testing.T, body map[string]any) runs.Run {
	t.Helper()
	code, resp := e.post("/api/mercury/runs", body)
	if code != http.StatusOK {
		t.Fatalf("create %v: %d %s", body["title"], code, trim(resp))
	}
	var r runs.Run
	mustJSON(t, resp, &r)
	return r
}

func bothKinds() []model.RunKind { return []model.RunKind{model.KindAuto, model.KindTodo} }

func containsRun(list []runs.Run, id string) bool {
	for _, r := range list {
		if r.ID == id {
			return true
		}
	}
	return false
}
