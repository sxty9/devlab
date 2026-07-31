package runs

import (
	"encoding/json"
	"testing"

	"devlab/backend/internal/model"
)

// Only a genuinely executed chain with no failed and no not-executed stage is a success —
// a not-applicable (skipped) stage must NEVER read as one on its own, and a skip after a
// failure sinks the pipeline (REQ-030.4). The one derivation is model.PipelineSucceeded;
// nothing is ever stored.
func TestPipelineSucceededHonesty(t *testing.T) {
	sv := func(states ...model.StepState) []model.StageView {
		out := make([]model.StageView, len(states))
		for i, st := range states {
			out[i] = model.StageView{Stage: model.StageImplement, State: st}
		}
		return out
	}
	cases := []struct {
		name            string
		stages          []model.StageView
		done, succeeded bool
	}{
		{"all executed", sv(model.StepExecuted, model.StepExecuted), true, true},
		{"not-applicable continues the chain", sv(model.StepExecuted, model.StepNotApplicable, model.StepExecuted), true, true},
		{"a failed stage sinks it", sv(model.StepExecuted, model.StepFailed), true, false},
		{"a not-executed stage sinks it", sv(model.StepExecuted, model.StepFailed, model.StepNotExecuted), true, false},
		{"running is not done", sv(model.StepExecuted, model.StepRunning), false, false},
		{"pending is not done", sv(model.StepPending), false, false},
		{"empty is neither done nor a success", nil, false, false},
	}
	for _, c := range cases {
		done, ok := model.PipelineSucceeded(c.stages)
		if done != c.done || ok != c.succeeded {
			t.Errorf("%s: PipelineSucceeded = (%v,%v), want (%v,%v)", c.name, done, ok, c.done, c.succeeded)
		}
	}
}

// A legacy result file stored a bool `ok` on steps, not the four-state `status`. The tolerant
// legacy reader must map it so old history still renders: ok:true → executed, ok:false →
// failed; a present status wins over the legacy bool.
func TestLegacyStepBoolMapsToStates(t *testing.T) {
	var lr legacyResult
	raw := `{"runId":"r","resultId":"x","repos":[{"repo":"svc","steps":[
		{"name":"push","ok":true},
		{"name":"pr","status":"not-applicable","ok":true},
		{"name":"analyze","ok":false}
	]}]}`
	if err := json.Unmarshal([]byte(raw), &lr); err != nil {
		t.Fatal(err)
	}
	res := mapLegacy(lr)
	got := res.Repos[0].Stages
	if got[0].State != model.StepExecuted {
		t.Errorf("legacy ok:true → %s, want executed", got[0].State)
	}
	if got[1].State != model.StepNotApplicable {
		t.Errorf("status must win over the legacy ok: %s", got[1].State)
	}
	if got[2].State != model.StepFailed {
		t.Errorf("legacy ok:false → %s, want failed", got[2].State)
	}
}
