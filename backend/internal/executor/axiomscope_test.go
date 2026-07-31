package executor

// The examined stand must have a CONSUMER. The pool, the renderer and the prompt slot all existed
// and were exercised by their own unit tests, while the assembled prompt never carried the section
// and nothing was ever written back — so every run, forever, fell through to "never examined ⇒
// examine the whole repository". These tests measure the wiring, not the pieces: what the agent was
// handed, and what the motor recorded afterwards.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"devlab/backend/internal/model"
)

// TestImplementCarriesTheExaminedStandIntoThePrompt pins the read half: the motor asks the pool for
// THIS repository's stand and the agent's prompt names it.
func TestImplementCarriesTheExaminedStandIntoThePrompt(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("alpha", "beta")
	deps.scope = map[string]string{
		"alpha": "\n## Zuletzt geprüfter Stand (für dieses Repository)\n\n- Keine Redundanz: zuletzt geprüft bei Commit aaaa1111\n",
		"beta":  "\n## Zuletzt geprüfter Stand (für dieses Repository)\n\n- Keine Redundanz: noch nie geprüft\n",
	}
	sink := newFakeSink()
	req := mkRequest(model.KindAuto, "alpha", "beta")
	req.Run.AxiomIDs = []string{"ax_redundanz"}

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(deps.scopeAsked) != 2 || deps.scopeAsked[0] != "alpha" || deps.scopeAsked[1] != "beta" {
		t.Fatalf("the motor asked for the examined stand of %v, want both repositories once each", deps.scopeAsked)
	}
	for _, call := range deps.agentCalls {
		want := deps.scope[call.repo]
		if !strings.Contains(call.prompt, strings.TrimSpace(want)) {
			t.Errorf("the prompt of %s does not carry its examined stand.\nwant: %q\nprompt:\n%s", call.repo, want, call.prompt)
		}
		// Per REPO: one repository's stand may never travel in the other's prompt.
		for other, section := range deps.scope {
			if other == call.repo {
				continue
			}
			if strings.Contains(call.prompt, strings.TrimSpace(section)) {
				t.Errorf("the prompt of %s carries the examined stand of %s", call.repo, other)
			}
		}
	}
}

// TestImplementRecordsTheExaminedStand pins the write half: after the agent worked the repository
// through, the stand it left behind is recorded — for every axiom of the run, per repository.
func TestImplementRecordsTheExaminedStand(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("alpha")
	deps.benches["alpha"].head = "cafebabe9999"
	sink := newFakeSink()
	req := mkRequest(model.KindAuto, "alpha")
	req.Run.AxiomIDs = []string{"ax_redundanz", "ax_uniformity"}

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(deps.scopeRecords) != 1 {
		t.Fatalf("the motor recorded %d examined stands, want exactly one", len(deps.scopeRecords))
	}
	rec := deps.scopeRecords[0]
	if rec.repo != "alpha" || rec.commit != "cafebabe9999" {
		t.Errorf("recorded stand = %s@%s, want alpha@cafebabe9999 (the workbench head the agent left)", rec.repo, rec.commit)
	}
	if strings.Join(rec.ids, ",") != "ax_redundanz,ax_uniformity" {
		t.Errorf("recorded axioms = %v, want every axiom of the run", rec.ids)
	}
}

// TestExaminedStandRecordFailureIsReportedNotFatal: the pool is a convenience, not the delivery. A
// write that fails costs the next run its incrementality and must be NAMED — never swallowed, and
// never turned into a failed stage over work that is already published.
func TestExaminedStandRecordFailureIsReportedNotFatal(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("alpha")
	deps.scopeErr = errors.New("axiom-check pool unreadable: /state/axiom-checks.json")
	sink := newFakeSink()
	req := mkRequest(model.KindAuto, "alpha")
	req.Run.AxiomIDs = []string{"ax_redundanz"}

	if err := Execute(context.Background(), deps, req, sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sv, ok := sink.terminal("alpha", model.StageImplement)
	if !ok || sv.State != model.StepExecuted {
		t.Fatalf("implement = %v (found=%v), want executed — a pool write may not fail the delivery", sv.State, ok)
	}
	if !strings.Contains(sv.Log, "examined stand not recorded") {
		t.Errorf("the failed pool write is not named in the stage log: %q", sv.Log)
	}
}

// A ToDo names no axiom, so there is no incremental scope: nothing is asked for, nothing is written,
// and the prompt carries no empty heading pretending otherwise.
func TestTodoCarriesNoExaminedStandSection(t *testing.T) {
	stubConstitution(t, nil)
	deps := newFakeDeps("alpha")
	sink := newFakeSink()

	if err := Execute(context.Background(), deps, mkRequest(model.KindTodo, "alpha"), sink); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(deps.agentCalls) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(deps.agentCalls))
	}
	if strings.Contains(deps.agentCalls[0].prompt, "Zuletzt geprüfter Stand") {
		t.Errorf("a ToDo prompt carries an examined-stand section:\n%s", deps.agentCalls[0].prompt)
	}
}
