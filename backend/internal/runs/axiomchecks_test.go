package runs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// forRepo reads the pool and fails the test on an unexpected error — the shape most assertions want.
func forRepo(t *testing.T, a *AxiomChecks, repo string) map[string]AxiomCheck {
	t.Helper()
	got, err := a.ForRepo(repo)
	if err != nil {
		t.Fatalf("ForRepo(%s): %v", repo, err)
	}
	return got
}

// The whole point of the pool: what a run recorded, the next run reads back — per repository and per
// axiom, with the commit and the moment. Without the roundtrip every recurring run falls back to
// "never checked ⇒ read the whole repository", which is the cost this store exists to avoid.
func TestAxiomChecksRoundTrip(t *testing.T) {
	checks, path := newTestChecks(t)
	at := time.Date(2026, 7, 26, 22, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

	if err := checks.Record("org/svc", []string{"ax_1", "ax_2"}, "c0ffee", at); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := forRepo(t, checks, "org/svc")
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
	if err := checks.Record("org/svc", []string{"ax_1"}, "beef", later); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got = forRepo(t, checks, "org/svc")
	if got["ax_1"].Commit != "beef" || !got["ax_1"].At.Equal(later) {
		t.Errorf("the re-examined axiom did not move on: %+v", got["ax_1"])
	}
	if got["ax_2"].Commit != "c0ffee" {
		t.Errorf("an untouched axiom must keep its commit: %+v", got["ax_2"])
	}

	// Another repository is its own record — and a fresh handle over the same file sees both, so the
	// record survives a restart.
	if err := checks.Record("org/other", []string{"ax_1"}, "d00d", later); err != nil {
		t.Fatalf("Record: %v", err)
	}
	reopened := &AxiomChecks{path: path}
	if forRepo(t, reopened, "org/svc")["ax_1"].Commit != "beef" ||
		forRepo(t, reopened, "org/other")["ax_1"].Commit != "d00d" {
		t.Errorf("the record did not survive a restart: %+v / %+v",
			forRepo(t, reopened, "org/svc"), forRepo(t, reopened, "org/other"))
	}
	// The caller gets a COPY: changing it cannot reach into the pool.
	handed := forRepo(t, reopened, "org/svc")
	handed["ax_1"] = AxiomCheck{Commit: "tampered"}
	if forRepo(t, reopened, "org/svc")["ax_1"].Commit != "beef" {
		t.Error("the handed-out map is not a copy — a reader can change the pool")
	}
}

// An empty pool is an empty record, never an error and never a claim: an unexamined repository, an
// unknown repository and a missing file all read as "nothing checked here yet".
func TestAxiomChecksEmptyPool(t *testing.T) {
	checks, path := newTestChecks(t)

	if got := forRepo(t, checks, "org/svc"); len(got) != 0 {
		t.Fatalf("a missing file must read as empty, got %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("reading must not create the file: %v", err)
	}
	// A nil store (the pool not wired at all) answers nothing instead of panicking.
	var absent *AxiomChecks
	got, err := absent.ForRepo("org/svc")
	if got != nil || err != nil {
		t.Errorf("an unwired pool must answer nothing, got %+v / %v", got, err)
	}
	if err := absent.Record("org/svc", []string{"ax_1"}, "c0ffee", time.Now()); err != nil {
		t.Errorf("recording into an unwired pool must be a no-op, got %v", err)
	}

	// Recordings without substance are not recorded: no repository, no commit, no axioms, empty ids.
	at := time.Now()
	for _, bad := range []func() error{
		func() error { return checks.Record("", []string{"ax_1"}, "c0ffee", at) },
		func() error { return checks.Record("org/svc", []string{"ax_1"}, "", at) },
		func() error { return checks.Record("org/svc", nil, "c0ffee", at) },
		func() error { return checks.Record("org/svc", []string{""}, "c0ffee", at) },
	} {
		if err := bad(); err != nil {
			t.Errorf("a recording without substance must be a silent no-op, got %v", err)
		}
	}
	if got := forRepo(t, checks, "org/svc"); len(got) != 0 {
		t.Errorf("a recording without substance must leave the pool empty, got %+v", got)
	}
}

// A pool with no path configured writes nowhere — it must not fall back to a relative name and drop
// a file (or a staging file) into whatever directory the daemon happens to run in.
func TestAxiomChecksUnconfiguredPathWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	unconfigured := &AxiomChecks{}
	if err := unconfigured.Record("org/svc", []string{"ax_1"}, "c0ffee", time.Now()); err != nil {
		t.Errorf("an unconfigured pool must be a no-op, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("an unconfigured pool wrote into the working directory: %v", entries)
	}
}

// A pool that cannot be read is never answered as "nothing checked yet" — that is the one lie that is
// also destructive: the next write would persist the emptiness over the damaged file and take every
// OTHER repository and axiom in it along. So the read names the damage, and the write sets the file
// aside (bytes untouched, still salvageable) before starting a fresh record, and says where it went.
func TestAxiomChecksDamagedPoolIsNamedAndSetAside(t *testing.T) {
	checks, path := newTestChecks(t)
	// A truncated record: unreadable as a whole, yet it still carries the only copy of what other
	// repositories were examined against.
	damaged := `{"repos":{"org/svc":{"ax_1":{"commit":"c0ffee"}},"org/other":{"ax_9":{"commit":"dec0`
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := checks.ForRepo("org/svc")
	if !errors.Is(err, ErrAxiomChecksUnreadable) {
		t.Fatalf("a damaged pool must be named on read, got err=%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a damaged pool must not claim any examination, got %+v", got)
	}

	at := time.Date(2026, 7, 26, 22, 30, 0, 0, time.UTC)
	err = checks.Record("org/svc", []string{"ax_1"}, "beef", at)
	if !errors.Is(err, ErrAxiomChecksUnreadable) {
		t.Fatalf("writing over a damaged pool must be reported, got err=%v", err)
	}

	// The report says where the evidence is, and the evidence is the original bytes.
	sidecar := ""
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".damaged-") {
			sidecar = filepath.Join(filepath.Dir(path), e.Name())
		}
	}
	if sidecar == "" {
		t.Fatalf("the damaged pool was replaced instead of set aside; directory: %v", entries)
	}
	if !strings.Contains(err.Error(), filepath.Base(sidecar)) {
		t.Errorf("the report does not name the file it set aside (%s): %v", sidecar, err)
	}
	kept, readErr := os.ReadFile(sidecar)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(kept) != damaged {
		t.Errorf("the set-aside copy is not the original: %q", kept)
	}

	// The pool itself is healthy again and holds the new record — the run keeps working.
	if got := forRepo(t, checks, "org/svc"); got["ax_1"].Commit != "beef" {
		t.Errorf("the pool did not heal on the next write: %+v", got)
	}
	if got := forRepo(t, &AxiomChecks{path: path}, "org/svc"); got["ax_1"].Commit != "beef" {
		t.Errorf("a fresh handle cannot read the healed pool: %+v", got)
	}

	// A second damage does not overwrite the first one's evidence.
	if err := os.WriteFile(path, []byte("{again broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checks.Record("org/svc", []string{"ax_1"}, "cafe", at); !errors.Is(err, ErrAxiomChecksUnreadable) {
		t.Fatalf("the second damage must be reported too, got %v", err)
	}
	sidecars := 0
	entries, readErr = os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".damaged-") {
			sidecars++
		}
	}
	if sidecars != 2 {
		t.Errorf("want two set-aside copies (one per damage), got %d: %v", sidecars, entries)
	}
}

