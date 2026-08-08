package deploy

// The PROOF for the root-wrapper write half (task point 7). The bash tests drive the root tool
// directly (no sudo) through the env seams the wrapper already exposes, exactly like wrapper_test.go:
//   (a) an approval for content A installs nothing when handed content B,
//   (c) an approval already used installs nothing a second time,
//   (d) a name outside the four is refused,
//   (e) after an install, who approved and what was there before is readable, and the previous
//       content is kept for a rollback.
// (b) — the source is committed history, never an unpinned working tree — is proven at the Go level
// (TestDeliveringBranchWrapperDriftReadsTheRunsBranch): the offered checksum is the delivering branch's
// committed content, never the working tree's, so content that is not committed to the branch is never
// what the approval (and thus the install) is pinned to.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sha256of(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// renewFixture stages the temp state root, sbin dir and audit dir, and returns the env seams plus the
// grant directory (under the state root but OUTSIDE the run-writable workspaces subtree).
type renewFixture struct {
	env       map[string]string
	stateDir  string
	sbinDir   string
	auditDir  string
	grantsDir string
}

func newRenewFixture(t *testing.T) renewFixture {
	t.Helper()
	state := t.TempDir()
	sbin := filepath.Join(state, "sbin")
	audit := filepath.Join(state, "audit")
	grants := filepath.Join(state, "mercury", "wrapper-grants")
	for _, d := range []string{sbin, grants, filepath.Join(state, "workspaces")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return renewFixture{
		env: map[string]string{
			"DEVLAB_STATE_DIR":         state,
			"DEVLAB_SBIN_DIR":          sbin,
			"DEVLAB_WRAPPER_AUDIT_DIR": audit,
		},
		stateDir: state, sbinDir: sbin, auditDir: audit, grantsDir: grants,
	}
}

// grantFile is ONE file of an approval, as a test states it: the wrapper name, its content, and the
// sha the grant pins for it. pinSHA lets a test pin a checksum that deliberately disagrees with the
// content (test a); when empty, the sha of content is used.
type grantFile struct {
	name    string
	content []byte
	pinSHA  string
}

func gf(name string, content []byte) grantFile { return grantFile{name: name, content: content} }

// writeApproval stages the content of every file plus ONE combined grant covering the whole approval,
// and returns the grant path and the ordered <name> <content-path> argument list — exactly the shape
// the root tool now takes (`--renew-wrapper <grant> <name> <content> …`). This mirrors the daemon's
// stageWrapperGrantSet: one approval, one grant, its files spent as one unit.
func (f renewFixture) writeApproval(t *testing.T, id, by string, files ...grantFile) (grantPath string, args []string) {
	t.Helper()
	lines := []string{"approvalId=" + id, "approvedBy=" + by, "approvedAt=2026-08-02T12:00:00Z"}
	for _, w := range files {
		sha := w.pinSHA
		if sha == "" {
			sha = sha256of(w.content)
		}
		contentPath := filepath.Join(f.grantsDir, id+"."+w.name+".content")
		if err := os.WriteFile(contentPath, w.content, 0o640); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, "sha256 "+w.name+" "+sha)
		args = append(args, w.name, contentPath)
	}
	lines = append(lines, "")
	grantPath = filepath.Join(f.grantsDir, id+".grant")
	if err := os.WriteFile(grantPath, []byte(strings.Join(lines, "\n")), 0o640); err != nil {
		t.Fatal(err)
	}
	return grantPath, args
}

func (f renewFixture) renew(t *testing.T, grantPath string, args ...string) wrapperResult {
	return runWrapper(t, "deploy/devlab-install", f.env, append([]string{"--renew-wrapper", grantPath}, args...)...)
}

