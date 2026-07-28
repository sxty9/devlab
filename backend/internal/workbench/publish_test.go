package workbench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestPublishAfterCommit is K-1's "abort right after a commit ⇒ already pushed": one
// Publish directly after the commit puts it on the durable remote — an interruption from
// here on costs nothing. The shipped workbench tip carries the work.
func TestPublishAfterCommit(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "work.txt"), "committed then published\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "work")
	tip := gitOut(t, wt, "rev-parse", "HEAD")

	if err := b.Publish(ctx); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// The abort happens HERE — and costs nothing: origin already carries the commit.
	if got := gitOut(t, origin, "rev-parse", "refs/heads/"+Branch); got != tip {
		t.Errorf("origin %s = %s, want %s — the commit was not published", Branch, got, tip)
	}
	shipped := filepath.Join(t.TempDir(), "shipped")
	gitT(t, "", "clone", "-b", Branch, origin, shipped)
	if !exists(shipped, "work.txt") {
		t.Errorf("published workbench does not carry the committed work")
	}
}

// TestPublishRejectedFoldsAndRetries is REQ-023.3: a push rejected because the remote
// advanced folds the remote in and retries — afterwards origin carries BOTH sides' work,
// and nothing was forced or reset.
func TestPublishRejectedFoldsAndRetries(t *testing.T) {
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

	advanceDev(t, origin, "remote.txt", "remote work\n") // origin moves ahead
	writeF(t, filepath.Join(wt, "local.txt"), "local work\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "local work")

	if err := b.Publish(ctx); err != nil {
		t.Fatalf("Publish (rejected → fold → retry): %v", err)
	}
	shipped := filepath.Join(t.TempDir(), "shipped")
	gitT(t, "", "clone", "-b", Branch, origin, shipped)
	if !exists(shipped, "remote.txt") || !exists(shipped, "local.txt") {
		t.Errorf("published state lost a side: remote=%v local=%v", exists(shipped, "remote.txt"), exists(shipped, "local.txt"))
	}
}

// TestPublishConflictIsHonest: when the remote diverged INCOMPATIBLY, Publish names the
// conflict and changes nothing — no force, no reset, local commits intact, origin intact.
func TestPublishConflictIsHonest(t *testing.T) {
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

	advanceDev(t, origin, "shared.txt", "remote version\n")
	remoteTip := gitOut(t, origin, "rev-parse", "refs/heads/"+Branch)
	writeF(t, filepath.Join(wt, "shared.txt"), "local version\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "local conflicting")
	localTip := gitOut(t, wt, "rev-parse", "HEAD")

	err := b.Publish(ctx)
	if err == nil {
		t.Fatal("Publish must fail honestly on an unfoldable divergence")
	}
	if !strings.Contains(err.Error(), "shared.txt") {
		t.Errorf("the publish error must NAME the conflict, got: %v", err)
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != localTip {
		t.Errorf("local tip moved (%s → %s) — a failed publish must change nothing", localTip, got)
	}
	if got := gitOut(t, origin, "rev-parse", "refs/heads/"+Branch); got != remoteTip {
		t.Errorf("origin tip moved (%s → %s) — a failed publish must never force", remoteTip, got)
	}
	if out := gitOut(t, wt, "status", "--porcelain"); out != "" {
		t.Errorf("failed publish left the tree dirty: %q", out)
	}
}
