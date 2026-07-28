package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// devTestRepo builds a bare origin with a seeded main and a workspace clone, returning both paths and a
// hermetic Executor. It also neutralises any global/system git config so a developer's ~/.gitconfig
// (signing, merge defaults, identity) can never perturb the assertions.
func devTestRepo(t *testing.T) (origin, wt string, e Executor) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	gitT(t, "", "init", "--bare", "-b", "main", origin)

	seed := filepath.Join(root, "seed")
	gitT(t, "", "clone", origin, seed)
	writeF(t, filepath.Join(seed, "README.md"), "v1\n")
	gitT(t, seed, "add", "-A")
	gitCommit(t, seed, "seed")
	gitT(t, seed, "push", "origin", "main")

	wt = filepath.Join(root, "work")
	gitT(t, "", "clone", origin, wt)
	return origin, wt, Executor{PerUser: false}
}

// pushSeedMain advances origin/main from the seed clone with a new file (an external change the dev
// state must be able to fold in).
func advanceMain(t *testing.T, origin, name, content string) {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "advance")
	gitT(t, "", "clone", origin, seed)
	writeF(t, filepath.Join(seed, name), content)
	gitT(t, seed, "add", "-A")
	gitCommit(t, seed, "advance main: "+name)
	gitT(t, seed, "push", "origin", "main")
}

func exists(wt, rel string) bool {
	_, err := os.Stat(filepath.Join(wt, rel))
	return err == nil
}

// TestDevBranchGrows is req 1 + 7a/7b: the work of a previous run is present in the state of the next.
// Run 1 creates mercury-dev from main, adds a file, and publishes it. A later run — even from a wiped
// workspace — must sit on THAT accumulated state (the file present), NEVER be reset back to main.
func TestDevBranchGrows(t *testing.T) {
	origin, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"

	// Run 1: establish mercury-dev from main (first time → created), do work, publish mercury-dev.
	prep, err := e.EnsureDevBranch(ctx, wt, "", "main", dev)
	if err != nil {
		t.Fatalf("EnsureDevBranch run1: %v", err)
	}
	if !prep.Created {
		t.Errorf("first EnsureDevBranch must report Created=true")
	}
	if err := e.CleanWorktree(ctx, wt); err != nil {
		t.Fatalf("CleanWorktree: %v", err)
	}
	writeF(t, filepath.Join(wt, "run1.txt"), "built by run 1\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "run1 work")
	if _, err := e.PushRefs(ctx, wt, "", false, dev); err != nil {
		t.Fatalf("push dev run1: %v", err)
	}

	// A brand-new workspace clone (simulating a wiped/recreated workspace) must recover the grown state.
	wt2 := filepath.Join(t.TempDir(), "work2")
	gitT(t, "", "clone", origin, wt2)
	prep, err = e.EnsureDevBranch(ctx, wt2, "", "main", dev)
	if err != nil {
		t.Fatalf("EnsureDevBranch run2: %v", err)
	}
	if prep.Created {
		t.Errorf("second EnsureDevBranch must report Created=false (mercury-dev already exists)")
	}
	if !exists(wt2, "run1.txt") {
		t.Errorf("run1's work missing after preparing the next run — the state was reset, not grown")
	}
	if branch := e.CurrentBranch(ctx, wt2); branch != dev {
		t.Errorf("workspace on %q, want %q", branch, dev)
	}
}

// TestDevStateAccumulatesAcrossRuns is req 7b: the dev state (what a dev-deploy ships — the mercury-dev
// tip) only ever GROWS across runs; a later run never removes an earlier run's work. Two runs each add a
// file; a fresh checkout of mercury-dev then carries BOTH.
func TestDevStateAccumulatesAcrossRuns(t *testing.T) {
	origin, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"

	run := func(work, file string) {
		if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
			t.Fatalf("prepare %s: %v", work, err)
		}
		if err := e.CleanWorktree(ctx, wt); err != nil {
			t.Fatalf("clean %s: %v", work, err)
		}
		if err := e.FoldInBranch(ctx, wt, "main"); err != nil {
			t.Fatalf("fold %s: %v", work, err)
		}
		writeF(t, filepath.Join(wt, file), work+"\n")
		gitT(t, wt, "add", "-A")
		gitCommit(t, wt, work)
		if _, err := e.PushRefs(ctx, wt, "", false, dev); err != nil {
			t.Fatalf("push %s: %v", work, err)
		}
	}
	run("run 1", "one.txt")
	run("run 2", "two.txt") // a second run on the SAME persistent branch

	// What a dev-deploy would ship — the published mercury-dev tip — carries BOTH runs' work.
	shipped := filepath.Join(t.TempDir(), "shipped")
	gitT(t, "", "clone", "-b", dev, origin, shipped)
	if !exists(shipped, "one.txt") {
		t.Errorf("run 1's work vanished from the dev state — a later run removed it")
	}
	if !exists(shipped, "two.txt") {
		t.Errorf("run 2's work missing from the dev state")
	}
}

