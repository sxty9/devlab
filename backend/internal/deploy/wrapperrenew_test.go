package deploy

// The PROOF for the root-wrapper write half (task point 7). The bash tests drive the root tool
// directly (no sudo) through the env seams the wrapper already exposes, exactly like wrapper_test.go:
//   (a) an approval for content A installs nothing when handed content B,
//   (c) an approval already used installs nothing a second time,
//   (d) a name outside the four is refused,
//   (e) after an install, who approved and what was there before is readable, and the previous
//       content is kept for a rollback.
// (b) — only merged content is a source — is proven at the Go level (TestMainWrapperDriftReadsStandardBranch):
// the offered checksum is the STANDARD BRANCH's, never the working tree's, so content that is not on
// the standard branch is never what the approval (and thus the install) is pinned to.

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

// writeGrant stages a content file and a matching grant, returning their paths. sha lets a test pin a
// checksum that deliberately disagrees with the content (test a).
func (f renewFixture) writeGrant(t *testing.T, id, name string, content []byte, sha, by string) (contentPath, grantPath string) {
	t.Helper()
	contentPath = filepath.Join(f.grantsDir, id+"."+name+".content")
	grantPath = filepath.Join(f.grantsDir, id+"."+name+".grant")
	if err := os.WriteFile(contentPath, content, 0o640); err != nil {
		t.Fatal(err)
	}
	grant := strings.Join([]string{
		"name=" + name, "sha256=" + sha, "approvalId=" + id,
		"approvedBy=" + by, "approvedAt=2026-08-02T12:00:00Z", "",
	}, "\n")
	if err := os.WriteFile(grantPath, []byte(grant), 0o640); err != nil {
		t.Fatal(err)
	}
	return contentPath, grantPath
}

func (f renewFixture) renew(t *testing.T, name, contentPath, grantPath string) wrapperResult {
	return runWrapper(t, "deploy/devlab-install", f.env, "--renew-wrapper", name, contentPath, grantPath)
}

// (a) A confirmation for content A installs content B NOT.
func TestRenewRefusesContentThatMismatchesTheApprovedChecksum(t *testing.T) {
	f := newRenewFixture(t)
	approvedSHA := sha256of([]byte("APPROVED CONTENT A\n"))
	// The staged content is B, but the grant carries the checksum of A.
	c, g := f.writeGrant(t, "qst_a1", "devlab-exec", []byte("DIFFERENT CONTENT B\n"), approvedSHA, "operator")
	res := f.renew(t, "devlab-exec", c, g)
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
	sha := sha256of(content)
	c, g := f.writeGrant(t, "qst_c1", "devlab-exec", content, sha, "operator")

	if res := f.renew(t, "devlab-exec", c, g); res.exit != 0 {
		t.Fatalf("first renewal should succeed, got exit %d\n%s", res.exit, res.out)
	}
	if got, _ := os.ReadFile(filepath.Join(f.sbinDir, "devlab-exec")); string(got) != string(content) {
		t.Fatalf("first renewal did not install the approved content")
	}
	// The same grant/approval a second time installs nothing.
	res := f.renew(t, "devlab-exec", c, g)
	if res.exit == 0 || !strings.Contains(res.out, "already used") {
		t.Fatalf("expected a single-use refusal, got exit %d\n%s", res.exit, res.out)
	}
}

