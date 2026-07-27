package api

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// A scheme write recomposes exactly the runs whose composed inputs changed — and, because the input hash
// now folds in record titles, a pure RENAME counts as a change. Unaffected runs (and ToDos, which don't
// fold in axioms at all) are left untouched, so recomposition neither loses a rename nor churns runs it
// didn't affect.
func TestRecomposeAffectedRunsOnlyTouchesChanged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))

	store := runs.NewStore()
	s := &Server{runs: store}

	byID := map[string]mercury.RunAxiom{
		"ax_1": {ID: "ax_1", Titel: "Minimalism", Body: "Sei minimal."},
		"ax_2": {ID: "ax_2", Titel: "Symmetrie", Body: "Sei symmetrisch."},
	}
	seedAt := time.Now().Add(-time.Hour).UTC()

	r1 := runs.Run{ID: "run_1", AxiomIDs: []string{"ax_1"}, UpdatedAt: seedAt}
	r2 := runs.Run{ID: "run_2", AxiomIDs: []string{"ax_2"}, UpdatedAt: seedAt}
	todo := runs.Run{ID: "run_t", Type: runs.TypeTodo, Task: "Fix login.", UpdatedAt: seedAt}
	composeInto(&r1, byID, nil, seedAt)
	composeInto(&r2, byID, nil, seedAt)
	composeInto(&todo, byID, nil, seedAt)
	if _, err := store.Patch(func([]runs.Run) ([]runs.Run, error) {
		return []runs.Run{r1, r2, todo}, nil
	}); err != nil {
		t.Fatal(err)
	}

	// Rename ax_1 (title only, body unchanged). run_1 references it → must recompose; run_2 must not.
	byID["ax_1"] = mercury.RunAxiom{ID: "ax_1", Titel: "Minimalismus-Maxim", Body: "Sei minimal."}
	s.recomposeAffectedRuns(byID, nil)

	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]runs.Run{}
	for _, r := range got {
		by[r.ID] = r
	}

	if by["run_1"].PromptHash == r1.PromptHash {
		t.Error("run_1 references the renamed axiom — its snapshot hash must change")
	}
	if !strings.Contains(by["run_1"].Prompt, "Minimalismus-Maxim") {
		t.Errorf("run_1 prompt not recomposed with the new title:\n%s", by["run_1"].Prompt)
	}
	if !by["run_1"].UpdatedAt.After(seedAt) {
		t.Error("run_1 UpdatedAt must advance on recompose")
	}

	if by["run_2"].PromptHash != r2.PromptHash {
		t.Error("run_2 does not reference the renamed axiom — its snapshot must be untouched")
	}
	if !by["run_2"].UpdatedAt.Equal(seedAt) {
		t.Error("run_2 UpdatedAt must not move (unaffected run must not churn)")
	}

	// A ToDo folds in no axioms, so it is never recomposed by a scheme write.
	if !by["run_t"].UpdatedAt.Equal(seedAt) || by["run_t"].PromptHash != "" {
		t.Errorf("ToDo must be skipped: got hash=%q updatedAt=%v", by["run_t"].PromptHash, by["run_t"].UpdatedAt)
	}
}