// TestCleanWorktreeKeepsHistory is req 4: hygiene removes an aborted run's half-changes (uncommitted
// tracked edits + untracked files) WITHOUT touching history — the accumulated commits and the branch
// pointer are untouched.
func TestCleanWorktreeKeepsHistory(t *testing.T) {
	_, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "committed.txt"), "keep me\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "accumulated work")
	before, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))

	// An aborted run's leftovers: an uncommitted edit to a tracked file and an untracked file.
	writeF(t, filepath.Join(wt, "committed.txt"), "half-edited\n")
	writeF(t, filepath.Join(wt, "stray.txt"), "orphan\n")

	if err := e.CleanWorktree(ctx, wt); err != nil {
		t.Fatalf("CleanWorktree: %v", err)
	}
	if got := readF(t, filepath.Join(wt, "committed.txt")); got != "keep me\n" {
		t.Errorf("tracked edit not reverted: got %q", got)
	}
	if exists(wt, "stray.txt") {
		t.Errorf("untracked stray.txt survived hygiene")
	}
	after, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	if before != after {
		t.Errorf("HEAD moved (%s → %s) — hygiene must not touch history", before, after)
	}
	if out, _ := runGit(e.gitIn(ctx, wt, "", "status", "--porcelain")); out != "" {
		t.Errorf("worktree not clean: %q", out)
	}
}

// TestFoldInBranch is req 1: the default branch is folded INTO the dev state. An external change on main
// must appear on the dev branch after the fold, while the dev branch's own work is retained. An
// already-contained default branch is a clean no-op.
func TestFoldInBranch(t *testing.T) {
	origin, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "dev-only.txt"), "dev work\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "dev work")

	// No divergence yet → folding main is a clean no-op (main is an ancestor).
	if err := e.FoldInBranch(ctx, wt, "main"); err != nil {
		t.Fatalf("fold (up to date) must succeed: %v", err)
	}

	// main advances externally; fetch + fold brings it in without losing the dev-only work.
	advanceMain(t, origin, "from-main.txt", "hotfix on main\n")
	if err := e.Fetch(ctx, wt, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.FoldInBranch(ctx, wt, "main"); err != nil {
		t.Fatalf("fold (advanced main): %v", err)
	}
	if !exists(wt, "from-main.txt") {
		t.Errorf("main's change not folded into the dev state")
	}
	if !exists(wt, "dev-only.txt") {
		t.Errorf("dev-only work lost by the fold")
	}
}

// TestFoldInBranchConflict pins the non-fatal conflict path: a divergent change to the same file aborts
// the merge cleanly (ErrMergeConflict), leaving the dev branch exactly as before.
func TestFoldInBranchConflict(t *testing.T) {
	origin, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "README.md"), "dev version\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "dev edits README")
	head, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))

	advanceMain(t, origin, "README.md", "main version\n") // conflicting edit to the same file
	if err := e.Fetch(ctx, wt, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.FoldInBranch(ctx, wt, "main"); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("expected ErrMergeConflict, got %v", err)
	}
	after, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	if head != after {
		t.Errorf("conflicting fold moved HEAD (%s → %s) — must abort cleanly", head, after)
	}
	if out, _ := runGit(e.gitIn(ctx, wt, "", "status", "--porcelain")); out != "" {
		t.Errorf("aborted fold left the tree dirty: %q", out)
	}
}

