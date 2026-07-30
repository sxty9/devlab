package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/statepath"
)

func newTestChecks(t *testing.T) (*AxiomChecks, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "axiom-checks.json")
	t.Setenv("DEVLAB_MERCURY_AXIOM_CHECKS", path)
	return NewAxiomChecks(nil), path
}

// The whole point of the pool: what a run recorded, the next run reads back — per repository and per
// axiom, with the commit and the moment. Without the roundtrip every recurring run falls back to
// "never checked ⇒ read the whole repository", which is the cost this store exists to avoid.
func TestAxiomChecksRoundTrip(t *testing.T) {
	checks, path := newTestChecks(t)
	at := time.Date(2026, 7, 26, 22, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

	checks.Record("org/svc", []string{"ax_1", "ax_2"}, "c0ffee", at)

	got := checks.ForRepo("org/svc")
	if len(got) != 2 {
		t.Fatalf("want two recorded examinations, got %+v", got)
	}
	for _, id := range []string{"ax_1", "ax_2"} {
		if got[id].Commit != "c0ffee" {
			t.Errorf("%s: commit = %q, want c0ffee", id, got[id].Commit)
		}
		// The store keeps the moment in UTC, so two instances in different zones read one truth.
		if !got[id].At.Equal(at) || got[id].At.Location() != time.UTC {
			t.Errorf("%s: at = %s, want %s in UTC", id, got[id].At, at.UTC())
		}
	}

	// A second examination of ONE axiom moves that axiom on and leaves the other where it was.
	later := at.Add(24 * time.Hour)
	checks.Record("org/svc", []string{"ax_1"}, "beef", later)
	got = checks.ForRepo("org/svc")
	if got["ax_1"].Commit != "beef" || !got["ax_1"].At.Equal(later) {
		t.Errorf("the re-examined axiom did not move on: %+v", got["ax_1"])
	}
	if got["ax_2"].Commit != "c0ffee" {
		t.Errorf("an untouched axiom must keep its commit: %+v", got["ax_2"])
	}

	// Another repository is its own record — and a fresh handle over the same file sees both, so the
	// record survives a restart.
	checks.Record("org/other", []string{"ax_1"}, "d00d", later)
	reopened := &AxiomChecks{path: path}
	if reopened.ForRepo("org/svc")["ax_1"].Commit != "beef" ||
		reopened.ForRepo("org/other")["ax_1"].Commit != "d00d" {
		t.Errorf("the record did not survive a restart: %+v / %+v",
			reopened.ForRepo("org/svc"), reopened.ForRepo("org/other"))
	}
	// The caller gets a COPY: changing it cannot reach into the pool.
	handed := reopened.ForRepo("org/svc")
	handed["ax_1"] = AxiomCheck{Commit: "tampered"}
	if reopened.ForRepo("org/svc")["ax_1"].Commit != "beef" {
		t.Error("the handed-out map is not a copy — a reader can change the pool")
	}
}

// An empty pool is an empty record, never an error and never a claim: an unexamined repository, an
// unknown repository and a missing file all read as "nothing checked here yet".
func TestAxiomChecksEmptyPool(t *testing.T) {
	checks, path := newTestChecks(t)

	if got := checks.ForRepo("org/svc"); len(got) != 0 {
		t.Fatalf("a missing file must read as empty, got %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("reading must not create the file: %v", err)
	}
	// A nil store (the pool not wired at all) answers nothing instead of panicking.
	var absent *AxiomChecks
	if got := absent.ForRepo("org/svc"); got != nil {
		t.Errorf("an unwired pool must answer nothing, got %+v", got)
	}
	absent.Record("org/svc", []string{"ax_1"}, "c0ffee", time.Now())

	// Recordings without substance are not recorded: no repository, no commit, no axioms, empty ids.
	at := time.Now()
	checks.Record("", []string{"ax_1"}, "c0ffee", at)
	checks.Record("org/svc", []string{"ax_1"}, "", at)
	checks.Record("org/svc", nil, "c0ffee", at)
	checks.Record("org/svc", []string{""}, "c0ffee", at)
	if got := checks.ForRepo("org/svc"); len(got) != 0 {
		t.Errorf("a recording without substance must leave the pool empty, got %+v", got)
	}
}

// A pool that cannot be read is "nothing checked yet", never a false "already checked": the run then
// examines in full — it loses its incrementality, never its correctness. And the next write repairs
// the file rather than inheriting the damage.
func TestAxiomChecksBrokenPool(t *testing.T) {
	checks, path := newTestChecks(t)
	if err := os.WriteFile(path, []byte("{not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := checks.ForRepo("org/svc"); len(got) != 0 {
		t.Fatalf("a damaged pool must read as empty, got %+v", got)
	}

	at := time.Date(2026, 7, 26, 22, 30, 0, 0, time.UTC)
	checks.Record("org/svc", []string{"ax_1"}, "c0ffee", at)
	if got := checks.ForRepo("org/svc"); got["ax_1"].Commit != "c0ffee" {
		t.Errorf("the pool did not heal on the next write: %+v", got)
	}
	// The healed file is readable JSON again for the next handle, not damage carried forward.
	if got := (&AxiomChecks{path: path}).ForRepo("org/svc"); got["ax_1"].Commit != "c0ffee" {
		t.Errorf("a fresh handle cannot read the healed pool: %+v", got)
	}
}

// The path comes from the state root, and the environment override wins — the same seam every other
// Mercury pool uses, so a test (and an operator) can place the file deliberately.
func TestAxiomChecksPath(t *testing.T) {
	root := t.TempDir()
	paths := &statepath.Paths{Root: root}
	t.Setenv("DEVLAB_MERCURY_AXIOM_CHECKS", "")
	if got := NewAxiomChecks(paths).path; got != paths.AxiomChecks() {
		t.Errorf("path = %q, want %q", got, paths.AxiomChecks())
	}
	override := filepath.Join(root, "elsewhere.json")
	t.Setenv("DEVLAB_MERCURY_AXIOM_CHECKS", override)
	if got := NewAxiomChecks(paths).path; got != override {
		t.Errorf("the override must win: path = %q, want %q", got, override)
	}
}