// (a) A confirmation for content A installs content B NOT.
func TestRenewRefusesContentThatMismatchesTheApprovedChecksum(t *testing.T) {
	f := newRenewFixture(t)
	approvedSHA := sha256of([]byte("APPROVED CONTENT A\n"))
	// The staged content is B, but the grant carries the checksum of A.
	g, args := f.writeApproval(t, "qst_a1", "operator",
		grantFile{name: "devlab-exec", content: []byte("DIFFERENT CONTENT B\n"), pinSHA: approvedSHA})
	res := f.renew(t, g, args...)
	if res.exit == 0 {
		t.Fatalf("expected a refusal, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "does not match the approved") {
		t.Fatalf("expected a checksum-mismatch refusal, got:\n%s", res.out)
	}
	if _, err := os.Stat(filepath.Join(f.sbinDir, "devlab-exec")); !os.IsNotExist(err) {
		t.Fatalf("nothing must be installed on a mismatch")
	}
}

// (c) An already-used confirmation does not take effect a second time.
func TestRenewIsSingleUse(t *testing.T) {
	f := newRenewFixture(t)
	content := []byte("#!/usr/bin/env bash\necho merged devlab-exec\n")
	g, args := f.writeApproval(t, "qst_c1", "operator", gf("devlab-exec", content))

	if res := f.renew(t, g, args...); res.exit != 0 {
		t.Fatalf("first renewal should succeed, got exit %d\n%s", res.exit, res.out)
	}
	if got, _ := os.ReadFile(filepath.Join(f.sbinDir, "devlab-exec")); string(got) != string(content) {
		t.Fatalf("first renewal did not install the approved content")
	}
	// The same grant/approval a second time installs nothing.
	res := f.renew(t, g, args...)
	if res.exit == 0 || !strings.Contains(res.out, "already used") {
		t.Fatalf("expected a single-use refusal, got exit %d\n%s", res.exit, res.out)
	}
}

// (d) A path/name outside the renewable list is refused.
func TestRenewRefusesUnknownName(t *testing.T) {
	f := newRenewFixture(t)
	content := []byte("malicious\n")
	g, args := f.writeApproval(t, "qst_d1", "operator", gf("evil-tool", content))
	res := f.renew(t, g, args...)
	if res.exit == 0 || !strings.Contains(res.out, "not a renewable root wrapper") {
		t.Fatalf("expected refusal of an unknown wrapper name, got exit %d\n%s", res.exit, res.out)
	}
	// And a real wrapper name whose content is fine still cannot be installed from a run-writable
	// place: a source under the workspaces subtree is refused (defense for task point 1).
	g2, _ := f.writeApproval(t, "qst_d2", "operator", gf("devlab-exec", content))
	ws := filepath.Join(f.stateDir, "workspaces", "runner")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	wc := filepath.Join(ws, "x.content")
	if err := os.WriteFile(wc, content, 0o644); err != nil {
		t.Fatal(err)
	}
	res2 := f.renew(t, g2, "devlab-exec", wc)
	if res2.exit == 0 || !strings.Contains(res2.out, "workspaces") {
		t.Fatalf("expected refusal of a run-writable source, got exit %d\n%s", res2.exit, res2.out)
	}
}

// (b) MULTI-FILE ATOMIC — the decisive regression. ONE approval covers THREE wrapper files. Before this
// fix each file was a separate root call under the same approvalId, so the first file's ledger entry
// made the second read as "already used" and the renewal stopped half-done. Now the whole set installs
// in one call: all three land, the approval is spent exactly ONCE, and a second use of the same approval
// installs nothing.
func TestRenewInstallsAMultiFileApprovalAtomically(t *testing.T) {
	f := newRenewFixture(t)
	exec := []byte("#!/usr/bin/env bash\n# merged devlab-exec\n")
	inst := []byte("#!/usr/bin/env bash\n# merged devlab-install\n")
	lib := []byte("# shellcheck shell=bash\n# merged devlab-setup-lib.sh\n")
	g, args := f.writeApproval(t, "qst_multi", "operator",
		gf("devlab-exec", exec), gf("devlab-install", inst), gf("devlab-setup-lib.sh", lib))

	if res := f.renew(t, g, args...); res.exit != 0 {
		t.Fatalf("a multi-file approval must install every file in one pass, got exit %d\n%s", res.exit, res.out)
	}
	for name, want := range map[string][]byte{"devlab-exec": exec, "devlab-install": inst, "devlab-setup-lib.sh": lib} {
		got, err := os.ReadFile(filepath.Join(f.sbinDir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s was not installed from the multi-file approval (the half-renewal defect); err=%v", name, err)
		}
	}
	// The ledger records ONE approval consumed across all three files.
	ledger, err := os.ReadFile(filepath.Join(f.auditDir, "installed.log"))
	if err != nil {
		t.Fatalf("no audit ledger: %v", err)
	}
	if n := strings.Count(string(ledger), "approvalId=qst_multi "); n != 3 {
		t.Fatalf("expected one ledger line per installed file under the one approval, got %d:\n%s", n, ledger)
	}
	// The whole approval is single-use: a second full use of the SAME approval installs nothing.
	res := f.renew(t, g, args...)
	if res.exit == 0 || !strings.Contains(res.out, "already used") {
		t.Fatalf("the whole approval must be single-use, got exit %d\n%s", res.exit, res.out)
	}
}

// (b, the refusal half) ALL-OR-NONE ON A BAD FILE: if ONE file of a multi-file approval fails validation
// (its content no longer matches the approved checksum), NONE of the set is installed — not even the
// files that would have passed. The approval is left unconsumed so a corrected delivery can still redeem
// it (task points 1/4).
func TestRenewMultiFileIsAllOrNoneWhenOneFileMismatches(t *testing.T) {
	f := newRenewFixture(t)
	good := []byte("#!/usr/bin/env bash\n# good devlab-exec\n")
	// devlab-install's grant pins the checksum of one content but the staged content is another.
	g, args := f.writeApproval(t, "qst_partial", "operator",
		gf("devlab-exec", good),
		grantFile{name: "devlab-install", content: []byte("STAGED B\n"), pinSHA: sha256of([]byte("APPROVED A\n"))})

	res := f.renew(t, g, args...)
	if res.exit == 0 || !strings.Contains(res.out, "does not match the approved") {
		t.Fatalf("a set with one bad file must be refused whole, got exit %d\n%s", res.exit, res.out)
	}
	// The GOOD file must NOT have been installed — validation happens for the whole set before any write.
	if _, err := os.Stat(filepath.Join(f.sbinDir, "devlab-exec")); !os.IsNotExist(err) {
		t.Fatalf("no file of the set may be installed when another file is bad (all-or-none)")
	}
	// And the approval is unconsumed: the ledger holds nothing for it.
	if ledger, _ := os.ReadFile(filepath.Join(f.auditDir, "installed.log")); strings.Contains(string(ledger), "qst_partial") {
		t.Fatalf("a renewal that wrote nothing must not consume the approval:\n%s", ledger)
	}
}

// Defect #1 closed, measured end-to-end: the sourced setup library is now a RENEWABLE name, and the
// root tool installs it READ-ONLY (0644), not executable. Before this change the daemon could guard
// devlab-setup-lib.sh and offer it for approval, but this tool refused the very name the human
// approved. Here an approval for it installs it, at mode 0644.
func TestRenewInstallsSetupLibraryReadOnly(t *testing.T) {
	f := newRenewFixture(t)
	content := []byte("# shellcheck shell=bash\n# merged devlab-setup-lib.sh\n")
	g, args := f.writeApproval(t, "qst_lib1", "operator", gf("devlab-setup-lib.sh", content))

	if res := f.renew(t, g, args...); res.exit != 0 {
		t.Fatalf("renewing the setup library must succeed (defect #1), got exit %d\n%s", res.exit, res.out)
	}
	dest := filepath.Join(f.sbinDir, "devlab-setup-lib.sh")
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(content) {
		t.Fatalf("the approved library content was not installed: err=%v", err)
	}
	// The library is SOURCED, never executed: it must land 0644, not 0755. The tool ran unprivileged
	// here (own_root is a no-op off root), so the explicit chmod is what the mode reflects.
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("the sourced setup library must be installed read-only (0644), got %o", perm)
	}
}

// (e) After an install, who approved it and what was there before are readable, and the previous
// content is kept so a return to the earlier stand is possible.
func TestRenewIsAuditableAndReversible(t *testing.T) {
	f := newRenewFixture(t)
	dest := filepath.Join(f.sbinDir, "devlab-install")
	old := []byte("#!/usr/bin/env bash\n# OLD devlab-install\n")
	if err := os.WriteFile(dest, old, 0o755); err != nil {
		t.Fatal(err)
	}
	oldSHA := sha256of(old)

	newContent := []byte("#!/usr/bin/env bash\n# NEW merged devlab-install\n")
	newSHA := sha256of(newContent)
	g, args := f.writeApproval(t, "qst_e1", "alice", gf("devlab-install", newContent))

	if res := f.renew(t, g, args...); res.exit != 0 {
		t.Fatalf("renewal should succeed, got exit %d\n%s", res.exit, res.out)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(newContent) {
		t.Fatalf("the new merged content was not installed")
	}

	// The ledger records who approved, what was installed, and what was there before.
	ledger, err := os.ReadFile(filepath.Join(f.auditDir, "installed.log"))
	if err != nil {
		t.Fatalf("no audit ledger: %v", err)
	}
	for _, want := range []string{"approvedBy=alice", "approvalId=qst_e1", "newSha=" + newSHA, "oldSha=" + oldSHA, "name=devlab-install"} {
		if !strings.Contains(string(ledger), want) {
			t.Fatalf("ledger missing %q:\n%s", want, ledger)
		}
	}
	// The previous content is backed up — a rollback is possible.
	backups, _ := filepath.Glob(filepath.Join(f.auditDir, "backups", "devlab-install.*."+oldSHA))
	if len(backups) != 1 {
		t.Fatalf("expected exactly one backup of the previous content, got %v", backups)
	}
	if got, _ := os.ReadFile(backups[0]); string(got) != string(old) {
		t.Fatalf("the backup does not hold the previous content")
	}
}


// ── the write is HANDED OUT of the service's cgroup when confined (task point 1, tests a/b/d) ──────
// The whole defect was physical: the devlab service runs ProtectSystem=strict without /usr/local/sbin in
// its ReadWritePaths, so the confined wrapper could not write the target and an approved renewal never
// took effect. The fix runs the SAME renewal in a transient unit outside the cgroup (like the self-repo
// restart). These tests drive that escape through the DEVLAB_SYSTEMD_RUN seam: a fake stands in for the
// transient unit — it records the call, applies the --setenv values, and makes the target writable (the
// world outside the sandbox), then runs the handed-out command. Confinement is simulated by making the
// sbin dir unwritable to the (non-root) test user, which is exactly what makes `test -w` false.

// fakeSystemdRun writes a test double for systemd-run and returns its path plus the log file it appends
// each invocation to. The double proves the tool escaped: it applies the --setenv env (carrying the
// recursion marker), simulates leaving the sandbox by making DEVLAB_SBIN_DIR writable, and execs the
// handed-out command that follows `--`.
func fakeSystemdRun(t *testing.T) (path, logPath string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "fake-systemd-run")
	logPath = filepath.Join(dir, "calls.log")
	script := "#!/usr/bin/env bash\n" +
		"set -euo pipefail\n" +
		"printf '%s\\n' \"$*\" >> \"" + logPath + "\"\n" +
		"declare -a cmd=(); after=0\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$after\" = 1 ]; then cmd+=(\"$a\"); continue; fi\n" +
		"  case \"$a\" in\n" +
		"    --setenv=*) export \"${a#--setenv=}\" ;;\n" +
		"    --) after=1 ;;\n" +
		"  esac\n" +
		"done\n" +
		// Stand in for escaping the cgroup: outside the service's ProtectSystem sandbox the wrapper dir is
		// writable. The handed-out command then writes it for real.
		"chmod u+w \"$DEVLAB_SBIN_DIR\"\n" +
		"exec \"${cmd[@]}\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, logPath
}

// confine makes the sbin dir unwritable to the non-root test user, so `test -w` in the wrapper is false —
// the same signal the ProtectSystem read-only mount gives a confined child in production.
func confine(t *testing.T, f renewFixture) {
	t.Helper()
	if err := os.Chmod(f.sbinDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.sbinDir, 0o755) })
}

