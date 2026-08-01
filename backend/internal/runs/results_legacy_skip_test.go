package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"devlab/backend/internal/model"
)

// The retired system marked a step it had SKIPPED as ok, under a step it literally named
// "übersprungen". Carried over unchanged that reads as executed — and executed is painted green, so
// the history showed a GREEN SKIP. The browser inspection found it on 2026-07-31 in several
// entries. The name is carried verbatim; only the claim that it ran is withdrawn.
func TestALegacySkippedStepDoesNotReadAsExecuted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "exec"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "legacy"))

	rec := map[string]any{
		"resultId": "2026-07-24T01-30-32.949681657Z",
		"runId":    "run_alt",
		"runName":  "Rechte & Zugang",
		"repos": []map[string]any{{
			"repo": "aigentic",
			"steps": []map[string]any{
				{"name": "übersprungen", "ok": true, "log": "Offener Mercury-PR ist noch offen"},
				{"name": "implement", "ok": true, "log": "gebaut"},
			},
		}},
	}
	d := filepath.Join(dir, "legacy", "run_alt")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(d, "2026-07-24T01-30-32.949681657Z.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := NewResultStore(nil).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("want the one archived record, got %d", len(all))
	}
	got := map[string]model.StepState{}
	reason := map[string]string{}
	for _, sv := range all[0].Repos[0].Stages {
		got[string(sv.Stage)] = sv.State
		reason[string(sv.Stage)] = sv.Reason
	}
	// The name stays — nothing is rewritten.
	if _, ok := got["übersprungen"]; !ok {
		t.Fatalf("the historical stage name was dropped: %v", got)
	}
	// But it never ran, so it must not read as executed — that is what renders green.
	if got["übersprungen"] == model.StepExecuted {
		t.Errorf("a skipped step reads as executed and therefore renders GREEN")
	}
	if got["übersprungen"] != model.StepNotApplicable {
		t.Errorf(`state = %q, want not-applicable`, got["übersprungen"])
	}
	if reason["übersprungen"] == "" {
		t.Error("the skip lost the reason the archive recorded for it")
	}
	// A step that really ran is untouched.
	if got["implement"] != model.StepExecuted {
		t.Errorf("implement = %q, want executed", got["implement"])
	}
}
