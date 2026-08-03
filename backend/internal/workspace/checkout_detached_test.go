package workspace

// The production build must be pinned to the MERGED state — a remote-tracking ref (origin/<default>),
// not a local branch. This test proves the two checkout tools differ on exactly that value: the plain
// Checkout REFUSES a remote-tracking ref (the 02.08.2026 production standstill: "a branch is expected,
// got remote branch origin/main"), while CheckoutDetached takes it and parks HEAD on the merged commit.
// It runs against a REAL local repository with a REAL origin, so it fails the moment CheckoutDetached
// is reduced back to Checkout — a test that stays green without the fix would prove nothing.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// realCheckout builds a working tree whose origin/main points at a committed merged state, and
// returns the working-tree path and the merged commit SHA. It mirrors what the runner's workspace
// looks like when the production send reaches it: a clone with an origin remote and a fetched
// origin/main, the working tree parked on some branch other than that ref.
func realCheckout(t *testing.T) (wt, mergedSHA string) {
	t.Helper()
	root := t.TempDir()
	remote := root + "/remote.git"
	gitCmd(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")

	src := root + "/src"
	gitCmd(t, root, "init", "-q", "-b", "main", "src")
	if err := exec.Command("sh", "-c", "printf '#!/bin/sh\\n' > "+src+"/service").Run(); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "add", "-A")
	gitCmd(t, src, "commit", "-qm", "merged state")
	gitCmd(t, src, "remote", "add", "origin", remote)
	gitCmd(t, src, "push", "-q", "origin", "main")
	sha := gitCmd(t, src, "rev-parse", "HEAD")

	wt = root + "/wt"
	gitCmd(t, root, "clone", "-q", remote, "wt")
	// Park the working tree off origin/main, exactly as the runner's checkout is when the send starts.
	gitCmd(t, wt, "switch", "-c", "local-work")
	return wt, sha
}

// TestCheckoutDetached_TakesMergedRefWhereCheckoutRefuses is the nachweis for the checkout fix: the
// remote-tracking ref origin/main is refused by Checkout and accepted by CheckoutDetached, which
// leaves HEAD detached on the merged commit.
func TestCheckoutDetached_TakesMergedRefWhereCheckoutRefuses(t *testing.T) {
	ctx := context.Background()
	ex := Executor{User: "", PerUser: false}
	wt, mergedSHA := realCheckout(t)

	// The wrong tool: Checkout demands a LOCAL branch and rejects the merged state's remote-tracking
	// ref outright. This is precisely the failure that stalled the production send.
	if err := ex.Checkout(ctx, wt, "origin/main"); err == nil {
		t.Fatal("Checkout must refuse a remote-tracking ref — it only accepts a local branch")
	} else if !strings.Contains(err.Error(), "a branch is expected") {
		t.Fatalf("Checkout should fail with git's own remote-branch message, got: %v", err)
	}

	// The right tool: CheckoutDetached takes the merged state and pins HEAD to its commit, nameless.
	if err := ex.CheckoutDetached(ctx, wt, "origin/main"); err != nil {
		t.Fatalf("CheckoutDetached must take the merged state's remote-tracking ref: %v", err)
	}
	if got := gitCmd(t, wt, "rev-parse", "HEAD"); got != mergedSHA {
		t.Fatalf("HEAD after the detached checkout = %q, want the merged commit %q", got, mergedSHA)
	}
	if branch := gitCmd(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); branch != "HEAD" {
		t.Fatalf("the build state must be DETACHED (no local branch), got branch %q", branch)
	}
}
