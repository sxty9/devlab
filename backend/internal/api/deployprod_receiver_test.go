package api

// The measurement half of Part 3, at the real DeployProd seam: a devlab (self) production delivery
// whose merged content changes the ROOT RECEIVER SCRIPTS proves — from the receiver's OWN self-report
// in the install-trigger output — whether the production host actually carries them. A host still on the
// old scripts (or a silent, older receiver) yields a ReceiverStale outcome that does NOT settle live; a
// host carrying exactly the merged scripts settles live. This closes the loop the deliver-package test
// drives from a scripted outcome: here the outcome is DERIVED from a real merged checkout and a real
// self-report, so the check cannot silently pass a host that never received the fix.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"devlab/backend/internal/deploy"
	"devlab/backend/internal/github"
	"devlab/backend/internal/statepath"
	"devlab/backend/internal/workspace"
)

// seedSelfWorkspaceWithReceiver lays a real clone of the SELF repo whose merged default branch carries
// the two root receiver scripts and a conforming ./service (so Detect classifies it a service). Returns
// the working tree and the sha256 of each committed receiver script — the checksum a verbatim install
// would have, so a matching self-report proves the host current.
func seedSelfWorkspaceWithReceiver(t *testing.T, wsRoot, user, short string) (wt, recvSHA, libSHA string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitAt(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")

	src := filepath.Join(root, "src")
	gitAt(t, root, "init", "-q", "-b", "main", "src")
	recvBytes := []byte("#!/usr/bin/env bash\n# receiver v2 with the edge-address fix\n")
	libBytes := []byte("#!/usr/bin/env bash\n# setup library v2\n")
	writeFileP(t, filepath.Join(src, "deploy", "devlab-deploy-recv"), recvBytes, 0o755)
	writeFileP(t, filepath.Join(src, "deploy", "devlab-setup-lib.sh"), libBytes, 0o644)
	writeFileP(t, filepath.Join(src, "service"), []byte("#!/bin/sh\n"), 0o755)
	gitAt(t, src, "add", "-A")
	gitAt(t, src, "commit", "-qm", "merged state with receiver v2")
	gitAt(t, src, "remote", "add", "origin", remote)
	gitAt(t, src, "push", "-q", "origin", "main")

	userDir := filepath.Join(wsRoot, user)
	if err := exec.Command("mkdir", "-p", userDir).Run(); err != nil {
		t.Fatal(err)
	}
	wt = filepath.Join(userDir, short)
	gitAt(t, userDir, "clone", "-q", remote, short)
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	return wt, sum(recvBytes), sum(libBytes)
}

func writeFileP(t *testing.T, path string, b []byte, mode uint32) {
	t.Helper()
	if err := exec.Command("mkdir", "-p", filepath.Dir(path)).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, os.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
}

// selfDeployDeps builds the ChainDeps the whole-path test uses, with the receiver self-report the
// (placeholdered) send returns as the ONLY variable — the value under test.
func selfDeployDeps(t *testing.T, s *Server, user string, report string) *ChainDeps {
	t.Helper()
	return &ChainDeps{
		s: s, user: user, benches: map[string]*repoBench{}, full: map[string]string{},
		execBypass: true,
		prodBuild: func(_ context.Context, _ workspace.Executor, wt string) (string, error) {
			art := filepath.Join(wt, deploy.ArtifactDirName)
			return art, exec.Command("mkdir", "-p", art).Run()
		},
		prodEmit: func(_ context.Context, _ workspace.Executor, _, _ string, _ deploy.Detection) error { return nil },
		// The send is placeholdered, but it returns exactly what a real receiver's install trigger would:
		// its RECV-SELF-SHA self-report. That report is the host state the drift check measures against.
		prodSend: func(_ context.Context, _ deploy.ProdConfig, _, _ string) (string, error) {
			return report, nil
		},
	}
}

func armProd(t *testing.T) {
	t.Helper()
	t.Setenv("DEVLAB_RUNS_PROD_TARGET", "deploy@host:/srv/stage")
	t.Setenv("DEVLAB_RUNS_PROD_RECV", "deploy@prod.example")
	t.Setenv("DEVLAB_RUNS_PROD_KEY", filepath.Join(t.TempDir(), "key"))
}

// A self delivery whose receiver scripts the host has NOT received (the receiver reports an old
// checksum) yields ReceiverStale — Running false — with the drifted script named and the merged
// checksum pinned.
func TestDeployProd_SelfReceiverStale_NotLive(t *testing.T) {
	s, wsRoot := prodNameServer(t)
	s.paths = &statepath.Paths{Root: t.TempDir()}
	const user, full, short = "runner", "acme/devlab", "devlab"
	t.Setenv("DEVLAB_SELF_REPO", short)
	wt, recvSHA, libSHA := seedSelfWorkspaceWithReceiver(t, wsRoot, user, short)
	_ = wt

	restore := github.SetAPIBase(fixtureGitHub(t))
	defer restore()
	armProd(t)

	// The host carries the OLD receiver but the current library.
	report := "install-only prod deploy of 'devlab'\n" +
		"RECV-SELF-SHA devlab-deploy-recv 0000oldsha0000\n" +
		"RECV-SELF-SHA devlab-setup-lib.sh " + libSHA + "\n" +
		"prod deploy of 'devlab' done\n"
	deps := selfDeployDeps(t, s, user, report)

	out, err := chainDeploy{d: deps}.DeployProd(context.Background(), full)
	if err == nil {
		t.Fatal("a stale production receiver is a failure and must surface an error")
	}
	if out.Running {
		t.Fatal("a delivery whose receiver scripts never reached the host must NOT report running/live")
	}
	if !out.ReceiverStale {
		t.Fatalf("the outcome must be marked receiver-stale, got %+v", out)
	}
	if len(out.ReceiverGrants) != 1 || out.ReceiverGrants[0].Name != "devlab-deploy-recv" {
		t.Fatalf("the drift must name exactly the receiver script whose checksum differs, got %v", out.ReceiverGrants)
	}
	if out.ReceiverGrants[0].SHA != recvSHA {
		t.Fatalf("the grant must pin the MERGED checksum %s, got %s", recvSHA, out.ReceiverGrants[0].SHA)
	}
	if out.ReceiverTarget == "" {
		t.Fatal("the outcome must name the production host so the approval can name it")
	}
}

// A self delivery whose receiver scripts the host DOES carry (the receiver reports the merged
// checksums) settles running/live.
func TestDeployProd_SelfReceiverCurrent_Live(t *testing.T) {
	s, wsRoot := prodNameServer(t)
	s.paths = &statepath.Paths{Root: t.TempDir()}
	const user, full, short = "runner", "acme/devlab", "devlab"
	t.Setenv("DEVLAB_SELF_REPO", short)
	_, recvSHA, libSHA := seedSelfWorkspaceWithReceiver(t, wsRoot, user, short)

	restore := github.SetAPIBase(fixtureGitHub(t))
	defer restore()
	armProd(t)

	report := "RECV-SELF-SHA devlab-deploy-recv " + recvSHA + "\n" +
		"RECV-SELF-SHA devlab-setup-lib.sh " + libSHA + "\n"
	deps := selfDeployDeps(t, s, user, report)

	out, err := chainDeploy{d: deps}.DeployProd(context.Background(), full)
	if err != nil {
		t.Fatalf("a host carrying exactly the merged receiver scripts must settle live: %v", err)
	}
	if !out.Running || out.ReceiverStale {
		t.Fatalf("the delivery must be running and NOT receiver-stale, got %+v", out)
	}
}

// A FOREIGN service delivery never carries the receiver scripts, so its send is never held on a
// receiver check even when the host reports nothing at all.
func TestDeployProd_ForeignRepo_NoReceiverCheck(t *testing.T) {
	s, wsRoot := prodNameServer(t)
	s.paths = &statepath.Paths{Root: t.TempDir()}
	const user, full, short = "runner", "acme/svc", "svc"
	t.Setenv("DEVLAB_SELF_REPO", "devlab")
	seedRealWorkspace(t, wsRoot, user, short)

	restore := github.SetAPIBase(fixtureGitHub(t))
	defer restore()
	armProd(t)

	deps := selfDeployDeps(t, s, user, "" /* the receiver reports nothing */)
	out, err := chainDeploy{d: deps}.DeployProd(context.Background(), full)
	if err != nil {
		t.Fatalf("a foreign service delivery must not be held on a receiver check: %v", err)
	}
	if !out.Running || out.ReceiverStale {
		t.Fatalf("a foreign delivery must settle running with no receiver check, got %+v", out)
	}
}
