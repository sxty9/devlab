package workbench

import (
	"context"
	"path/filepath"
	"testing"
)

// AheadOfDefault answers honestly in all three states: no workbench at all, a workbench level with
// the default branch, and a workbench carrying its own commits.
func TestAheadOfDefaultIsHonestInEveryState(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()

	// (a) no workbench yet — not ahead, no tip, and NOT an error.
	ahead, head, err := b.AheadOfDefault(ctx)
	if err != nil {
		t.Fatalf("AheadOfDefault on a pristine repo: %v", err)
	}
	if ahead || head != "" {
		t.Errorf("pristine repo: ahead=%v head=%q, want false/empty", ahead, head)
	}

	// (b) established but level with the default branch.
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	ahead, head, err = b.AheadOfDefault(ctx)
	if err != nil {
		t.Fatalf("AheadOfDefault after Prepare: %v", err)
	}
	if ahead {
		t.Errorf("a freshly created workbench is not ahead of the default branch")
	}
	if head != gitOut(t, wt, "rev-parse", "refs/heads/"+Branch) {
		t.Errorf("head %q does not name the workbench tip", head)
	}

	// (c) with its own commit — ahead, and the tip moved.
	writeF(t, filepath.Join(wt, "work.txt"), "work\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "work")
	ahead, head, err = b.AheadOfDefault(ctx)
	if err != nil {
		t.Fatalf("AheadOfDefault after a commit: %v", err)
	}
	if !ahead {
		t.Errorf("a workbench carrying its own commit IS ahead of the default branch")
	}
	if head != gitOut(t, wt, "rev-parse", "refs/heads/"+Branch) {
		t.Errorf("head %q is stale", head)
	}
}

// ContainedInDefault is the "already delivered" probe: the seed commit is in the default branch, a
// workbench commit is not — and an unknown object is an ERROR, never a silent "not contained".
func TestContainedInDefaultProvesDelivery(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	seed := gitOut(t, wt, "rev-parse", "refs/remotes/origin/main")

	in, err := b.ContainedInDefault(ctx, seed)
	if err != nil {
		t.Fatalf("ContainedInDefault(seed): %v", err)
	}
	if !in {
		t.Errorf("the default branch's own tip must read as contained")
	}

	writeF(t, filepath.Join(wt, "undelivered.txt"), "not on main\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "undelivered")
	own := gitOut(t, wt, "rev-parse", "HEAD")
	in, err = b.ContainedInDefault(ctx, own)
	if err != nil {
		t.Fatalf("ContainedInDefault(own): %v", err)
	}
	if in {
		t.Errorf("a workbench-only commit must not read as delivered")
	}

	if _, err := b.ContainedInDefault(ctx, "0000000000000000000000000000000000000000"); err == nil {
		t.Errorf("an unknown object must be an error, not a quiet \"not contained\"")
	}
	if in, err := b.ContainedInDefault(ctx, ""); in || err != nil {
		t.Errorf("an empty commit is simply not contained: in=%v err=%v", in, err)
	}
}

// MergeBaseDefault + CommitsAhead name the delivery span: base = where the default branch and the
// workbench last agreed, count = the commits added since.
func TestMergeBaseDefaultAndCommitsAheadNameTheSpan(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	base, err := b.MergeBaseDefault(ctx)
	if err != nil {
		t.Fatalf("MergeBaseDefault: %v", err)
	}
	if base != gitOut(t, wt, "rev-parse", "refs/remotes/origin/main") {
		t.Errorf("base %q is not where the two branches agree", base)
	}

	for _, name := range []string{"a.txt", "b.txt"} {
		writeF(t, filepath.Join(wt, name), name)
		gitT(t, wt, "add", "-A")
		gitCommit(t, wt, "add "+name)
	}
	n, err := b.CommitsAhead(ctx, base)
	if err != nil {
		t.Fatalf("CommitsAhead: %v", err)
	}
	if n != 2 {
		t.Errorf("CommitsAhead = %d, want 2", n)
	}

	// The default branch moving on does NOT change the recorded span start of work already done.
	advanceMain(t, origin, "third-party.txt", "elsewhere\n")
	if n, err := b.CommitsAhead(ctx, base); err != nil || n != 2 {
		t.Errorf("after the default branch advanced: CommitsAhead = %d (err %v), want 2", n, err)
	}
}

// CommitAll secures loose work as a commit and is a no-op on a clean tree — an agent that already
// committed must never lose its tip to this call (K-1).
func TestCommitAllSecuresLooseWorkAndNeverMovesBack(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := b.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Clean tree: the tip is returned unchanged.
	head, err := b.CommitAll(ctx, "nothing to do", "", 0)
	if err != nil {
		t.Fatalf("CommitAll on a clean tree: %v", err)
	}
	if head != before {
		t.Errorf("a clean tree must leave the tip at %s, got %s", before, head)
	}

	// Loose work: one commit, and it is REACHABLE from the tip.
	writeF(t, filepath.Join(wt, "loose.txt"), "left behind\n")
	head, err = b.CommitAll(ctx, "mercury-run: secure loose work", "runner", 42)
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if head == before {
		t.Fatalf("loose work was not committed (tip still %s)", before)
	}
	if !commitReachable(t, wt, before) {
		t.Errorf("the previous tip is no longer reachable — the branch moved backwards")
	}
	if dirty, err := b.HasUncommitted(ctx); err != nil || dirty {
		t.Errorf("the tree is still dirty after CommitAll: dirty=%v err=%v", dirty, err)
	}
	if got := gitOut(t, wt, "log", "-1", "--format=%an <%ae>"); got != "runner <42+runner@users.noreply.github.com>" {
		t.Errorf("commit identity = %q — the linked account is not carried", got)
	}
	if _, err := b.CommitAll(ctx, "  ", "", 0); err == nil {
		t.Errorf("an empty commit message must be refused")
	}
}

// ReadFile / WriteFile are the file seam of the workbench: an absent file is an ANSWER, a write is
// readable back, and a path escaping the working tree is refused.
func TestReadWriteFileStayInsideTheWorkingTree(t *testing.T) {
	_, wt, b := testRepo(t)
	if _, err := b.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}

	content, exists, err := b.ReadFile("CLAUDE.md")
	if err != nil || exists || content != "" {
		t.Errorf("an absent file must read as exists=false without an error: %q %v %v", content, exists, err)
	}
	if err := b.WriteFile("CLAUDE.md", []byte("# constitution\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	content, exists, err = b.ReadFile("CLAUDE.md")
	if err != nil || !exists || content != "# constitution\n" {
		t.Errorf("written file reads back as %q exists=%v err=%v", content, exists, err)
	}
	if readF(t, filepath.Join(wt, "CLAUDE.md")) != "# constitution\n" {
		t.Errorf("the file did not land in the working tree")
	}
	if _, _, err := b.ReadFile("../escape.txt"); err == nil {
		t.Errorf("a path leaving the working tree must be refused")
	}
	if err := b.WriteFile("../escape.txt", []byte("x")); err == nil {
		t.Errorf("a write leaving the working tree must be refused")
	}
}

// BranchAt cuts the delivery branch from the workbench WITHOUT re-pointing the workbench, and
// PushBranch routes the workbench itself through its own publish path rather than a weaker push.
func TestBranchAtLeavesTheWorkbenchWhereItIs(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "feature.txt"), "feature\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "feature")
	tip, err := b.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := b.BranchAt(ctx, "feature/thing", tip); err != nil {
		t.Fatalf("BranchAt: %v", err)
	}
	if got := gitOut(t, wt, "rev-parse", "refs/heads/feature/thing"); got != tip {
		t.Errorf("delivery branch at %s, want %s", got, tip)
	}
	if got := gitOut(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != Branch {
		t.Errorf("the working tree left the workbench (now on %q)", got)
	}

	if err := b.PushBranch(ctx, "feature/thing"); err != nil {
		t.Fatalf("PushBranch(delivery): %v", err)
	}
	if err := b.PushBranch(ctx, Branch); err != nil {
		t.Fatalf("PushBranch(workbench): %v", err)
	}
	if got := gitOut(t, wt, "rev-parse", "refs/remotes/origin/"+Branch); got != tip {
		t.Errorf("the workbench was not published through its own path: origin at %s, want %s", got, tip)
	}
}