// If the damaged file cannot even be moved out of the way, nothing is written: an unreadable file may
// still be salvageable, and a store that cannot preserve it must not destroy it.
func TestAxiomChecksDamagedPoolThatCannotBeSetAsideIsNotOverwritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the rename cannot be made to fail")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "axiom-checks.json")
	damaged := "{not json at all"
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // readable, not writable: no rename, no new file
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	checks := &AxiomChecks{path: path}
	err := checks.Record("org/svc", []string{"ax_1"}, "beef", time.Date(2026, 7, 26, 22, 30, 0, 0, time.UTC))
	if !errors.Is(err, ErrAxiomChecksUnreadable) {
		t.Fatalf("want the damage named, got %v", err)
	}
	b, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(b) != damaged {
		t.Errorf("the damaged file was overwritten although it could not be preserved: %q", b)
	}
}

// A pool the process may not read is a read error, not an empty record.
func TestAxiomChecksUnreadableFileIsNamed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads regardless of the mode")
	}
	checks, path := newTestChecks(t)
	if err := os.WriteFile(path, []byte(`{"repos":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	got, err := checks.ForRepo("org/svc")
	if !errors.Is(err, ErrAxiomChecksUnreadable) {
		t.Fatalf("an unreadable pool must be named, got %+v / %v", got, err)
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
