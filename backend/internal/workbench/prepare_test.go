package workbench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPrepareCreatesFromDefaultFirstTime: the very first establishment derives the
// workbench from the default branch and says so (Created + FoldedDefault).
func TestPrepareCreatesFromDefaultFirstTime(t *testing.T) {
	_, wt, b := testRepo(t)
	res, err := b.Prepare(context.Background())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !res.Created || !res.FoldedDefault {
		t.Errorf("first Prepare: Created=%v FoldedDefault=%v, want true/true", res.Created, res.FoldedDefault)
	}
	if res.Conflicted || res.Recovered != 0 || len(res.Orphans) != 0 {
		t.Errorf("first Prepare reported drama on a pristine repo: %+v", res)
	}
	if got := gitOut(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != Branch {
		t.Errorf("working tree on %q, want %s", got, Branch)
	}
	if res.Head == "" || res.Head != gitOut(t, wt, "rev-parse", "HEAD") {
		t.Errorf("Head %q does not name the tip", res.Head)
	}
}

// TestWorkbenchGrowsAcrossWorkspaces is K-1/REQ-022.1: the work of run n is present in the
// state of run n+1 — even from a wiped, recreated workspace the accumulated state is grown
// onto, never reset back to the default branch.
func TestWorkbenchGrowsAcrossWorkspaces(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "run1.txt"), "built by run 1\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "run1 work")
	if err := b.Publish(ctx); err != nil {
		t.Fatalf("Publish run1: %v", err)
	}

	// A brand-new workspace clone (wiped/recreated) must recover the grown state.
	wt2 := filepath.Join(t.TempDir(), "work2")
	gitT(t, "", "clone", origin, wt2)
	b2 := benchAt(t, wt2)
	res, err := b2.Prepare(ctx)
	if err != nil {
		t.Fatalf("Prepare run2: %v", err)
	}
	if res.Created {
		t.Errorf("second Prepare must not report Created — the workbench already exists on the remote")
	}
	if !res.FoldedRemote {
		t.Errorf("second Prepare must report the pushed state as incorporated")
	}
	if !exists(wt2, "run1.txt") {
		t.Errorf("run1's work missing after preparing the next run — the state was reset, not grown")
	}
	if got := gitOut(t, wt2, "rev-parse", "--abbrev-ref", "HEAD"); got != Branch {
		t.Errorf("workspace on %q, want %s", got, Branch)
	}
}

// TestPrepareKeepsUnpushedLocalCommits is the core of K-1: a run committed to the workbench
// but was interrupted BEFORE publishing, so the commit lives only locally while the remote
// is BEHIND. The next preparation keeps the commit — never a reset onto the stale remote —
// folds the remote in (a clean no-op here) and reports the recovered work.
func TestPrepareKeepsUnpushedLocalCommits(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()

	// Run 1 establishes AND publishes the workbench, so origin/mercury-dev exists.
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "published.txt"), "published\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "run1 published")
	if err := b.Publish(ctx); err != nil {
		t.Fatal(err)
	}

	// Run 2 commits more work but is interrupted before any publish.
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "unpushed.txt"), "interrupted before publish\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "run2 unpublished work")
	unpushedTip := gitOut(t, wt, "rev-parse", "HEAD")

	// Run 3 prepares the SAME workspace again. The pre-fix machinery reset the branch onto
	// origin/mercury-dev here and silently destroyed the commit.
	res, err := b.Prepare(ctx)
	if err != nil {
		t.Fatalf("Prepare run3: %v", err)
	}
	if !exists(wt, "unpushed.txt") {
		t.Errorf("unpushed local commit was LOST — the branch was reset onto the remote")
	}
	if !exists(wt, "published.txt") {
		t.Errorf("published work missing after preparing the next run")
	}
	if res.Recovered != 1 {
		t.Errorf("Recovered = %d, want 1 (the one unpublished commit)", res.Recovered)
	}
	if res.Conflicted {
		t.Errorf("a merely-behind remote must fold in as a clean no-op, got a conflict")
	}
	if !commitReachable(t, wt, unpushedTip) {
		t.Errorf("unpushed commit %s no longer reachable — it was discarded", unpushedTip)
	}
}

