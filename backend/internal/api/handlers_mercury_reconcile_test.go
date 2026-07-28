package api

import (
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// A constitution write recomposes exactly the runs whose composed inputs changed — and,
// because the input hash folds in record titles, a pure RENAME counts as a change (REQ-003).
// Unaffected runs (and todos, which fold in no axioms) are left untouched, so recomposition
// neither loses a rename nor churns runs it did not affect.
func TestRecomposeAffectedRunsOnlyTouchesChanged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))

	store := runs.NewStore(nil)
	s := &Server{runs: store}

	cat := runs.Catalog{ByID: map[string]mercury.RunAxiom{
		"ax_1": {ID: "ax_1", Titel: "Minimalism", Body: "Sei minimal."},
		"ax_2": {ID: "ax_2", Titel: "Symmetrie", Body: "Sei symmetrisch."},
	}}

	r1 := runs.Run{ID: "run_1", Kind: model.KindAuto, Title: "R1", AxiomIDs: []string{"ax_1"}}
	r2 := runs.Run{ID: "run_2", Kind: model.KindAuto, Title: "R2", AxiomIDs: []string{"ax_2"}}
	todo := runs.Run{ID: "run_t", Kind: model.KindTodo, Title: "T", Task: "Fix login."}
	runs.ComposeInto(&r1, cat)
	runs.ComposeInto(&r2, cat)
	runs.ComposeInto(&todo, cat)
	if _, err := store.Patch(func([]runs.Run) ([]runs.Run, error) {
		return []runs.Run{r1, r2, todo}, nil
	}); err != nil {
		t.Fatal(err)
	}

	// Rename ax_1 (title only, body unchanged). run_1 references it → must recompose; run_2
	// must not.
	cat.ByID["ax_1"] = mercury.RunAxiom{ID: "ax_1", Titel: "Minimalismus-Maxim", Body: "Sei minimal."}
	s.recomposeAffectedRuns(cat)

	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]runs.Run{}
	for _, r := range got {
		by[r.ID] = r
	}

	if by["run_1"].PromptInputHash == r1.PromptInputHash {
		t.Error("run_1 references the renamed axiom — its snapshot hash must change")
	}
	if !strings.Contains(by["run_1"].PromptSnapshot, "Minimalismus-Maxim") {
		t.Errorf("run_1 prompt not recomposed with the new title:\n%s", by["run_1"].PromptSnapshot)
	}

	if by["run_2"].PromptInputHash != r2.PromptInputHash {
		t.Error("run_2 does not reference the renamed axiom — its snapshot must be untouched")
	}

	// A todo folds in no axioms, so a constitution write never recomposes it.
	if by["run_t"].PromptInputHash != "" || by["run_t"].PromptSnapshot != todo.PromptSnapshot {
		t.Errorf("todo must be skipped: got hash=%q", by["run_t"].PromptInputHash)
	}
}
