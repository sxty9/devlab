package deploy

// Setting a service up on this host is HANDED OUT of the devlab service's sandbox — the same escape the
// approved wrapper renewal uses, for the same physical reason.
//
// The devlab service runs with ProtectSystem=strict, and that mount namespace is inherited by every child
// it starts — including this root wrapper, raised by the sudoers pin. /etc is not among its ReadWritePaths,
// on purpose: standing write over the account database would let the service mint system identities
// unattended. But setting a service up for the first time MUST write there (useradd), and against a
// read-only mount being root does not help. So the first genuinely new service since the sandbox was
// tightened found the capability missing — `useradd: cannot lock /etc/passwd; try again later.`, a message
// about a right the sandbox withholds, not about a busy database (measured 2026-08-09).
//
// The fix runs the SAME install outside the cgroup, in one pass, and these tests pin it: the escape is
// taken when a needed path is unreachable, NOT taken when it is reachable, never taken by a dry run, and
// never taken twice. Confinement is simulated exactly as the renewal tests simulate it — by making the
// directory unwritable to the non-root test user, which is what makes `test -w` false.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingSystemdRun writes a systemd-run double that only RECORDS the call and succeeds. It does not
// re-run the handed-out command: what these tests prove is the DECISION to escape and what crosses the
// boundary, and a real re-entry would attempt root writes no test may make.
func recordingSystemdRun(t *testing.T) (path, logPath string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "recording-systemd-run")
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, logPath
}

// handoutCalls reads the recorded systemd-run invocations (empty when the escape was never taken).
func handoutCalls(t *testing.T, logPath string) string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// setupFixture is a complete, well-formed FIRST-TIME setup of a foreign service, with every path the
// hand-out decision measures pointed at a temp directory the test controls. By default all three are
// writable, so nothing is confined and nothing escapes; a test confines the one path it is about.
type setupFixture struct {
	env       map[string]string
	artifact  string
	checkout  string
	unitDir   string
	accountDB string
	permsDir  string
	secretDir string
	srLog     string
}

func newSetupFixture(t *testing.T, repo string) setupFixture {
	t.Helper()
	env, artifact := foreignFixture(t, repo)
	f := setupFixture{
		env:       env,
		artifact:  artifact,
		checkout:  filepath.Dir(artifact),
		unitDir:   t.TempDir(),
		accountDB: t.TempDir(),
		permsDir:  filepath.Join(t.TempDir(), "permissions.d"),
		secretDir: t.TempDir(),
	}
	sr, srLog := recordingSystemdRun(t)
	f.srLog = srLog
	// systemd knows no unit and our unit dir is empty ⇒ this is a first-time setup; the account is absent
	// and no package holds the name, so the reserved gate lets the install through to the boundary.
	f.env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, "")
	f.env["DEVLAB_UNIT_DIR"] = f.unitDir
	f.env["DEVLAB_GETENT"] = fakeGetent(t, nil)
	f.env["DEVLAB_DPKG"] = noPackages(t)
	f.env["DEVLAB_ACCOUNT_DB_DIR"] = f.accountDB
	f.env["DEVLAB_HOLISTIC_PERMS"] = f.permsDir
	f.env["DEVLAB_HOLISTIC_DIR"] = f.secretDir
	f.env["DEVLAB_SYSTEMD_RUN"] = sr
	return f
}

// confineDir makes a directory unwritable to the non-root test user — the same signal a ProtectSystem
// read-only mount gives a confined child in production.
func confineDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func (f setupFixture) install(t *testing.T, repo string, extra ...string) wrapperResult {
	t.Helper()
	args := append([]string{repo, f.artifact, "dev", "--port", "8772"}, extra...)
	return runWrapper(t, "deploy/devlab-install", f.env, args...)
}

// reachedTheWritingHalf guards every "and it did NOT escape" assertion against passing for the wrong
// reason. An install refused anywhere in the validation cascade also calls no systemd-run, so proving
// absence of the escape is only worth something once the run is known to have got PAST the boundary the
// escape sits on. The markers are the first effects beyond it: the first-time setup announcing itself,
// or the program copy into /opt. Both fail afterwards under the non-root test user, which is expected —
// what is asserted here is only how FAR the run got.
func reachedTheWritingHalf(t *testing.T, res wrapperResult) {
	t.Helper()
	if strings.Contains(res.out, "first-time setup of") || strings.Contains(res.out, "/opt/svc-a") {
		return
	}
	t.Fatalf("the run never reached the writing half, so proving it did not escape proves nothing; exit %d\n%s", res.exit, res.out)
}

