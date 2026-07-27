package api

import (
	"testing"

	"devlab/backend/internal/runs"
)

// The delivery chain is the binding order executeRepo walks; a halt records every LATER stage as
// not-executed, so its contents pin down exactly which steps a failure must suppress.
func TestDeliveryChain(t *testing.T) {
	cases := map[string][]string{
		"report": {"analyze"},
		"pr":     {"implement", "push", "pr"},
		"full":   {"implement", "dev-deploy", "push", "pr"},
		"":       {"implement", "push", "pr"}, // an unknown mode behaves as pr
	}
	for mode, want := range cases {
		got := deliveryChain(mode)
		if len(got) != len(want) {
			t.Fatalf("deliveryChain(%q) = %v, want %v", mode, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("deliveryChain(%q)[%d] = %q, want %q", mode, i, got[i], want[i])
			}
		}
	}
}

// A failed step prevents ALL following ones: they are recorded as not-executed (never skipped, never a
// hollow success), each says why, and the repo result is consequently not a success (req 3, 5, 7).
func TestRecordNotExecutedHaltsChain(t *testing.T) {
	rr := &runs.RepoResult{Repo: "r", Steps: []runs.Step{
		{Name: "implement", Status: runs.StepOK},
		{Name: "dev-deploy", Status: runs.StepFailed},
	}}
	recordNotExecuted(rr, "full", "dev-deploy", &liveSaver{do: func() {}})

	byName := map[string]runs.Step{}
	for _, s := range rr.Steps {
		byName[s.Name] = s
	}
	if byName["push"].Status != runs.StepNotExecuted {
		t.Errorf("push after a failed dev-deploy = %q, want not-executed", byName["push"].Status)
	}
	if byName["pr"].Status != runs.StepNotExecuted {
		t.Errorf("pr after a failed dev-deploy = %q, want not-executed", byName["pr"].Status)
	}
	// The failed step and the earlier ok step are not re-added.
	if len(rr.Steps) != 4 {
		t.Fatalf("expected 4 steps (implement, dev-deploy, push, pr), got %d: %+v", len(rr.Steps), rr.Steps)
	}
	// Every not-executed step is auskunftsfähig — it states, in the user's language, why it did not run.
	for _, s := range rr.Steps {
		if s.Status == runs.StepNotExecuted && s.Log == "" {
			t.Errorf("not-executed step %q carries no reason", s.Name)
		}
	}
	// A result carrying not-executed steps is not a success.
	if runs.StepsSucceeded(rr.Steps) {
		t.Error("a chain halted by a failed step must not count as successful")
	}
}

// A structurally not-applicable step lets the chain continue: it neither halts it nor produces any
// not-executed step, and the chain still completes as a success (req 4). Mirrors executeRepo's full-mode
// path when dev-deploy does not apply (no deploy target / switched off).
func TestNotApplicableContinuesChain(t *testing.T) {
	steps := []runs.Step{
		{Name: "implement", Status: runs.StepOK},
		{Name: "dev-deploy", Status: runs.StepNotApplicable},
		{Name: "push", Status: runs.StepOK},
		{Name: "pr", Status: runs.StepOK},
	}
	for _, s := range steps {
		if s.Status == runs.StepNotExecuted {
			t.Fatal("a not-applicable dev-deploy must not produce a not-executed step")
		}
	}
	if !runs.StepsSucceeded(steps) {
		t.Error("a chain whose only skip is not-applicable must count as successful")
	}
}

// recordNotExecuted on the last stage (or in report mode) adds nothing — there is nothing downstream to
// suppress.
func TestRecordNotExecutedNoDownstream(t *testing.T) {
	rr := &runs.RepoResult{Steps: []runs.Step{{Name: "pr", Status: runs.StepFailed}}}
	recordNotExecuted(rr, "full", "pr", &liveSaver{do: func() {}})
	if len(rr.Steps) != 1 {
		t.Errorf("nothing follows pr; expected no extra steps, got %+v", rr.Steps)
	}
	report := &runs.RepoResult{Steps: []runs.Step{{Name: "analyze", Status: runs.StepFailed}}}
	recordNotExecuted(report, "report", "analyze", &liveSaver{do: func() {}})
	if len(report.Steps) != 1 {
		t.Errorf("report mode has no downstream; expected no extra steps, got %+v", report.Steps)
	}
}
