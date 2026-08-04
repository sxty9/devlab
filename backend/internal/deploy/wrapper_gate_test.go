package deploy

// The THREE measured cases the gate/probe must hold (task points 1-3). The wrapper gate and the
// renewal probe now read the tree that is DELIVERED — the delivering branch's committed ref for the
// gate, the stack tip for the probe — not the shared working tree (which sits on the standard branch)
// and not main alone. Each test sets the working tree deliberately on the WRONG branch to prove the
// fix reads a ref, not the tree:
//
//	(a) an open delivery changed a root script and the installed copy IS that content — no drift, no
//	    question (the false positive that offered a rollback to the older main content on 2026-08-04);
//	(b) the installed copy is main's and the STACK TIP changed the script — drift, and the offered
//	    content is the TIP's, not main's;
//	(c) the DELIVERING branch changed a root script while the working tree and the installed copy are
//	    both main — the gate halts (today it let this through: the regression test).

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gateRepo is a real git checkout with a default branch (main) and helpers to add branches whose
// content differs, plus a fake sbin dir the drift probe compares against.
type gateRepo struct {
	wt   string
	sbin string
}

func newGateRepo(t *testing.T, mainInstall []byte) gateRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	wt := t.TempDir()
	gitInit(t, wt)
	writeRepoFile(t, wt, "deploy/devlab-install", mainInstall)
	gitCommitAll(t, wt, "main: devlab-install")

	sbin := filepath.Join(t.TempDir(), "sbin")
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	restore := wrapperInstallDir
	wrapperInstallDir = sbin
	t.Cleanup(func() { wrapperInstallDir = restore })
	return gateRepo{wt: wt, sbin: sbin}
}

// branchWith commits content on a NEW branch and returns to main, so the working tree stays on main
// while <branch> carries the change — the exact split the gate must see through.
func (g gateRepo) branchWith(t *testing.T, branch string, content []byte) {
	t.Helper()
	gitCmd(t, g.wt, "checkout", "-q", "-b", branch)
	writeRepoFile(t, g.wt, "deploy/devlab-install", content)
	gitCommitAll(t, g.wt, branch+": devlab-install")
	gitCmd(t, g.wt, "checkout", "-q", "main")
}

// install writes the installed copy the drift probe compares against.
func (g gateRepo) install(t *testing.T, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(g.sbin, "devlab-install"), content, 0o755); err != nil {
		t.Fatal(err)
	}
}

// (a) An open delivery changed devlab-install and the installed copy IS that content. The delivering
// branch carries the open delivery's change, so the gate — measuring the delivering branch — sees NO
// drift and raises no question. Reading main (the old probe) would have reported drift and offered to
// roll the installed script BACK to the older main content.
func TestGate_OpenDeliveryContentInstalled_NoDrift(t *testing.T) {
	v1 := []byte("#!/usr/bin/env bash\n# devlab-install v1 (main)\n")
	v2 := []byte("#!/usr/bin/env bash\n# devlab-install v2 (open delivery)\n")
	g := newGateRepo(t, v1)
	g.branchWith(t, "fix/prior-delivery", v2) // the open delivery / stack tip carries v2
	g.install(t, v2)                          // installed is exactly the open delivery's content

	// The gate measures the delivering branch, which stands on the open delivery and carries v2.
	if err := GuardWrappersCurrent(g.wt, "fix/prior-delivery"); err != nil {
		t.Fatalf("no drift expected when the installed copy matches the delivering branch, got: %v", err)
	}
	// The probe against the stack tip agrees: installed == tip ⇒ nothing to renew (no rollback offer).
	drifts, err := StackTipWrapperDrift(g.wt, "fix/prior-delivery")
	if err != nil {
		t.Fatal(err)
	}
	if d := driftNamed(drifts, "devlab-install"); d != nil {
		t.Fatalf("the stack-tip probe must not offer a rollback when installed matches the tip, got %+v", d)
	}
}

// (b) The installed copy is main's, and the STACK TIP changed the script. The probe reports drift and
// offers the TIP's content (never main's).
func TestGate_StackTipChanged_OffersTipContent(t *testing.T) {
	v1 := []byte("#!/usr/bin/env bash\n# devlab-install v1 (main == installed)\n")
	tip := []byte("#!/usr/bin/env bash\n# devlab-install v2 (stack tip)\n")
	g := newGateRepo(t, v1)
	g.branchWith(t, "fix/prior-delivery", tip)
	g.install(t, v1) // installed still matches main

	drifts, err := StackTipWrapperDrift(g.wt, "fix/prior-delivery")
	if err != nil {
		t.Fatal(err)
	}
	got := driftNamed(drifts, "devlab-install")
	if got == nil {
		t.Fatalf("a stack-tip wrapper change must be reported as drift, got %+v", drifts)
	}
	if got.WantSHA != sha256of(tip) || string(got.WantContent) != string(tip) {
		t.Fatalf("the offered content must be the STACK TIP's, not main's; got sha %s want %s", got.WantSHA, sha256of(tip))
	}
}

// (c) THE REGRESSION TEST. The delivering branch changed devlab-install; the working tree AND the
// installed copy are both main. The old gate read the working tree (main) and let this through — a
// half-shipped daemon over a stale root script. The gate now reads the delivering branch's ref, so it
// SEES the change and halts with the named refusal.
func TestGate_DeliveringBranchChangesRootScript_Halts(t *testing.T) {
	v1 := []byte("#!/usr/bin/env bash\n# devlab-install v1 (main == workbench == installed)\n")
	v2 := []byte("#!/usr/bin/env bash\n# devlab-install v2 (this run's change)\n")
	g := newGateRepo(t, v1)
	g.branchWith(t, "fix/this-run", v2) // the run changed the script on its own branch …
	g.install(t, v1)                    // … while the installed copy is still main's

	// Sanity: the working tree is on main (v1), so a gate that read the tree would see no drift.
	onDisk, _ := os.ReadFile(filepath.Join(g.wt, "deploy", "devlab-install"))
	if string(onDisk) != string(v1) {
		t.Fatalf("precondition: the working tree must sit on main (v1), got %q", onDisk)
	}

	err := GuardWrappersCurrent(g.wt, "fix/this-run")
	if err == nil {
		t.Fatal("the gate must halt when the delivering branch changed a root script (today it let this through)")
	}
	if !errors.Is(err, ErrWrappersStale) {
		t.Fatalf("the refusal must match ErrWrappersStale, got: %v", err)
	}
	if !strings.Contains(err.Error(), "devlab-install") {
		t.Fatalf("the refusal must name the stale wrapper devlab-install: %v", err)
	}
}
