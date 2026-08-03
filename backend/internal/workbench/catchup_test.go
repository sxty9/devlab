package workbench

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// ancestorOf reports whether sha is an ancestor of (or equal to) ref — the branch-aware form of the
// package's commitReachable, which is pinned to the shared branch.
func ancestorOf(t *testing.T, wt, sha, ref string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", wt, "merge-base", "--is-ancestor", sha, ref)
	cmd.Env = gitEnv()
	return cmd.Run() == nil
}

// forkTaskBranch cuts a task branch from the current origin/main, commits one change on it, and
// returns the bench pointed at that branch plus the branch's head commit. The working tree is left
// on the task branch, exactly as Prepare would leave it before the implement stage.
func forkTaskBranch(t *testing.T, origin, wt string, b *Bench, file, content string) (*Bench, string) {
	t.Helper()
	gitT(t, wt, "fetch", "origin")
	gitT(t, wt, "checkout", "-b", "fix/task-aaa111", "origin/main")
	writeF(t, filepath.Join(wt, file), content)
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "task work")
	on, err := b.On("fix/task-aaa111")
	if err != nil {
		t.Fatalf("On(task): %v", err)
	}
	return on, gitOut(t, wt, "rev-parse", "HEAD")
}

// A branch cut from an older layer sits BEHIND the tip: StackPosition names the fork it still shares
// with the tip, the tip's commit, and how many commits landed since — measured, not guessed.
func TestStackPositionBehind(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	bt, taskHead := forkTaskBranch(t, origin, wt, b, "task.txt", "task work\n")
	advanceMain(t, origin, "landed.txt", "a delivery that landed\n") // the tip moves on
	gitT(t, wt, "fetch", "origin")

	tip, fork, behind, exists, err := bt.StackPosition(ctx, "main")
	if err != nil || !exists {
		t.Fatalf("StackPosition: exists=%v err=%v", exists, err)
	}
	if behind != 1 {
		t.Errorf("behind=%d, want 1 (one delivery landed since the branch forked)", behind)
	}
	wantTip := gitOut(t, wt, "rev-parse", "origin/main")
	if tip != wantTip {
		t.Errorf("tip=%s, want origin/main %s", tip, wantTip)
	}
	// The fork is the seed both still share — NOT the task head, and NOT the new tip.
	if fork == taskHead || fork == tip {
		t.Errorf("fork %s must be the shared root, not the branch head or the tip", fork)
	}
	if !ancestorOf(t, wt, fork, "refs/heads/fix/task-aaa111") {
		// fork is an ancestor of the task branch by construction.
		t.Errorf("fork %s is not reachable from the task branch", fork)
	}
}

// A branch already sitting on the tip is not behind — nothing to catch up (test case 1).
func TestStackPositionOnTip(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	bt, _ := forkTaskBranch(t, origin, wt, b, "task.txt", "task work\n") // no advance: still on the tip
	_, _, behind, exists, err := bt.StackPosition(ctx, "main")
	if err != nil || !exists {
		t.Fatalf("StackPosition: exists=%v err=%v", exists, err)
	}
	if behind != 0 {
		t.Errorf("behind=%d, want 0 — the branch already sits on the tip", behind)
	}
}

// A clean catch-up replays the branch's own work onto the current tip: the branch head moves, and
// BOTH the branch's own change and the delivery that landed since are present (test case 2).
func TestCatchUpOntoClean(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	bt, oldHead := forkTaskBranch(t, origin, wt, b, "task.txt", "task work\n")
	advanceMain(t, origin, "landed.txt", "landed\n")

	rep, err := bt.CatchUpOnto(ctx, "main")
	if err != nil {
		t.Fatalf("CatchUpOnto: %v", err)
	}
	if rep.Conflicted {
		t.Fatalf("a clean catch-up must not conflict: %+v", rep)
	}
	if !rep.Rebased || rep.NewHead == oldHead {
		t.Errorf("the branch must have been rebased onto the tip: %+v", rep)
	}
	if rep.OldHead != oldHead {
		t.Errorf("OldHead=%s, want %s", rep.OldHead, oldHead)
	}
	// The rebased branch now descends from the advanced tip AND still carries its own file.
	tip := gitOut(t, wt, "rev-parse", "origin/main")
	if !ancestorOf(t, wt, tip, "refs/heads/fix/task-aaa111") {
		t.Errorf("after catch-up the branch does not contain the tip %s", tip)
	}
	gitT(t, wt, "checkout", "fix/task-aaa111")
	if !exists(wt, "task.txt") || !exists(wt, "landed.txt") {
		t.Errorf("after catch-up both the branch's own work and the landed delivery must be present")
	}
}

