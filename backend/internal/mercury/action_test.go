package mercury

import (
	"errors"
	"strings"
	"testing"
)

// testActionCtx is the set of things a proposed action may reference in these tests.
func testActionCtx() ActionContext {
	return ActionContext{
		AxiomIDs:    map[string]bool{"ax_1": true, "ax_2": true},
		RunIDs:      map[string]bool{"run_1": true},
		RecordPaths: map[string]bool{"axiome/architektur/uniformitaet.md": true, "regeln/prozess/deploy.md": true},
		RepoIDs:     map[string]bool{"repo-devlab": true},
	}
}

func TestExtractChatAction_Valid(t *testing.T) {
	ctx := testActionCtx()
	cases := map[string]struct {
		in       string
		wantKind string
	}{
		"create_todo existing repo": {
			`Klar, ich lege das an. {"kind":"create_todo","name":"Bug X","task":"Fix the black screen","targets":[{"repo":"repo-devlab"}]}`,
			ActionCreateTodo,
		},
		"create_todo new repo": {
			`{"kind":"create_todo","name":"Neuer Service","task":"Scaffold it","targets":[{"newRepo":"billing"}]}`,
			ActionCreateTodo,
		},
		"create_run": {
			`{"kind":"create_run","name":"Nightly","axiomIds":["ax_1","ax_2"],"schedule":{"kind":"daily","timeOfDay":"03:00"}}`,
			ActionCreateRun,
		},
		"add_record axiom": {
			`{"kind":"add_record","section":"axiome","titel":"Neu","body":"Ein Satz."}`,
			ActionAddRecord,
		},
		"add_record rule": {
			`{"kind":"add_record","section":"regeln","titel":"Regel","body":"Ein Satz."}`,
			ActionAddRecord,
		},
		"edit_record": {
			`{"kind":"edit_record","path":"axiome/architektur/uniformitaet.md","titel":"Uniform","body":"Neu."}`,
			ActionEditRecord,
		},
		"delete_record": {
			`{"kind":"delete_record","path":"regeln/prozess/deploy.md"}`,
			ActionDeleteRecord,
		},
		"delete_run": {`{"kind":"delete_run","runId":"run_1"}`, ActionDeleteRun},
		"run_now":    {`{"kind":"run_now","runId":"run_1"}`, ActionRunNow},
		"plan_runs": {
			`{"kind":"plan_runs","mode":"replace","runs":[{"name":"A","axiomIds":["ax_1"],"schedule":{"kind":"daily","timeOfDay":"03:00"}}]}`,
			ActionPlanRuns,
		},
	}
	for label, tc := range cases {
		action, cleaned, ok := ExtractChatAction(tc.in, ctx)
		if !ok {
			t.Errorf("%s: expected an action, got none", label)
			continue
		}
		if action.Kind != tc.wantKind {
			t.Errorf("%s: kind = %q, want %q", label, action.Kind, tc.wantKind)
		}
		if strings.Contains(cleaned, "{") {
			t.Errorf("%s: cleaned reply still holds JSON: %q", label, cleaned)
		}
	}
}

func TestExtractChatAction_StripsAndKeepsProse(t *testing.T) {
	ctx := testActionCtx()
	in := "Notiert. Ich lege das ToDo an.\n" +
		`{"kind":"create_todo","name":"Bug","task":"Fix it","targets":[{"repo":"repo-devlab"}]}`
	action, cleaned, ok := ExtractChatAction(in, ctx)
	if !ok || action.Kind != ActionCreateTodo {
		t.Fatalf("expected create_todo, got ok=%v kind=%q", ok, action.Kind)
	}
	if cleaned != "Notiert. Ich lege das ToDo an." {
		t.Errorf("cleaned = %q, want the prose without the JSON", cleaned)
	}
	if action.Task != "Fix it" || len(action.Targets) != 1 || action.Targets[0].Repo != "repo-devlab" {
		t.Errorf("action not parsed as expected: %+v", action)
	}
}

