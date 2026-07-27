package runs

import (
	"encoding/json"
	"strings"
	"testing"
)

// Only a genuinely executed, successful step is a success — a not-applicable (skipped) step must NEVER
// read as one, and neither a failed nor a not-executed step does. This single guard is what stopped a
// skipped delivery step from being booked green (req 1).
func TestStepStatusIsSuccess(t *testing.T) {
	cases := map[StepStatus]bool{
		StepOK:            true,
		StepFailed:        false,
		StepNotApplicable: false,
		StepNotExecuted:   false,
	}
	for st, want := range cases {
		if got := st.IsSuccess(); got != want {
			t.Errorf("%q.IsSuccess() = %v, want %v", st, got, want)
		}
	}
}

// StepsSucceeded is the honest reading of "a repository counts as successfully processed only if no step
// failed; a result with not-executed steps is no success" (req 5). A not-applicable step continues the
// chain and does not, on its own, sink it (req 4).
func TestStepsSucceeded(t *testing.T) {
	chain := func(statuses ...StepStatus) []Step {
		steps := make([]Step, len(statuses))
		for i, s := range statuses {
			steps[i] = Step{Name: "s", Status: s}
		}
		return steps
	}
	tests := []struct {
		name  string
		steps []Step
		want  bool
	}{
		{"all ok", chain(StepOK, StepOK), true},
		{"not-applicable continues the chain", chain(StepOK, StepNotApplicable, StepOK), true},
		{"a failed step sinks it", chain(StepOK, StepFailed), false},
		{"a not-executed step sinks it", chain(StepOK, StepFailed, StepNotExecuted), false},
		{"not-executed alone sinks it", chain(StepNotExecuted), false},
		{"empty is vacuously clean", nil, true},
	}
	for _, tc := range tests {
		if got := StepsSucceeded(tc.steps); got != tc.want {
			t.Errorf("%s: StepsSucceeded = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A legacy result file stored a bool `ok`, not the four-state `status`. Reading one must map it onto the
// status so old history still renders: ok:true→ok, ok:false→failed. A file that already carries `status`
// keeps it (the new field wins), and no `ok` is required.
func TestStepLegacyUnmarshal(t *testing.T) {
	var legacyOK Step
	if err := json.Unmarshal([]byte(`{"name":"push","ok":true}`), &legacyOK); err != nil {
		t.Fatal(err)
	}
	if legacyOK.Status != StepOK {
		t.Errorf("legacy ok:true → %q, want %q", legacyOK.Status, StepOK)
	}
	var legacyFail Step
	if err := json.Unmarshal([]byte(`{"name":"pr","ok":false}`), &legacyFail); err != nil {
		t.Fatal(err)
	}
	if legacyFail.Status != StepFailed {
		t.Errorf("legacy ok:false → %q, want %q", legacyFail.Status, StepFailed)
	}
	var modern Step
	if err := json.Unmarshal([]byte(`{"name":"dev-deploy","status":"not-applicable","ok":true}`), &modern); err != nil {
		t.Fatal(err)
	}
	if modern.Status != StepNotApplicable {
		t.Errorf("status must win over legacy ok: got %q, want %q", modern.Status, StepNotApplicable)
	}
}

// New results are written with `status` and NO redundant `ok` field (single source of truth).
func TestStepMarshalHasNoLegacyOK(t *testing.T) {
	b, err := json.Marshal(Step{Name: "push", Status: StepOK})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"ok":`) {
		t.Errorf("a marshaled step must not carry the legacy ok field: %s", b)
	}
	if !strings.Contains(string(b), `"status":"ok"`) {
		t.Errorf("a marshaled step must carry status: %s", b)
	}
}
