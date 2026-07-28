package runs

import (
	"encoding/json"
	"testing"
)

// A target names exactly one repository; Create marks a repo that is to be created first
// (REQ-006.2) — the chain creates it and sets branch protection in the same pass. The wire
// form is stable and minimal.
func TestTargetWireForm(t *testing.T) {
	b, err := json.Marshal([]Target{{Repo: "devlab"}, {Repo: "new-svc", Create: true}})
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"repo":"devlab"},{"repo":"new-svc","create":true}]`
	if string(b) != want {
		t.Errorf("targets marshal = %s, want %s", b, want)
	}
	var ts []Target
	if err := json.Unmarshal([]byte(want), &ts); err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].Repo != "devlab" || ts[0].Create || ts[1].Repo != "new-svc" || !ts[1].Create {
		t.Fatalf("targets round-trip: %+v", ts)
	}
}

// A todo's targets are stored ONLY in Targets — the run definition is slim (B-20) and there
// is no second (legacy) field pair to bridge anymore; the migration tool folds old records.
func TestRunDefinitionIsSlim(t *testing.T) {
	var r Run
	if err := json.Unmarshal([]byte(`{"id":"run_x","kind":"todo","title":"T","targets":[{"repo":"devlab"}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Targets) != 1 || r.Targets[0].Repo != "devlab" {
		t.Fatalf("targets: %+v", r.Targets)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"startPending", "suspended", "\"done\"", "lastResult", "nextFireAt"} {
		if jsonContains(string(b), forbidden) {
			t.Errorf("slim run must not carry %q: %s", forbidden, b)
		}
	}
}

func jsonContains(s, needle string) bool {
	return len(s) > 0 && len(needle) > 0 && (stringIndex(s, needle) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