func TestExtractChatAction_LegacyRunPlan(t *testing.T) {
	ctx := testActionCtx()
	in := `{"runs":[{"name":"A","axiomIds":["ax_1"],"schedule":{"kind":"daily","timeOfDay":"03:00"}}]}`
	action, _, ok := ExtractChatAction(in, ctx)
	if !ok {
		t.Fatal("expected a bare run-plan to be accepted as plan_runs")
	}
	if action.Kind != ActionPlanRuns || action.Mode != "replace" || len(action.Runs) != 1 {
		t.Errorf("legacy run-plan not folded into plan_runs/replace: %+v", action)
	}
}

func TestExtractChatAction_NotAnAction(t *testing.T) {
	ctx := testActionCtx()
	cases := map[string]string{
		"plain prose":        "Das ist nur eine Erklärung, kein Handlungswunsch.",
		"unrelated json":     `Hier ein Beispiel: {"foo":1,"bar":"x"} — nur zur Illustration.`,
		"empty kind":         `{"kind":"","name":"X"}`,
		"unknown kind":       `{"kind":"frobnicate","name":"X"}`,
		"todo no target":     `{"kind":"create_todo","name":"X","task":"y","targets":[]}`,
		"todo bad target":    `{"kind":"create_todo","name":"X","task":"y","targets":[{"repo":"a","newRepo":"b"}]}`,
		"todo unknown repo":  `{"kind":"create_todo","name":"X","task":"y","targets":[{"repo":"ghost"}]}`,
		"run unknown axiom":  `{"kind":"create_run","name":"X","axiomIds":["ax_9"],"schedule":{"kind":"daily","timeOfDay":"03:00"}}`,
		"run bad schedule":   `{"kind":"create_run","name":"X","axiomIds":["ax_1"],"schedule":{"kind":"monthly","timeOfDay":"03:00"}}`,
		"add bad section":    `{"kind":"add_record","section":"nope","titel":"T","body":"B"}`,
		"add missing body":   `{"kind":"add_record","section":"axiome","titel":"T","body":"  "}`,
		"edit unknown path":  `{"kind":"edit_record","path":"axiome/ghost/none.md","titel":"T","body":"B"}`,
		"edit bad path":      `{"kind":"edit_record","path":"not a path","titel":"T","body":"B"}`,
		"delete unknown run": `{"kind":"delete_run","runId":"run_9"}`,
		"plan bad mode":      `{"kind":"plan_runs","mode":"wipe","runs":[{"name":"A","axiomIds":["ax_1"],"schedule":{"kind":"daily","timeOfDay":"03:00"}}]}`,
	}
	for label, in := range cases {
		if _, _, ok := ExtractChatAction(in, ctx); ok {
			t.Errorf("%s: expected NO action, but one was extracted", label)
		}
	}
}

func TestValidateChatAction_ErrorWraps(t *testing.T) {
	ctx := testActionCtx()
	a := ChatAction{Kind: "frobnicate"}
	if err := ValidateChatAction(&a, ctx); !errors.Is(err, ErrInvalidAction) {
		t.Errorf("unknown kind: expected ErrInvalidAction, got %v", err)
	}
	// A schedule fault surfaces as ErrInvalidPlacement (shared with the run-plan validator).
	run := ChatAction{Kind: ActionCreateRun, Name: "X", AxiomIDs: []string{"ax_1"}, Schedule: PlanSchedule{Kind: "daily", TimeOfDay: "3pm"}}
	if err := ValidateChatAction(&run, ctx); !errors.Is(err, ErrInvalidPlacement) {
		t.Errorf("bad schedule: expected ErrInvalidPlacement, got %v", err)
	}
}

func TestValidateChatAction_NoContextSkipsMembership(t *testing.T) {
	// An empty ActionContext must not enforce membership (the endpoint is the authoritative gate), so a
	// well-formed action still validates when we were shown no ids.
	a := ChatAction{Kind: ActionRunNow, RunID: "run_anything"}
	if err := ValidateChatAction(&a, ActionContext{}); err != nil {
		t.Errorf("empty context should skip membership, got %v", err)
	}
}