// TestResetDevToDefault is req 5: the explicit way back discards the accumulated dev state and moves the
// dev branch back onto the default branch — an explicit action, never automatic.
func TestResetDevToDefault(t *testing.T) {
	_, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "discardme.txt"), "accumulated\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "work to be discarded")

	if err := e.ResetDevToDefault(ctx, wt, "", "main", dev); err != nil {
		t.Fatalf("ResetDevToDefault: %v", err)
	}
	if exists(wt, "discardme.txt") {
		t.Errorf("accumulated work survived an explicit reset to the default branch")
	}
	// The dev branch now equals origin/main exactly.
	devTip, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	mainTip, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "origin/main"))
	if devTip != mainTip {
		t.Errorf("after reset dev tip %s != origin/main %s", devTip, mainTip)
	}
}

// TestRevertRangeCounterBooks is req 10 + 13b: a counter-booking removes a delivery's effect from the dev
// state through a NEW reversing commit — history is not rewritten (the original commit remains and the
// commit count grows, never shrinks).
func TestRevertRangeCounterBooks(t *testing.T) {
	_, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	from, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	writeF(t, filepath.Join(wt, "feature.txt"), "delivered feature\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "delivery: add feature")
	to, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	countBefore := commitCount(t, e, ctx, wt)

	conflicted, changed, err := e.RevertRange(ctx, wt, from, to, "tester", 1, "Revert delivery: add feature")
	if err != nil {
		t.Fatalf("RevertRange: %v", err)
	}
	if conflicted || !changed {
		t.Fatalf("clean revert: conflicted=%v changed=%v, want false/true", conflicted, changed)
	}
	if exists(wt, "feature.txt") {
		t.Errorf("counter-booking did not remove the delivery's effect (feature.txt still present)")
	}
	// History untouched: the original delivery commit is still reachable, and a reverting commit was ADDED.
	if !commitReachable(t, e, ctx, wt, to) {
		t.Errorf("original delivery commit %s no longer reachable — history was rewritten", to)
	}
	if got := commitCount(t, e, ctx, wt); got != countBefore+1 {
		t.Errorf("commit count = %d, want %d (exactly one reversing commit added)", got, countBefore+1)
	}
}

// TestRevertRangeConflict is req 12 + 13c: when later work built on the delivery and touched the same
// lines, the counter-booking makes no guess — it reports a conflict and leaves the branch untouched, so
// the caller can raise a ToDo instead of committing a mangled revert.
func TestRevertRangeConflict(t *testing.T) {
	_, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	from, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	writeF(t, filepath.Join(wt, "shared.txt"), "line from delivery A\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "delivery A")
	to, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))

	// Later work builds on delivery A and rewrites the same line.
	writeF(t, filepath.Join(wt, "shared.txt"), "delivery B rewrote this line\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "delivery B on top of A")
	head, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))

	conflicted, changed, err := e.RevertRange(ctx, wt, from, to, "tester", 1, "Revert delivery A")
	if err != nil {
		t.Fatalf("RevertRange returned a hard error: %v", err)
	}
	if !conflicted || changed {
		t.Fatalf("expected a conflict (later work touched the same lines): conflicted=%v changed=%v", conflicted, changed)
	}
	after, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	if head != after {
		t.Errorf("a conflicting counter-booking moved HEAD (%s → %s) — it must leave the branch untouched", head, after)
	}
	if got := readF(t, filepath.Join(wt, "shared.txt")); got != "delivery B rewrote this line\n" {
		t.Errorf("later work was clobbered by an aborted revert: %q", got)
	}
	if out, _ := runGit(e.gitIn(ctx, wt, "", "status", "--porcelain")); out != "" {
		t.Errorf("aborted revert left the tree dirty: %q", out)
	}
}

// TestRevertRangeAlreadyReverted pins idempotency: counter-booking a delivery whose effect is already
// gone (a repeated rollback, or later work removed it) is a clean no-op — changed=false, no error, no new
// commit — never a spurious error or a re-application of the change.
func TestRevertRangeAlreadyReverted(t *testing.T) {
	_, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	from, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	writeF(t, filepath.Join(wt, "feature.txt"), "delivered\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "delivery")
	to, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))

	// First counter-booking removes the effect.
	if _, changed, err := e.RevertRange(ctx, wt, from, to, "tester", 1, "Revert 1"); err != nil || !changed {
		t.Fatalf("first revert: changed=%v err=%v", changed, err)
	}
	countAfterFirst := commitCount(t, e, ctx, wt)

	// A second counter-booking of the same range is a clean no-op (the effect is already gone).
	conflicted, changed, err := e.RevertRange(ctx, wt, from, to, "tester", 1, "Revert 2")
	if err != nil {
		t.Fatalf("repeated revert must not error, got %v", err)
	}
	if conflicted || changed {
		t.Errorf("repeated revert: conflicted=%v changed=%v, want false/false (idempotent no-op)", conflicted, changed)
	}
	if exists(wt, "feature.txt") {
		t.Errorf("repeated revert re-applied the delivery — feature.txt came back")
	}
	if got := commitCount(t, e, ctx, wt); got != countAfterFirst {
		t.Errorf("repeated revert added a commit (count %d → %d) — it must be a no-op", countAfterFirst, got)
	}
	if out, _ := runGit(e.gitIn(ctx, wt, "", "status", "--porcelain")); out != "" {
		t.Errorf("repeated revert left the tree dirty: %q", out)
	}
}