// (task test a) DECISIVE: when the target is read-only, the approved renewal is HANDED OUT of the cgroup
// and the file ends up carrying the approved checksum — measured, not asserted. And the escape passed the
// recursion marker, so the transient unit does the work instead of looping back.
func TestRenewHandsWriteOutWhenTargetIsReadOnly(t *testing.T) {
	f := newRenewFixture(t)
	sr, srLog := fakeSystemdRun(t)
	f.env["DEVLAB_SYSTEMD_RUN"] = sr
	confine(t, f)

	content := []byte("#!/usr/bin/env bash\necho merged devlab-install\n")
	sha := sha256of(content)
	g, args := f.writeApproval(t, "qst_h1", "operator", gf("devlab-install", content))

	res := f.renew(t, g, args...)
	if res.exit != 0 {
		t.Fatalf("a confined renewal must be handed out and succeed, got exit %d\n%s", res.exit, res.out)
	}
	got, err := os.ReadFile(filepath.Join(f.sbinDir, "devlab-install"))
	if err != nil || sha256of(got) != sha {
		t.Fatalf("after an approved handed-out renewal the file must carry the approved checksum; err=%v got sha %s want %s", err, sha256of(got), sha)
	}
	log, _ := os.ReadFile(srLog)
	if !strings.Contains(string(log), "DEVLAB_WRENEW_HANDED_OUT=1") {
		t.Fatalf("the escape must carry the recursion marker so the transient unit does the work, not loop; systemd-run got:\n%s", log)
	}
	if !strings.Contains(string(log), "devlab-wrapper-renew-") {
		t.Fatalf("the escape must name a transient renewal unit; systemd-run got:\n%s", log)
	}
}