// DECISIVE: the account database is read-only, so the WHOLE install leaves the cgroup. This is the case
// that stood between a finished implementation and a running service.
func TestSetupHandsTheWholeInstallOutWhenTheAccountDatabaseIsReadOnly(t *testing.T) {
	f := newSetupFixture(t, "svc-a")
	confineDir(t, f.accountDB)

	res := f.install(t, "svc-a")
	if res.exit != 0 {
		t.Fatalf("a confined first-time setup must be handed out and adopt its status, got exit %d\n%s", res.exit, res.out)
	}
	calls := handoutCalls(t, f.srLog)
	if calls == "" {
		t.Fatalf("a first-time setup that cannot write the account database MUST be handed out; systemd-run was never called\n%s", res.out)
	}
	if !strings.Contains(calls, "DEVLAB_SETUP_HANDED_OUT=1") {
		t.Errorf("the escape must carry the recursion marker so the transient unit does the work instead of escaping again; systemd-run got:\n%s", calls)
	}
	if !strings.Contains(calls, "devlab-setup-svc-a-") {
		t.Errorf("the transient unit must name the setup it carries; systemd-run got:\n%s", calls)
	}
	// The handed-out call is THIS tool with the ORIGINAL arguments — not a reconstruction of them.
	if !strings.Contains(calls, "devlab-install svc-a "+f.artifact+" dev --port 8772") {
		t.Errorf("the transient unit must re-enter this very tool with the original arguments; systemd-run got:\n%s", calls)
	}
	if !strings.Contains(res.out, f.accountDB) {
		t.Errorf("the log must name WHICH path forced the escape: %s", res.out)
	}
	// The escape is taken at the boundary between checking and writing, so nothing was written first.
	if ents, err := os.ReadDir(f.unitDir); err != nil || len(ents) != 0 {
		t.Errorf("nothing may be written before the install is handed out; unit dir holds %v (err %v)", ents, err)
	}
}