// TestPrepareFirstRunUnpushedKept covers the FIRST-ever run interrupted before it could
// publish: the local workbench exists with commits, origin/mercury-dev does NOT. The next
// preparation keeps and grows the local branch — never recreates it from the default branch.
func TestPrepareFirstRunUnpushedKept(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()

	res, err := b.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created {
		t.Fatalf("first Prepare should report Created")
	}
	writeF(t, filepath.Join(wt, "first.txt"), "first run work\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "first run work")
	// No publish — origin/mercury-dev is never created (the run was interrupted).

	res, err = b.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created {
		t.Errorf("second Prepare must NOT recreate the branch — a local workbench already exists")
	}
	if !exists(wt, "first.txt") {
		t.Errorf("first run's unpublished work was discarded by a reset to the default branch")
	}
	if res.Recovered != 1 {
		t.Errorf("Recovered = %d, want 1", res.Recovered)
	}
}

// TestPrepareRemoteFoldConflictKeepsLocal is REQ-023.2/K-1: a diverged pushed state that
// conflicts with the kept local commits resets NOTHING — the conflict is named, the local
// state stays byte-for-byte, the tree ends clean.
func TestPrepareRemoteFoldConflictKeepsLocal(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "shared.txt"), "base\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "base")
	if err := b.Publish(ctx); err != nil {
		t.Fatal(err)
	}

	// Another worker publishes a conflicting edit; this workspace edits the SAME line and
	// commits, but never pushed.
	advanceDev(t, origin, "shared.txt", "remote version\n")
	writeF(t, filepath.Join(wt, "shared.txt"), "local version\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "local edits shared")
	localTip := gitOut(t, wt, "rev-parse", "HEAD")

	res, err := b.Prepare(ctx)
	if err != nil {
		t.Fatalf("a fold conflict must be non-fatal, got: %v", err)
	}
	if !res.Conflicted {
		t.Errorf("Conflicted not reported for a diverged pushed state")
	}
	if len(res.ConflictFiles) == 0 || res.ConflictFiles[0] != "shared.txt" {
		t.Errorf("ConflictFiles = %v, want [shared.txt] — the conflict must be NAMED", res.ConflictFiles)
	}
	if after := gitOut(t, wt, "rev-parse", "HEAD"); after != localTip {
		t.Errorf("HEAD moved (%s → %s) — a conflicting fold must keep the local state exactly", localTip, after)
	}
	if got := readF(t, filepath.Join(wt, "shared.txt")); got != "local version\n" {
		t.Errorf("local work clobbered by a conflicting fold: %q", got)
	}
	if out := gitOut(t, wt, "status", "--porcelain"); out != "" {
		t.Errorf("aborted fold left the tree dirty: %q", out)
	}
}

// TestPrepareFoldsDivergedRemoteIn: local unpushed commits + a compatibly diverged remote
// fold together — BOTH sides' work present afterwards, nothing discarded.
func TestPrepareFoldsDivergedRemoteIn(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "base.txt"), "base\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "base")
	if err := b.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	advanceDev(t, origin, "remote.txt", "from the remote\n")
	writeF(t, filepath.Join(wt, "local.txt"), "unpushed local\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "local unpushed")

	res, err := b.Prepare(ctx)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if res.Conflicted {
		t.Fatalf("compatible divergence must not conflict")
	}
	if !res.FoldedRemote {
		t.Errorf("FoldedRemote not reported")
	}
	if !exists(wt, "local.txt") || !exists(wt, "remote.txt") {
		t.Errorf("fold lost a side: local=%v remote=%v", exists(wt, "local.txt"), exists(wt, "remote.txt"))
	}
	if res.Recovered != 1 {
		t.Errorf("Recovered = %d, want 1 (the unpushed local commit)", res.Recovered)
	}
}

// TestFoldInDefaultGrowsOnly is REQ-022.2: the default branch is folded INTO the workbench
// — its changes arrive, the workbench's own work is retained, the previous tip remains an
// ancestor (the state only ever grows). An already-contained default is a clean no-op.
func TestFoldInDefaultGrowsOnly(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "dev-only.txt"), "dev work\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "dev work")
	before := gitOut(t, wt, "rev-parse", "HEAD")

	// No divergence yet → clean no-op.
	rep, err := b.FoldInDefault(ctx)
	if err != nil {
		t.Fatalf("fold (up to date): %v", err)
	}
	if rep.Folded || rep.Conflicted {
		t.Errorf("up-to-date fold: Folded=%v Conflicted=%v, want false/false", rep.Folded, rep.Conflicted)
	}

	// The default branch advances externally; the fold brings it in without losing dev work.
	advanceMain(t, origin, "from-main.txt", "hotfix on main\n")
	rep, err = b.FoldInDefault(ctx)
	if err != nil {
		t.Fatalf("fold (advanced main): %v", err)
	}
	if !rep.Folded {
		t.Errorf("Folded not reported for an advanced default branch")
	}
	if !exists(wt, "from-main.txt") {
		t.Errorf("default branch's change not folded into the workbench")
	}
	if !exists(wt, "dev-only.txt") {
		t.Errorf("dev-only work lost by the fold")
	}
	if !commitReachable(t, wt, before) {
		t.Errorf("previous tip no longer an ancestor — the fold rewrote instead of growing")
	}
}