// (task test b, wrapper layer) When the target is read-only and there is NO way to hand out (systemd-run
// absent), the renewal FAILS BY NAME with the real reason and installs nothing — never a silent success
// and never a fall-through to a write that would fail again (the loop). The daemon turns this failure into
// one disturbance, not a repeat of the same question.
func TestRenewFailsByNameWhenConfinedAndNoEscape(t *testing.T) {
	f := newRenewFixture(t)
	f.env["DEVLAB_SYSTEMD_RUN"] = filepath.Join(t.TempDir(), "does-not-exist")
	confine(t, f)

	content := []byte("#!/usr/bin/env bash\necho merged\n")
	g, args := f.writeApproval(t, "qst_h2", "operator", gf("devlab-install", content))

	res := f.renew(t, g, args...)
	if res.exit == 0 {
		t.Fatalf("a confined renewal with no escape must fail, got exit 0\n%s", res.out)
	}
	if !strings.Contains(res.out, "cannot write") || !strings.Contains(res.out, "cannot be installed") {
		t.Fatalf("the failure must name the real reason (read-only sbin, no escape), got:\n%s", res.out)
	}
	if _, err := os.Stat(filepath.Join(f.sbinDir, "devlab-install")); !os.IsNotExist(err) {
		t.Fatalf("nothing must be installed when the renewal could not be handed out")
	}
}