// TestStackedDeliveryDiff is req 9 + 13a: a delivery's PR base is the previous delivery's branch, so the
// diff shows ONLY that delivery's own changes even though it sits on the prior work.
func TestStackedDeliveryDiff(t *testing.T) {
	_, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	// Delivery 1 on the dev branch.
	writeF(t, filepath.Join(wt, "a.txt"), "delivery one\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "delivery 1")
	if err := e.BranchAt(ctx, wt, "mercury-run/r1", "HEAD"); err != nil {
		t.Fatalf("BranchAt d1: %v", err)
	}
	// Delivery 2 stacks on top (same dev branch keeps growing).
	writeF(t, filepath.Join(wt, "b.txt"), "delivery two\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "delivery 2")
	if err := e.BranchAt(ctx, wt, "mercury-run/r2", "HEAD"); err != nil {
		t.Fatalf("BranchAt d2: %v", err)
	}

	// The PR of delivery 2 (base = delivery 1's branch) shows ONLY b.txt — not a.txt.
	diff, _ := runGit(e.gitIn(ctx, wt, "", "diff", "--name-only", "mercury-run/r1", "mercury-run/r2"))
	if diff != "b.txt" {
		t.Errorf("stacked PR diff = %q, want only %q — the PR must show only its own changes", diff, "b.txt")
	}
	// The full branch still CONTAINS the prior work (req 3 — it carries the not-yet-merged prior work).
	both, _ := runGit(e.gitIn(ctx, wt, "", "ls-tree", "--name-only", "mercury-run/r2"))
	if !contains(both, "a.txt") || !contains(both, "b.txt") {
		t.Errorf("delivery 2 branch must carry both a.txt and b.txt, got tree: %q", both)
	}
}

// TestDevBranchKeepsUnpushedLocalCommits is the core of the loss fix (req 1/7): a run commits to the
// persistent dev branch but is interrupted BEFORE publishing, so the commit lives only locally. The next
// run, in the SAME workspace, must KEEP that commit — never reset the branch onto the (stale) remote — and
// fold the remote in. It also reports the recovered commit (req 5).
func TestDevBranchKeepsUnpushedLocalCommits(t *testing.T) {
	origin, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"
	_ = origin

	// Run 1 establishes AND publishes mercury-dev, so origin/mercury-dev exists.
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "published.txt"), "published\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "run1 published")
	if _, err := e.PushRefs(ctx, wt, "", false, dev); err != nil {
		t.Fatal(err)
	}

	// Run 2 (same workspace) prepares the branch, commits MORE work, but is interrupted before PushRefs —
	// the commit exists only on the local branch.
	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "unpushed.txt"), "interrupted before publish\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "run2 unpublished work")
	unpushedTip, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))

	// Run 3, same workspace, prepares the branch AGAIN. The unpushed commit MUST survive — the pre-fix
	// `checkout -f -B mercury-dev origin/mercury-dev` would have reset it away.
	prep, err := e.EnsureDevBranch(ctx, wt, "", "main", dev)
	if err != nil {
		t.Fatalf("EnsureDevBranch run3: %v", err)
	}
	if !exists(wt, "unpushed.txt") {
		t.Errorf("unpushed local commit was LOST — the branch was reset onto the remote")
	}
	if !exists(wt, "published.txt") {
		t.Errorf("published work missing after preparing the next run")
	}
	if prep.Recovered != 1 {
		t.Errorf("Recovered = %d, want 1 (the one unpublished commit)", prep.Recovered)
	}
	if !commitReachable(t, e, ctx, wt, unpushedTip) {
		t.Errorf("unpushed commit %s no longer reachable from HEAD — it was discarded", unpushedTip)
	}
}

