package axiomauthors

import (
	"path/filepath"
	"testing"
	"time"
)

// The constitution's authorship pool: a create stamps both creator and editor; a later edit by
// someone else stamps only the editor and leaves the original creator intact; and an axiom with no
// recorded authorship reports "not found" so the surface shows unknown rather than a guessed person.
func TestAxiomAuthorship(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_AXIOM_AUTHORS", filepath.Join(dir, "axiom-authors.json"))
	s := NewStore(nil)

	t0 := time.Now().UTC().Truncate(time.Second)

	// Create as alice → both created and updated are alice.
	s.Mutate("ax_1", func(a Author) Author {
		a.CreatedBy, a.CreatedAt, a.UpdatedBy, a.UpdatedAt = "alice", t0, "alice", t0
		return a
	})
	// Edit as bob → only the editor changes; the creator stays alice.
	s.Mutate("ax_1", func(a Author) Author {
		a.UpdatedBy, a.UpdatedAt = "bob", t0.Add(time.Hour)
		return a
	})

	got, ok := s.Get("ax_1")
	if !ok {
		t.Fatal("ax_1 authorship not found after stamping")
	}
	if got.CreatedBy != "alice" {
		t.Errorf("creator must be preserved across an edit: createdBy=%q want alice", got.CreatedBy)
	}
	if got.UpdatedBy != "bob" {
		t.Errorf("editor must be recorded: updatedBy=%q want bob", got.UpdatedBy)
	}

	// An edit of an axiom that predates tracking records the editor but leaves the creator unknown —
	// no invented history.
	s.Mutate("ax_legacy", func(a Author) Author {
		a.UpdatedBy, a.UpdatedAt = "carol", t0
		return a
	})
	leg, ok := s.Get("ax_legacy")
	if !ok || leg.UpdatedBy != "carol" || leg.CreatedBy != "" {
		t.Errorf("a pre-existing axiom edited once: want createdBy empty, updatedBy carol; got %+v", leg)
	}

	// An axiom nobody has touched is unknown, not a guessed person.
	if _, ok := s.Get("ax_none"); ok {
		t.Error("an untouched axiom must report no authorship (unknown)")
	}

	// Reload from disk (fresh store) → the record persisted.
	if a, ok := NewStore(nil).Get("ax_1"); !ok || a.CreatedBy != "alice" || a.UpdatedBy != "bob" {
		t.Errorf("authorship did not persist across reload: %+v (ok=%v)", a, ok)
	}
}