// A branch that was PUBLISHED in an earlier run and then rebased must have its remote force-updated
// to the caught-up state — otherwise the ordinary fast-forward Publish would fold the old commits
// back in and silently undo the catch-up. After the catch-up the remote equals the branch, so a
// following Publish is a clean no-op.
func TestCatchUpOntoRepublishesRewrittenBranch(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	bt, _ := forkTaskBranch(t, origin, wt, b, "task.txt", "task work\n")
	// The branch was published in an earlier run: origin now carries its OLD history.
	if err := bt.Publish(ctx); err != nil {
		t.Fatalf("publish before catch-up: %v", err)
	}
	oldRemote := gitOut(t, wt, "rev-parse", "origin/fix/task-aaa111")

	advanceMain(t, origin, "landed.txt", "landed\n")
	rep, err := bt.CatchUpOnto(ctx, "main")
	if err != nil {
		t.Fatalf("CatchUpOnto: %v", err)
	}
	if !rep.Rebased {
		t.Fatalf("the branch must have been rebased: %+v", rep)
	}
	gitT(t, wt, "fetch", "origin")
	newRemote := gitOut(t, wt, "rev-parse", "origin/fix/task-aaa111")
	if newRemote == oldRemote {
		t.Errorf("the remote branch was not force-updated to the caught-up state (still %s)", oldRemote)
	}
	if newRemote != rep.NewHead {
		t.Errorf("the remote %s does not match the caught-up head %s", newRemote, rep.NewHead)
	}
	// The ordinary Publish is now a clean no-op — it does NOT fold the old commits back in.
	if err := bt.Publish(ctx); err != nil {
		t.Fatalf("Publish after catch-up must be a clean no-op: %v", err)
	}
	if !ancestorOf(t, wt, gitOut(t, wt, "rev-parse", "origin/main"), "refs/heads/fix/task-aaa111") {
		t.Errorf("Publish folded the branch off the tip — the catch-up was undone")
	}
}

// A catch-up that CONFLICTS is FULLY aborted: the branch is byte-for-byte as before (same head), the
// conflicting files are named, and no rebase state is left in the working tree (test case 3).
func TestCatchUpOntoConflictLeavesBranchUnchanged(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	// The branch edits README; the tip edits the SAME file differently → the replay cannot apply.
	bt, oldHead := forkTaskBranch(t, origin, wt, b, "README.md", "task version\n")
	advanceMain(t, origin, "README.md", "tip version\n")

	rep, err := bt.CatchUpOnto(ctx, "main")
	if err != nil {
		t.Fatalf("CatchUpOnto: %v", err)
	}
	if !rep.Conflicted {
		t.Fatalf("editing the same file as the tip must conflict: %+v", rep)
	}
	if rep.NewHead != oldHead {
		t.Errorf("a conflicted catch-up must leave the branch EXACTLY as before: head %s, want %s", rep.NewHead, oldHead)
	}
	if got := gitOut(t, wt, "rev-parse", "refs/heads/fix/task-aaa111"); got != oldHead {
		t.Errorf("the branch ref moved despite the conflict: %s, want %s", got, oldHead)
	}
	var named bool
	for _, f := range rep.ConflictFiles {
		if f == "README.md" {
			named = true
		}
	}
	if !named {
		t.Errorf("the conflict must name README.md, got %v", rep.ConflictFiles)
	}
	// No rebase is left in progress: a fresh rebase --abort would fail because there is nothing to abort.
	if _, err := bt.gitUser(ctx, "rebase", "--abort"); err == nil {
		t.Errorf("a rebase was left in progress after the conflict — the abort was not complete")
	}
}

// Reaches is the read-only "did this delivery land in that layer?" probe the intervening-deliveries
// join rides on: the landed commit is reachable from the tip but not from the fork.
func TestReaches(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	bt, _ := forkTaskBranch(t, origin, wt, b, "task.txt", "task\n")
	advanceMain(t, origin, "landed.txt", "landed\n")
	gitT(t, wt, "fetch", "origin")

	_, fork, _, _, err := bt.StackPosition(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	tip := gitOut(t, wt, "rev-parse", "origin/main")
	landed := tip // the advance is the tip commit here

	if in, err := bt.Reaches(ctx, landed, tip); err != nil || !in {
		t.Errorf("the landed commit must be reachable from the tip: in=%v err=%v", in, err)
	}
	if in, err := bt.Reaches(ctx, landed, fork); err != nil || in {
		t.Errorf("the landed commit must NOT be reachable from the fork: in=%v err=%v", in, err)
	}
}