// TestFoldInDefaultConflictNamed: a conflicting default-branch change is named and rolls
// nothing back (REQ-023.2) — the run decides how to proceed.
func TestFoldInDefaultConflictNamed(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "README.md"), "dev version\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "dev edits README")
	head := gitOut(t, wt, "rev-parse", "HEAD")

	advanceMain(t, origin, "README.md", "main version\n")
	rep, err := b.FoldInDefault(ctx)
	if err != nil {
		t.Fatalf("a fold conflict must be non-fatal, got: %v", err)
	}
	if !rep.Conflicted {
		t.Fatalf("Conflicted not reported")
	}
	if len(rep.ConflictFiles) == 0 || rep.ConflictFiles[0] != "README.md" {
		t.Errorf("ConflictFiles = %v, want [README.md]", rep.ConflictFiles)
	}
	if after := gitOut(t, wt, "rev-parse", "HEAD"); after != head {
		t.Errorf("conflicting fold moved HEAD (%s → %s)", head, after)
	}
	if out := gitOut(t, wt, "status", "--porcelain"); out != "" {
		t.Errorf("aborted fold left the tree dirty: %q", out)
	}
}

// TestFoldInDefaultRequiresPreparedWorkbench: folding into a working tree that is not on
// the workbench is a caller error, named honestly.
func TestFoldInDefaultRequiresPreparedWorkbench(t *testing.T) {
	_, _, b := testRepo(t)
	if _, err := b.FoldInDefault(context.Background()); err == nil {
		t.Fatal("FoldInDefault on an unprepared working tree must fail")
	}
}

// TestCleanUntrackedOnlyUnversioned is REQ-023.4: hygiene reverts uncommitted edits and
// removes untracked and ignored files — commits and the branch pointer are untouched.
func TestCleanUntrackedOnlyUnversioned(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "committed.txt"), "keep me\n")
	writeF(t, filepath.Join(wt, ".gitignore"), "build/\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "accumulated work")
	before := gitOut(t, wt, "rev-parse", "HEAD")

	// An aborted run's leftovers: an uncommitted edit, an untracked file, an ignored artefact.
	writeF(t, filepath.Join(wt, "committed.txt"), "half-edited\n")
	writeF(t, filepath.Join(wt, "stray.txt"), "orphan\n")
	if err := os.MkdirAll(filepath.Join(wt, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "build", "artefact.bin"), "stale\n")

	if err := b.CleanUntracked(ctx); err != nil {
		t.Fatalf("CleanUntracked: %v", err)
	}
	if got := readF(t, filepath.Join(wt, "committed.txt")); got != "keep me\n" {
		t.Errorf("tracked edit not reverted: got %q", got)
	}
	if exists(wt, "stray.txt") || exists(wt, filepath.Join("build", "artefact.bin")) {
		t.Errorf("untracked/ignored leftovers survived hygiene")
	}
	if after := gitOut(t, wt, "rev-parse", "HEAD"); before != after {
		t.Errorf("HEAD moved (%s → %s) — hygiene must never touch commits", before, after)
	}
	if out := gitOut(t, wt, "status", "--porcelain"); out != "" {
		t.Errorf("worktree not clean: %q", out)
	}
}
