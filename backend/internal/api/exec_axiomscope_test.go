package api

// The join behind the incremental scope: pool → axioms → the ONE renderer. Three properties, each
// the answer to a way the prompt could lie: a first run must say "never examined here, examine in
// full", a second run must name the stand the first one recorded, and a record that could not be READ
// must be named as a gap instead of being answered like an empty one.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// newScopeDeps builds a composition over nothing but the examined-stand pool: no constitution store
// (the axiom titles then fall back to the ids, which is a documented degradation, not a failure) and
// no runner account. The pool file is the one thing that has to be real.
func newScopeDeps(t *testing.T) (*ChainDeps, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "axiom-checks.json")
	t.Setenv("DEVLAB_MERCURY_AXIOM_CHECKS", path)
	s := &Server{axiomChecks: runs.NewAxiomChecks(nil)}
	return &ChainDeps{s: s}, path
}

func scopeRun() runs.Run {
	return runs.Run{ID: "run_1", Title: "Passive Speicher", AxiomIDs: []string{"ax_pool", "ax_atomic"}}
}

// TestAxiomScopeFirstRunThenSecondRun is the whole point of the record: the first execution of a
// repository has nothing to go on and says so, the second is scoped to the commits since the stand
// the first one left. Before the store had a consumer, EVERY run looked like the first.
func TestAxiomScopeFirstRunThenSecondRun(t *testing.T) {
	d, _ := newScopeDeps(t)
	ctx := context.Background()
	run := scopeRun()

	first := d.AxiomScope(ctx, "svc", run)
	for _, want := range []string{"Zuletzt geprüfter Stand", "keine frühere Prüfung", "GESAMTE Repository", "ax_pool", "ax_atomic"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first run's scope is missing %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "zuletzt geprüft bei Commit") {
		t.Errorf("the first run's scope names a stand nobody recorded:\n%s", first)
	}

	at := time.Date(2026, 7, 20, 10, 30, 0, 0, time.UTC)
	if err := d.RecordAxiomScope("svc", run, "cafebabe1234", at); err != nil {
		t.Fatalf("RecordAxiomScope: %v", err)
	}

	second := d.AxiomScope(ctx, "svc", run)
	for _, want := range []string{"zuletzt geprüft bei Commit cafebabe1234", "2026-07-20", "git log <commit>..HEAD"} {
		if !strings.Contains(second, want) {
			t.Errorf("the second run's scope is missing %q:\n%s", want, second)
		}
	}
	if strings.Contains(second, "keine frühere Prüfung") {
		t.Errorf("the second run still claims nothing was ever examined:\n%s", second)
	}

	// The stand is per REPOSITORY: a repo nobody examined must not inherit another one's.
	if other := d.AxiomScope(ctx, "other", run); strings.Contains(other, "cafebabe1234") {
		t.Errorf("a second repository inherited the recorded stand:\n%s", other)
	}
	// …and the full name resolves onto the same key as the bare id, so one run's record is the next
	// run's finding no matter which form reached the motor.
	if full := d.AxiomScope(ctx, "an-org/svc", run); !strings.Contains(full, "cafebabe1234") {
		t.Errorf("the full name did not find the record the repo id wrote:\n%s", full)
	}
}

// A pool that cannot be READ must never become the claim "never examined here": that is an assertion
// nobody established, and it is the one direction that also hides the damage.
func TestAxiomScopeNamesADamagedPoolInsteadOfClaimingNothingWasChecked(t *testing.T) {
	d, path := newScopeDeps(t)
	ctx := context.Background()
	run := scopeRun()
	if err := os.WriteFile(path, []byte("{not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := d.AxiomScope(ctx, "svc", run)
	for _, want := range []string{"NICHT gelesen", "benannte Lücke", "GESAMTE Repository", "Abschlussbericht"} {
		if !strings.Contains(out, want) {
			t.Errorf("a damaged pool is not named as a gap, missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "keine frühere Prüfung") {
		t.Errorf("a damaged pool was answered like an empty one:\n%s", out)
	}
}

// A ToDo names no axiom: there is no incremental scope, so nothing is rendered and nothing is
// recorded — an empty heading would be a section pretending to say something.
func TestAxiomScopeIsEmptyWithoutAxioms(t *testing.T) {
	d, path := newScopeDeps(t)
	todo := runs.Run{ID: "run_2", Title: "One-off"}
	if got := d.AxiomScope(context.Background(), "svc", todo); got != "" {
		t.Errorf("a run without axioms rendered a scope section: %q", got)
	}
	if err := d.RecordAxiomScope("svc", todo, "cafebabe1234", time.Now()); err != nil {
		t.Errorf("RecordAxiomScope for a run without axioms: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a run without axioms wrote into the examined-stand pool")
	}
}

// The join reports the error it was handed VERBATIM, so the prompt names the file that is broken and
// the operator can find it. Pinned separately because the section wording is the renderer's job and
// this is the boundary between the two.
func TestAxiomScopeSectionPassesThePoolErrorThrough(t *testing.T) {
	axioms := []mercury.RunAxiom{{ID: "ax_pool", Titel: "Passive Speicher"}}
	out := axiomScopeSection(axioms, map[string]runs.AxiomCheck{}, errors.New("axiom-check pool unreadable: /state/broken.json"))
	if !strings.Contains(out, "/state/broken.json") {
		t.Errorf("the section does not name the unreadable file:\n%s", out)
	}
}
