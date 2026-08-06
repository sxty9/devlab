package deploy

// devlab-install-recv is the operator's one-pass installer for the prod-side receiver — the one root
// artifact the delivery chain cannot deliver to itself (the forced-command deploy key can only rsync
// into staging and trigger an install, never overwrite the receiver). These tests prove its logic in
// its DIRECT-INVOCATION test seam (DEVLAB_RECV_TEST=1, no root, a fixture sbin), the same way the
// receiver's own decisions are proved in --check mode: a fresh install lands both files, a re-run is
// idempotent, an existing copy is backed up before replacement, and a service whose staged setup is
// missing is NEVER triggered (fail-closed, no half-setup).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installRecvEnv builds a fixture sbin + staging and returns the env the installer runs against under
// its test seam. When stageOK, a first-time-ready artifact for repo is staged (binary + delivered unit
// running as User=<repo>); otherwise the artifact is staged WITHOUT its setup/ product, which the
// receiver must refuse.
func installRecvEnv(t *testing.T, repo string, stageOK bool) (env map[string]string, sbin string) {
	t.Helper()
	root := t.TempDir()
	sbin = filepath.Join(root, "sbin")
	staging := filepath.Join(root, "staging")
	art := filepath.Join(staging, repo)
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, repo+"d"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if stageOK {
		setup := filepath.Join(art, "setup")
		if err := os.MkdirAll(setup, 0o755); err != nil {
			t.Fatal(err)
		}
		unit := "[Service]\nUser=" + repo + "\nExecStart=/opt/" + repo + "/bin/" + repo + "d --listen 127.0.0.1:8811\n"
		if err := os.WriteFile(filepath.Join(setup, repo+".service"), []byte(unit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env = map[string]string{
		"DEVLAB_RECV_TEST": "1",
		"DEVLAB_SBIN":      sbin,
		"DEVLAB_STAGING":   staging,
		"DEVLAB_UNIT_DIR":  filepath.Join(root, "units"), // empty → systemd knows no unit → first-time
		"DEVLAB_SYSTEMCTL": fakeSystemctl(t, ""),         // empty FragmentPath → first-time
	}
	return env, sbin
}

func runInstallRecv(t *testing.T, env map[string]string, args ...string) wrapperResult {
	t.Helper()
	return runWrapper(t, "deploy/devlab-install-recv", env, args...)
}

// A fresh install lands BOTH files (the sourced library and the receiver) into the fixture sbin; with
// no service named it triggers nothing.
func TestInstallRecvFreshInstall(t *testing.T) {
	env, sbin := installRecvEnv(t, "svc-a", true)
	res := runInstallRecv(t, env)
	if res.exit != 0 {
		t.Fatalf("a fresh install must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	for _, f := range []string{"devlab-deploy-recv", "devlab-setup-lib.sh"} {
		if _, err := os.Stat(filepath.Join(sbin, f)); err != nil {
			t.Errorf("the installer must land %s: %v", f, err)
		}
	}
	if !strings.Contains(res.out, "nothing was triggered") {
		t.Errorf("with no service named nothing should be triggered:\n%s", res.out)
	}
}

// A second run installs nothing new — an already-current receiver is left untouched, so re-running is
// safe (idempotent).
func TestInstallRecvIdempotent(t *testing.T) {
	env, _ := installRecvEnv(t, "svc-a", true)
	if res := runInstallRecv(t, env); res.exit != 0 {
		t.Fatalf("first install failed: %d\n%s", res.exit, res.out)
	}
	res := runInstallRecv(t, env)
	if res.exit != 0 {
		t.Fatalf("the idempotent re-run must succeed (exit 0), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "already current") {
		t.Errorf("a re-run must report the receiver as already current:\n%s", res.out)
	}
}

// An existing (older) copy is backed up before it is replaced, so the operator can always step back to
// the receiver that was running before.
func TestInstallRecvBacksUpExisting(t *testing.T) {
	env, sbin := installRecvEnv(t, "svc-a", true)
	old := filepath.Join(sbin, "devlab-deploy-recv")
	if err := os.WriteFile(old, []byte("OLD RECEIVER"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := runInstallRecv(t, env)
	if res.exit != 0 {
		t.Fatalf("install over an existing receiver must succeed, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "backed up the existing devlab-deploy-recv") {
		t.Errorf("replacing an existing receiver must back it up:\n%s", res.out)
	}
	// The backup carries the OLD bytes.
	entries, _ := filepath.Glob(filepath.Join(sbin, "devlab-deploy-recv.bak-*"))
	if len(entries) == 0 {
		t.Fatalf("no backup file was written:\n%s", res.out)
	}
	if b, _ := os.ReadFile(entries[0]); string(b) != "OLD RECEIVER" {
		t.Errorf("the backup must hold the replaced bytes, got %q", string(b))
	}
}

// A named service whose staged artifact is MISSING its setup/ product is refused by the receiver's
// dry-run and therefore NEVER triggered — the installer fails closed (exit non-zero) rather than
// half-set-up a service, and the receiver is still installed for the others.
func TestInstallRecvFailsClosedOnMissingSetup(t *testing.T) {
	env, sbin := installRecvEnv(t, "svc-a", false) // no setup/ staged
	res := runInstallRecv(t, env, "svc-a")
	if res.exit == 0 {
		t.Fatalf("a service with no delivered setup must not be reported as done (exit != 0):\n%s", res.out)
	}
	if !strings.Contains(res.out, "will NOT be triggered") {
		t.Errorf("the installer must refuse to trigger a service whose dry-run did not pass:\n%s", res.out)
	}
	// The receiver itself was still installed — the fault is the service's, not the receiver's.
	if _, err := os.Stat(filepath.Join(sbin, "devlab-deploy-recv")); err != nil {
		t.Errorf("the receiver must be installed even when a named service is refused: %v", err)
	}
}
