package api

import (
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// A constitution write recomposes exactly the runs whose composed inputs changed — and, because the
// input hash folds in record titles, a pure RENAME counts as a change (REQ-003). Every prompt now
// carries the whole constitution (REQ-002.1), automatic runs and ToDos alike, so a rename reaches
// BOTH kinds; a write that changes nothing composed still churns nothing.
func TestRecomposeAffectedRunsReachesBothKinds(t *testing.T) {
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

	// A write that changes nothing composed must rewrite nothing at all.
	s.recomposeAffectedRuns(cat)
	unchanged := listByID(t, store)
	for _, before := range []runs.Run{r1, r2, todo} {
		if got := unchanged[before.ID]; got.PromptSnapshot != before.PromptSnapshot || got.PromptInputHash != before.PromptInputHash {
			t.Errorf("%s was rewritten although nothing changed", before.ID)
		}
	}

	// Rename ax_1 (title only, body unchanged). Every prompt carries the whole constitution, so
	// every run — both automatic ones and the todo — must pick the new title up.
	cat.ByID["ax_1"] = mercury.RunAxiom{ID: "ax_1", Titel: "Minimalismus-Maxim", Body: "Sei minimal."}
	s.recomposeAffectedRuns(cat)

	by := listByID(t, store)
	for _, before := range []runs.Run{r1, r2, todo} {
		got := by[before.ID]
		if got.PromptInputHash == before.PromptInputHash {
			t.Errorf("%s: the renamed axiom must move the input hash", before.ID)
		}
		if !strings.Contains(got.PromptSnapshot, "Minimalismus-Maxim") {
			t.Errorf("%s not recomposed with the new title:\n%s", before.ID, got.PromptSnapshot)
		}
	}
	// The todo carries the wording, not just the title legend (REQ-002.1).
	if !strings.Contains(by["run_t"].PromptSnapshot, "Sei symmetrisch.") {
		t.Errorf("the todo prompt must carry every axiom in full wording:\n%s", by["run_t"].PromptSnapshot)
	}
}

func listByID(t *testing.T, store *runs.Store) map[string]runs.Run {
	t.Helper()
	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]runs.Run{}
	for _, r := range got {
		by[r.ID] = r
	}
	return by
}