// (d) A path/name outside the renewable list is refused.
func TestRenewRefusesUnknownName(t *testing.T) {
	f := newRenewFixture(t)
	content := []byte("malicious\n")
	c, g := f.writeGrant(t, "qst_d1", "evil-tool", content, sha256of(content), "operator")
	res := f.renew(t, "evil-tool", c, g)
	if res.exit == 0 || !strings.Contains(res.out, "not a renewable root wrapper") {
		t.Fatalf("expected refusal of an unknown wrapper name, got exit %d\n%s", res.exit, res.out)
	}
	// And a real wrapper name whose content is fine still cannot be installed from a run-writable
	// place: a source under the workspaces subtree is refused (defense for task point 1).
	ws := filepath.Join(f.stateDir, "workspaces", "runner")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	wc := filepath.Join(ws, "x.content")
	if err := os.WriteFile(wc, content, 0o644); err != nil {
		t.Fatal(err)
	}
	res2 := f.renew(t, "devlab-exec", wc, g)
	if res2.exit == 0 || !strings.Contains(res2.out, "workspaces") {
		t.Fatalf("expected refusal of a run-writable source, got exit %d\n%s", res2.exit, res2.out)
	}
}

// Defect #1 closed, measured end-to-end: the sourced setup library is now a RENEWABLE name, and the
// root tool installs it READ-ONLY (0644), not executable. Before this change the daemon could guard
// devlab-setup-lib.sh and offer it for approval, but this tool refused the very name the human
// approved. Here an approval for it installs it, at mode 0644.
func TestRenewInstallsSetupLibraryReadOnly(t *testing.T) {
	f := newRenewFixture(t)
	content := []byte("# shellcheck shell=bash\n# merged devlab-setup-lib.sh\n")
	sha := sha256of(content)
	c, g := f.writeGrant(t, "qst_lib1", "devlab-setup-lib.sh", content, sha, "operator")

	if res := f.renew(t, "devlab-setup-lib.sh", c, g); res.exit != 0 {
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
	c, g := f.writeGrant(t, "qst_e1", "devlab-install", newContent, newSHA, "alice")

	if res := f.renew(t, "devlab-install", c, g); res.exit != 0 {
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

// (b) Only merged content is a source: MainWrapperDrift offers the STANDARD-BRANCH checksum, never
// the working tree's. A wrapper changed only in the working tree (not committed to the default
// branch) is never what the renewal is pinned to.
func TestMainWrapperDriftReadsStandardBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	wt := t.TempDir()
	gitInit(t, wt)

	merged := []byte("#!/usr/bin/env bash\n# MERGED devlab-exec\n")
	writeRepoFile(t, wt, "deploy/devlab-exec", merged)
	gitCommitAll(t, wt, "add merged wrapper")

	// The working tree now diverges from the committed (merged) content — this is what a run could do.
	writeRepoFile(t, wt, "deploy/devlab-exec", []byte("#!/usr/bin/env bash\n# WORKING-TREE tampering\n"))

	// The installed copy differs from both, so a drift exists.
	sbin := filepath.Join(t.TempDir(), "sbin")
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbin, "devlab-exec"), []byte("stale installed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_SBIN_DIR", sbin)
	restore := wrapperInstallDir
	wrapperInstallDir = sbin
	t.Cleanup(func() { wrapperInstallDir = restore })

	drifts, err := MainWrapperDrift(wt)
	if err != nil {
		t.Fatal(err)
	}
	var got *WrapperDrift
	for i := range drifts {
		if drifts[i].Name == "devlab-exec" {
			got = &drifts[i]
		}
	}
	if got == nil {
		t.Fatalf("devlab-exec drift not reported, got %+v", drifts)
	}
	if got.WantSHA != sha256of(merged) {
		t.Fatalf("the offered content must be the MERGED (standard-branch) content, not the working tree's; got %s want %s", got.WantSHA, sha256of(merged))
	}
	if string(got.WantContent) != string(merged) {
		t.Fatalf("WantContent must be the merged content byte-for-byte")
	}
}

// WorkingWrapperDrift offers THIS run's own working-branch content — the second renewal source, for
// when the run itself changed a root script that is not yet merged. It reports the drift against the
// installed copy and pins the offered checksum to the working-tree copy (not the standard branch).
func TestWorkingWrapperDriftReadsWorkingBranch(t *testing.T) {
	wt := t.TempDir()

	// The run changed devlab-exec on its working branch (only the working tree matters here — no commit
	// or standard branch is needed, which is exactly the case MainWrapperDrift cannot serve).
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

	drifts, err := WorkingWrapperDrift(wt)
	if err != nil {
		t.Fatal(err)
	}
	var got *WrapperDrift
	for i := range drifts {
		if drifts[i].Name == "devlab-exec" {
			got = &drifts[i]
		}
	}
	if got == nil {
		t.Fatalf("devlab-exec working-branch drift not reported, got %+v", drifts)
	}
	if got.WantSHA != sha256of(working) || string(got.WantContent) != string(working) {
		t.Fatalf("the offered content must be THIS run's working-tree content; got sha %s want %s", got.WantSHA, sha256of(working))
	}
	// The drift carries a SHORT change summary (task point 4) — a human reads it, not the full content.
	if !strings.Contains(got.Summary, "lines") {
		t.Fatalf("the drift must summarize the change for the question, got %q", got.Summary)
	}

	// When the installed copy already matches the working branch, there is no drift to renew.
	if err := os.WriteFile(filepath.Join(sbin, "devlab-exec"), working, 0o755); err != nil {
		t.Fatal(err)
	}
	drifts, err = WorkingWrapperDrift(wt)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range drifts {
		if d.Name == "devlab-exec" {
			t.Fatalf("no drift expected once the installed copy matches the working branch, got %+v", d)
		}
	}
}

// fakeRenewer records whether the root write step was reached at all — a refusal before the write
// (source no longer matches the approval) must never call it.
type fakeRenewer struct{ calls []string }

func (f *fakeRenewer) Renew(_ context.Context, name, _, _ string) error {
	f.calls = append(f.calls, name)
	return nil
}

// A working-source approval with a matching checksum reaches the root write step and stages the run's
// own content; but if the run's branch CHANGES after the approval, the re-read no longer hashes to the
// approved checksum and RenewApprovedWrapper refuses BEFORE the write — nothing is installed (task
// point 3, the working-source half of "the branch changed after approval installs nothing").
func TestRenewApprovedWrapperBindsWorkingSourceToTheApprovedChecksum(t *testing.T) {
	wt := t.TempDir()
	grantDir := filepath.Join(t.TempDir(), "grants")

	approved := []byte("#!/usr/bin/env bash\n# approved run content\n")
	writeRepoFile(t, wt, "deploy/devlab-install", approved)
	approvedSHA := sha256of(approved)

	// Matching content → the write step is reached and the grant is staged.
	r := &fakeRenewer{}
	if err := RenewApprovedWrapper(context.Background(), r, WorkingWrapperContent(wt), grantDir,
		"devlab-install", approvedSHA, "qst_w1", "operator", "2026-08-04T00:00:00Z"); err != nil {
		t.Fatalf("a matching working-source approval should reach the write step, got %v", err)
	}
	if len(r.calls) != 1 || r.calls[0] != "devlab-install" {
		t.Fatalf("the root write step should have been called once, got %+v", r.calls)
	}
	if _, err := os.Stat(filepath.Join(grantDir, "qst_w1.devlab-install.content")); err != nil {
		t.Fatalf("the approved content should have been staged for the root tool: %v", err)
	}

	// The run's branch changes AFTER the approval (a later commit rewrote the script). The re-read now
	// differs from the approved checksum, so the renewal refuses and never reaches the write step.
	writeRepoFile(t, wt, "deploy/devlab-install", []byte("#!/usr/bin/env bash\n# changed after approval\n"))
	r2 := &fakeRenewer{}
	err := RenewApprovedWrapper(context.Background(), r2, WorkingWrapperContent(wt), grantDir,
		"devlab-install", approvedSHA, "qst_w2", "operator", "2026-08-04T00:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "not the approved") {
		t.Fatalf("a source that changed after the approval must be refused, got %v", err)
	}
	if len(r2.calls) != 0 {
		t.Fatalf("nothing must reach the root write step when the source no longer matches, got %+v", r2.calls)
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
