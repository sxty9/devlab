package workbench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// orphanCommit manufactures an orphan: a committed piece of work whose branch ref is then
// moved away by an EXTERNAL actor (never by this machinery), leaving the commit reachable
// from no ref at all. Returns the orphaned sha.
func orphanCommit(t *testing.T, wt string) string {
	t.Helper()
	base := gitOut(t, wt, "rev-parse", "HEAD")
	writeF(t, filepath.Join(wt, "precious.txt"), "committed, then externally stranded\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "precious work")
	sha := gitOut(t, wt, "rev-parse", "HEAD")
	// External mangling: the branch ref is moved back, the commit is now unreachable
	// (reflogs deliberately do not count — they expire).
	gitT(t, wt, "update-ref", "refs/heads/"+Branch, base)
	gitT(t, wt, "reset", "--hard", "HEAD") // realign index+tree with the moved ref (test-side hygiene)
	return sha
}

// TestCollectOrphansRescues is REQ-023.5/K-1: a committed-but-unreachable commit gets a
// rescue ref under refs/devlab/rescue/*, a best-effort push, and a report — and the sweep
// is idempotent (a rescued commit is reachable and never reported again).
func TestCollectOrphansRescues(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	sha := orphanCommit(t, wt)

	orphans, err := b.CollectOrphans(ctx)
	if err != nil {
		t.Fatalf("CollectOrphans: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("orphans = %d, want 1 (%v)", len(orphans), orphans)
	}
	o := orphans[0]
	if o.Commit != sha {
		t.Errorf("rescued %s, want %s", o.Commit, sha)
	}
	if !strings.HasPrefix(o.RescueRef, rescueRefPrefix) {
		t.Errorf("rescue ref %q outside %s", o.RescueRef, rescueRefPrefix)
	}
	if got := gitOut(t, wt, "rev-parse", o.RescueRef); got != sha {
		t.Errorf("local rescue ref points at %s, want %s", got, sha)
	}
	if !o.Pushed {
		t.Errorf("rescue not pushed (best-effort backup expected to succeed against a live origin)")
	}
	if got := gitOut(t, origin, "rev-parse", o.RescueRef); got != sha {
		t.Errorf("origin rescue ref points at %s, want %s", got, sha)
	}

	// Idempotent: the rescued commit is reachable now — nothing further to secure.
	again, err := b.CollectOrphans(ctx)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second sweep re-rescued: %v", again)
	}
}

// TestPrepareSweepsOrphans: the rescue runs as part of the normal preparation — an
// interrupted predecessor's stranded commit is secured before the next run builds.
func TestPrepareSweepsOrphans(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	sha := orphanCommit(t, wt)

	res, err := b.Prepare(ctx)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(res.Orphans) != 1 || res.Orphans[0].Commit != sha {
		t.Fatalf("Prepare.Orphans = %+v, want the stranded commit %s", res.Orphans, sha)
	}
	if got := gitOut(t, wt, "rev-parse", res.Orphans[0].RescueRef); got != sha {
		t.Errorf("rescue ref points at %s, want %s", got, sha)
	}
}

// TestCollectOrphansQuietOnCleanRepo: a repository with nothing stranded reports nothing —
// no refs invented, no noise.
func TestCollectOrphansQuietOnCleanRepo(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	orphans, err := b.CollectOrphans(ctx)
	if err != nil {
		t.Fatalf("CollectOrphans: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("clean repo yielded orphans: %v", orphans)
	}
	if out := gitOut(t, wt, "for-each-ref", rescueRefPrefix[:len(rescueRefPrefix)-1]); out != "" {
		t.Errorf("rescue refs invented on a clean repo: %q", out)
	}
}