// TestDevBranchFirstRunUnpushedKept covers the FIRST-ever run interrupted before it could publish: the
// local mercury-dev exists with commits, but origin/mercury-dev does NOT. The next run must KEEP the local
// branch (grow on it), never recreate it from the default branch and discard the first run's work — the
// pre-fix code did exactly that whenever the remote dev branch was absent.
func TestDevBranchFirstRunUnpushedKept(t *testing.T) {
	_, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"

	prep, err := e.EnsureDevBranch(ctx, wt, "", "main", dev)
	if err != nil {
		t.Fatal(err)
	}
	if !prep.Created {
		t.Fatalf("first run should report Created=true")
	}
	writeF(t, filepath.Join(wt, "first.txt"), "first run work\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "first run work")
	// No push — origin/mercury-dev is never created (the run was interrupted).

	prep, err = e.EnsureDevBranch(ctx, wt, "", "main", dev)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Created {
		t.Errorf("second run must NOT recreate the branch — a local dev branch already exists")
	}
	if !exists(wt, "first.txt") {
		t.Errorf("first run's unpublished work was discarded by a reset to the default branch")
	}
	if prep.Recovered != 1 {
		t.Errorf("Recovered = %d, want 1", prep.Recovered)
	}
}

// TestDevBranchRemoteFoldConflictKeepsLocal is req 2/7: when the pushed dev state has DIVERGED from the
// kept local dev branch (both changed the same line), folding it in conflicts — and that must reset
// NOTHING. The local commits stay exactly as they were, the tree is clean, and RemoteConflict is reported
// so the run can decide per the Laufregeln whether to proceed.
func TestDevBranchRemoteFoldConflictKeepsLocal(t *testing.T) {
	origin, wt, e := devTestRepo(t)
	ctx := context.Background()
	const dev = "mercury-dev"

	if _, err := e.EnsureDevBranch(ctx, wt, "", "main", dev); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "shared.txt"), "base\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "base")
	if _, err := e.PushRefs(ctx, wt, "", false, dev); err != nil {
		t.Fatal(err)
	}

	// Another worker advances origin/mercury-dev with a conflicting edit to the same line.
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, "", "clone", "-b", dev, origin, other)
	writeF(t, filepath.Join(other, "shared.txt"), "remote version\n")
	gitT(t, other, "add", "-A")
	gitCommit(t, other, "remote edits shared")
	gitT(t, other, "push", "origin", dev)

	// This workspace's interrupted run edited the SAME line differently and committed, but never pushed.
	writeF(t, filepath.Join(wt, "shared.txt"), "local version\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "local edits shared")
	localTip, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))

	prep, err := e.EnsureDevBranch(ctx, wt, "", "main", dev)
	if err != nil {
		t.Fatalf("a fold conflict must be non-fatal, got err: %v", err)
	}
	if !prep.RemoteConflict {
		t.Errorf("RemoteConflict not reported for a diverged pushed dev state")
	}
	after, _ := runGit(e.gitIn(ctx, wt, "", "rev-parse", "HEAD"))
	if after != localTip {
		t.Errorf("HEAD moved (%s → %s) — a conflicting fold must keep the local state exactly", localTip, after)
	}
	if got := readF(t, filepath.Join(wt, "shared.txt")); got != "local version\n" {
		t.Errorf("local work clobbered by a conflicting fold: %q", got)
	}
	if out, _ := runGit(e.gitIn(ctx, wt, "", "status", "--porcelain")); out != "" {
		t.Errorf("aborted fold left the tree dirty: %q", out)
	}
}

func commitCount(t *testing.T, e Executor, ctx context.Context, wt string) int {
	t.Helper()
	n, err := e.CommitsAhead(ctx, wt, "origin/main")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func commitReachable(t *testing.T, e Executor, ctx context.Context, wt, sha string) bool {
	t.Helper()
	_, err := runGit(e.gitIn(ctx, wt, "", "merge-base", "--is-ancestor", sha, "HEAD"))
	return err == nil
}

func contains(haystack, needle string) bool {
	for _, line := range splitLines(haystack) {
		if line == needle {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