// (task tests c/d, through the escape) The hand-out does NOT weaken the four bindings: a handed-out
// renewal whose content no longer matches the approved checksum is refused OUTSIDE the cgroup too, and
// nothing is installed. So even the escaped, unconfined path cannot change a root script without a valid,
// content-pinned approval.
func TestHandoutStillEnforcesTheApprovedChecksum(t *testing.T) {
	f := newRenewFixture(t)
	sr, _ := fakeSystemdRun(t)
	f.env["DEVLAB_SYSTEMD_RUN"] = sr
	confine(t, f)

	approvedSHA := sha256of([]byte("APPROVED CONTENT A\n"))
	// Staged content is B; the grant pins the checksum of A. The escape runs, but the re-check outside
	// refuses before any write.
	g, args := f.writeApproval(t, "qst_h3", "operator",
		grantFile{name: "devlab-exec", content: []byte("DIFFERENT CONTENT B\n"), pinSHA: approvedSHA})

	res := f.renew(t, g, args...)
	if res.exit == 0 || !strings.Contains(res.out, "does not match the approved") {
		t.Fatalf("a handed-out renewal must still refuse content that mismatches the approved checksum, got exit %d\n%s", res.exit, res.out)
	}
	if _, err := os.Stat(filepath.Join(f.sbinDir, "devlab-exec")); !os.IsNotExist(err) {
		t.Fatalf("nothing must be installed on a checksum mismatch, even through the hand-out")
	}
}