// The rights directory and the secret directory are the same class and are named individually, so a
// failure says WHICH path is unreachable rather than "something under /etc".
func TestSetupHandsOutForTheRightsAndTheSecretDirectory(t *testing.T) {
	t.Run("rights directory", func(t *testing.T) {
		f := newSetupFixture(t, "svc-a")
		// A manifest in the checkout is what makes the rights directory a path this install must write.
		if err := os.MkdirAll(filepath.Join(f.checkout, "permissions"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.checkout, "permissions", "svc-a.json"), []byte(`{"service":"svc-a"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		// The account already exists, so ONLY the rights directory can force the escape.
		f.env["DEVLAB_GETENT"] = fakeGetent(t, map[string]string{"svc-a": ownShapeAccount("svc-a")})
		confineDir(t, filepath.Dir(f.permsDir))

		res := f.install(t, "svc-a")
		if calls := handoutCalls(t, f.srLog); calls == "" {
			t.Fatalf("an unreachable rights directory must hand the install out; exit %d\n%s", res.exit, res.out)
		}
		if !strings.Contains(res.out, f.permsDir) {
			t.Errorf("the log must name the rights directory as the blocked path: %s", res.out)
		}
	})

	t.Run("secret directory", func(t *testing.T) {
		f := newSetupFixture(t, "svc-a")
		f.env["DEVLAB_GETENT"] = fakeGetent(t, map[string]string{"svc-a": ownShapeAccount("svc-a")})
		confineDir(t, f.secretDir)

		res := f.install(t, "svc-a")
		if calls := handoutCalls(t, f.srLog); calls == "" {
			t.Fatalf("an unreachable secret directory must hand the install out; exit %d\n%s", res.exit, res.out)
		}
		if !strings.Contains(res.out, f.secretDir) {
			t.Errorf("the log must name the secret directory as the blocked path: %s", res.out)
		}
	})
}

// The mirror image, and the one that protects the fourteen services that already deliver every day: where
// the paths ARE reachable — the production receiver, an unconfined root install, a direct-invocation test —
// nothing is handed out and the install runs exactly where it always did.
func TestSetupStaysInPlaceWhenNothingIsConfined(t *testing.T) {
	f := newSetupFixture(t, "svc-a")

	res := f.install(t, "svc-a")
	if calls := handoutCalls(t, f.srLog); calls != "" {
		t.Fatalf("an install that can reach every path it writes must NOT leave the cgroup; systemd-run got:\n%s", calls)
	}
	reachedTheWritingHalf(t, res)
}

// A dry run writes nothing, so it has nothing to escape: --check plans in place even when confined.
func TestSetupCheckNeverHandsOut(t *testing.T) {
	f := newSetupFixture(t, "svc-a")
	confineDir(t, f.accountDB)

	res := f.install(t, "svc-a", "--check")
	if res.exit != 0 {
		t.Fatalf("a confined dry run must still plan, got exit %d\n%s", res.exit, res.out)
	}
	if calls := handoutCalls(t, f.srLog); calls != "" {
		t.Fatalf("a dry run writes nothing and must never be handed out; systemd-run got:\n%s", calls)
	}
	if !strings.Contains(res.out, "PLAN:") {
		t.Errorf("the dry run must still say what it would do: %s", res.out)
	}
}

// The recursion marker is what makes the escape terminate: inside the transient unit the tool does the
// work, it does not hand the same install out again. Without this a confined host would spawn transient
// units forever instead of failing or succeeding once.
func TestSetupHandOutDoesNotLoop(t *testing.T) {
	f := newSetupFixture(t, "svc-a")
	confineDir(t, f.accountDB)
	f.env["DEVLAB_SETUP_HANDED_OUT"] = "1"

	f.install(t, "svc-a")
	if calls := handoutCalls(t, f.srLog); calls != "" {
		t.Fatalf("the transient unit must do the work, never hand the same install out again; systemd-run got:\n%s", calls)
	}
}

// When the path is unreachable AND there is no way out, the setup FAILS BY NAME — naming the path it
// cannot write and what is therefore lost — and writes nothing. Never a silent success, and never a
// fall-through to a write that would fail again.
func TestSetupFailsByNameWhenConfinedAndNoEscape(t *testing.T) {
	f := newSetupFixture(t, "svc-a")
	confineDir(t, f.accountDB)
	f.env["DEVLAB_SYSTEMD_RUN"] = filepath.Join(t.TempDir(), "does-not-exist")

	res := f.install(t, "svc-a")
	if res.exit == 0 {
		t.Fatalf("a confined setup with no escape must fail, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "cannot write "+f.accountDB) {
		t.Errorf("the failure must name the path it cannot write: %s", res.out)
	}
	if !strings.Contains(res.out, "cannot be set up on this host") {
		t.Errorf("the failure must name what is lost: %s", res.out)
	}
	if ents, err := os.ReadDir(f.unitDir); err != nil || len(ents) != 0 {
		t.Errorf("a setup that could not be handed out must write nothing; unit dir holds %v (err %v)", ents, err)
	}
}

// An UPDATE is not excluded by its KIND but by MEASUREMENT — the trap this whole family of defects springs
// from. An update writes no account and no manifest, so it stays in place; but an update whose unit newly
// demands a secret this host does not have must escape for exactly the same measured reason a first-time
// setup does. Same install, same confinement, opposite outcomes — decided by reading the unit, not by
// asking whether the call "looks like" an update.
func TestSetupEscapeOfAnUpdateIsDecidedByTheUnitNotByItsKind(t *testing.T) {
	newUpdate := func(t *testing.T, unitBody string) setupFixture {
		t.Helper()
		f := newSetupFixture(t, "svc-a")
		unit := filepath.Join(f.unitDir, "svc-a.service")
		if err := os.WriteFile(unit, []byte(unitBody), 0o644); err != nil {
			t.Fatal(err)
		}
		// systemd reports OUR unit under this name ⇒ an update, not a first-time setup.
		f.env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, unit)
		f.env["DEVLAB_GETENT"] = fakeGetent(t, map[string]string{"svc-a": ownShapeAccount("svc-a")})
		confineDir(t, f.secretDir)
		return f
	}

	t.Run("demands no secret this host lacks", func(t *testing.T) {
		f := newUpdate(t, "[Service]\nUser=svc-a\nExecStart=/opt/svc-a/bin/svc-ad\n")
		res := f.install(t, "svc-a")
		if calls := handoutCalls(t, f.srLog); calls != "" {
			t.Fatalf("an update that writes none of the confined paths must stay in place; systemd-run got:\n%s", calls)
		}
		reachedTheWritingHalf(t, res)
	})

	t.Run("demands a secret this host lacks", func(t *testing.T) {
		f := newSetupFixture(t, "svc-a")
		unit := filepath.Join(f.unitDir, "svc-a.service")
		body := "[Service]\nUser=svc-a\nEnvironment=HOLISTIC_SECRET_FILE=" + f.secretDir + "/jwt-secret\n"
		if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		f.env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, unit)
		f.env["DEVLAB_GETENT"] = fakeGetent(t, map[string]string{"svc-a": ownShapeAccount("svc-a")})
		confineDir(t, f.secretDir)

		res := f.install(t, "svc-a")
		if calls := handoutCalls(t, f.srLog); calls == "" {
			t.Fatalf("an update whose unit demands a secret this host lacks must escape too; exit %d\n%s", res.exit, res.out)
		}
	})

	t.Run("demands a secret this host already has", func(t *testing.T) {
		f := newSetupFixture(t, "svc-a")
		if err := os.WriteFile(filepath.Join(f.secretDir, "jwt-secret"), []byte("already here"), 0o640); err != nil {
			t.Fatal(err)
		}
		unit := filepath.Join(f.unitDir, "svc-a.service")
		body := "[Service]\nUser=svc-a\nEnvironment=HOLISTIC_SECRET_FILE=" + f.secretDir + "/jwt-secret\n"
		if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		f.env["DEVLAB_SYSTEMCTL"] = fakeSystemctl(t, unit)
		f.env["DEVLAB_GETENT"] = fakeGetent(t, map[string]string{"svc-a": ownShapeAccount("svc-a")})
		confineDir(t, f.secretDir)

		res := f.install(t, "svc-a")
		if calls := handoutCalls(t, f.srLog); calls != "" {
			t.Fatalf("a secret already on this host is never overwritten, so it needs no write and no escape; systemd-run got:\n%s", calls)
		}
		reachedTheWritingHalf(t, res)
	})
}