// DeliveringBranchWrapperDrift offers THIS run's own delivering-branch content — the second renewal
// source, for when the run itself changed a root script that is not yet merged. It reports the drift
// against the installed copy and pins the offered checksum to the delivering branch. With no branch ref
// resolvable (a bare working tree), it falls back to the working tree — the run's checkout.
func TestDeliveringBranchWrapperDriftReadsTheRunsBranch(t *testing.T) {
	wt := t.TempDir()

	// The run changed devlab-exec on its branch (a bare working tree here, so the source falls back to
	// the working tree — exactly the case the stack tip cannot serve).
	working := []byte("#!/usr/bin/env bash\n# THIS RUN's devlab-exec\n")
	writeRepoFile(t, wt, "deploy/devlab-exec", working)

	sbin := filepath.Join(t.TempDir(), "sbin")
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbin, "devlab-exec"), []byte("stale installed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	restore := wrapperInstallDir
	wrapperInstallDir = sbin
	t.Cleanup(func() { wrapperInstallDir = restore })

	drifts, err := DeliveringBranchWrapperDrift(wt, "fix/change-a-root-script")
	if err != nil {
		t.Fatal(err)
	}
	got := driftNamed(drifts, "devlab-exec")
	if got == nil {
		t.Fatalf("devlab-exec delivering-branch drift not reported, got %+v", drifts)
	}
	if got.WantSHA != sha256of(working) || string(got.WantContent) != string(working) {
		t.Fatalf("the offered content must be THIS run's content; got sha %s want %s", got.WantSHA, sha256of(working))
	}
	// The drift carries a SHORT change summary (task point 4) — a human reads it, not the full content.
	if !strings.Contains(got.Summary, "lines") {
		t.Fatalf("the drift must summarize the change for the question, got %q", got.Summary)
	}

	// When the installed copy already matches the delivering branch, there is no drift to renew.
	if err := os.WriteFile(filepath.Join(sbin, "devlab-exec"), working, 0o755); err != nil {
		t.Fatal(err)
	}
	drifts, err = DeliveringBranchWrapperDrift(wt, "fix/change-a-root-script")
	if err != nil {
		t.Fatal(err)
	}
	if d := driftNamed(drifts, "devlab-exec"); d != nil {
		t.Fatalf("no drift expected once the installed copy matches the delivering branch, got %+v", d)
	}
}

// driftNamed returns the drift for one wrapper name, or nil.
func driftNamed(drifts []WrapperDrift, name string) *WrapperDrift {
	for i := range drifts {
		if drifts[i].Name == name {
			return &drifts[i]
		}
	}
	return nil
}

// fakeRenewer records whether the root write step was reached at all — a refusal before the write
// (a source no longer matches the approval) must never call it — and which files one call carried, so a
// test can prove the whole approval reached root as ONE invocation.
type fakeRenewer struct{ calls [][]string }

func (f *fakeRenewer) Renew(_ context.Context, _ string, files []WrapperFile) error {
	names := make([]string, len(files))
	for i, fl := range files {
		names[i] = fl.Name
	}
	f.calls = append(f.calls, names)
	return nil
}

// A working-source approval whose files all match reaches the root write step as ONE call carrying the
// whole set; but if the run's branch CHANGES one file after the approval, the re-read no longer hashes to
// the approved checksum and RenewApprovedWrapperSet refuses the WHOLE set BEFORE the write — nothing is
// installed (task points 3/4, the working-source half of "the branch changed after approval installs
// nothing", now all-or-none across the set).
func TestRenewApprovedWrapperSetBindsWorkingSourceToTheApprovedChecksum(t *testing.T) {
	wt := t.TempDir()
	grantDir := filepath.Join(t.TempDir(), "grants")

	approvedInstall := []byte("#!/usr/bin/env bash\n# approved run content\n")
	approvedExec := []byte("#!/usr/bin/env bash\n# approved exec content\n")
	writeRepoFile(t, wt, "deploy/devlab-install", approvedInstall)
	writeRepoFile(t, wt, "deploy/devlab-exec", approvedExec)
	set := []ApprovedWrapper{
		{Name: "devlab-install", SHA: sha256of(approvedInstall)},
		{Name: "devlab-exec", SHA: sha256of(approvedExec)},
	}

	// Matching content → the write step is reached ONCE with the whole set, and each grant is staged.
	r := &fakeRenewer{}
	if err := RenewApprovedWrapperSet(context.Background(), r, DeliveringBranchContent(wt, "fix/x"), grantDir,
		"qst_w1", "operator", "2026-08-04T00:00:00Z", set); err != nil {
		t.Fatalf("a matching working-source approval should reach the write step, got %v", err)
	}
	if len(r.calls) != 1 || len(r.calls[0]) != 2 {
		t.Fatalf("the whole approval should reach the root write step in ONE call, got %+v", r.calls)
	}
	for _, name := range []string{"devlab-install", "devlab-exec"} {
		if _, err := os.Stat(filepath.Join(grantDir, "qst_w1."+name+".content")); err != nil {
			t.Fatalf("the approved content for %s should have been staged for the root tool: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(grantDir, "qst_w1.grant")); err != nil {
		t.Fatalf("the combined grant should have been staged: %v", err)
	}

	// ONE file's branch content changes AFTER the approval (a later commit rewrote devlab-install). The
	// re-read now differs from the approved checksum, so the WHOLE set is refused and nothing reaches root.
	writeRepoFile(t, wt, "deploy/devlab-install", []byte("#!/usr/bin/env bash\n# changed after approval\n"))
	r2 := &fakeRenewer{}
	err := RenewApprovedWrapperSet(context.Background(), r2, DeliveringBranchContent(wt, "fix/x"), grantDir,
		"qst_w2", "operator", "2026-08-04T00:00:00Z", set)
	if err == nil || !strings.Contains(err.Error(), "not the approved") {
		t.Fatalf("a source that changed after the approval must refuse the whole set, got %v", err)
	}
	if len(r2.calls) != 0 {
		t.Fatalf("nothing must reach the root write step when one file no longer matches, got %+v", r2.calls)
	}
}

// ── tiny git helpers (local repo; default branch is the standard branch) ──────────────────────
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
func gitInit(t *testing.T, dir string) {
	gitCmd(t, dir, "init", "-q", "-b", "main")
}
func writeRepoFile(t *testing.T, dir, rel string, b []byte) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
func gitCommitAll(t *testing.T, dir, msg string) {
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", msg)
}
