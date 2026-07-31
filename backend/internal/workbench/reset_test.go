package workbench

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/model"
)

// TestResetOnlyExplicit is REQ-022.4/K-1: the machinery (Prepare, folds, Publish, hygiene)
// NEVER moves the workbench back onto the default branch — only the explicit, actor-bound
// ResetToDefault does. And even that shelters the discarded tip under a rescue ref before
// force-publishing the reset.
func TestResetOnlyExplicit(t *testing.T) {
	origin, wt, b := testRepo(t)
	ctx := context.Background()

	// Accumulate real state through the whole machinery.
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "accumulated.txt"), "grown state\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "accumulated work")
	if err := b.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.FoldInDefault(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Prepare(ctx); err != nil { // a full second pass — still no reset anywhere
		t.Fatal(err)
	}
	if err := b.CleanUntracked(ctx); err != nil {
		t.Fatal(err)
	}
	oldTip := gitOut(t, wt, "rev-parse", "refs/heads/"+LegacyShared)
	mainTip := gitOut(t, wt, "rev-parse", "origin/main")
	if oldTip == mainTip {
		t.Fatal("precondition: the workbench must be ahead of the default branch")
	}

	// The one explicit way back.
	if err := b.ResetToDefault(ctx, model.Actor{User: "alice"}); err != nil {
		t.Fatalf("ResetToDefault: %v", err)
	}
	if exists(wt, "accumulated.txt") {
		t.Errorf("accumulated work survived an explicit reset")
	}
	if got := gitOut(t, wt, "rev-parse", "refs/heads/"+LegacyShared); got != mainTip {
		t.Errorf("workbench tip = %s, want the default tip %s", got, mainTip)
	}
	// The reset is published: origin's workbench equals the default tip too.
	if got := gitOut(t, origin, "rev-parse", "refs/heads/"+LegacyShared); got != mainTip {
		t.Errorf("origin workbench = %s, want %s — the reset was not published", got, mainTip)
	}
	// The discarded state is sheltered, locally and on the origin.
	refs := gitOut(t, wt, "for-each-ref", "--format=%(objectname)", "refs/devlab/rescue")
	if !strings.Contains(refs, oldTip) {
		t.Errorf("discarded tip %s not sheltered under a local rescue ref (got %q)", oldTip, refs)
	}
	originRefs := gitOut(t, origin, "for-each-ref", "--format=%(objectname)", "refs/devlab/rescue")
	if !strings.Contains(originRefs, oldTip) {
		t.Errorf("discarded tip %s not sheltered on the origin (got %q)", oldTip, originRefs)
	}
}

// TestResetRefusesTheMachinery: a reset is a person's explicit act — the autonomous system
// (with or without an on-behalf-of) and an anonymous actor are refused, and nothing moves.
func TestResetRefusesTheMachinery(t *testing.T) {
	_, wt, b := testRepo(t)
	ctx := context.Background()
	if _, err := b.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	writeF(t, filepath.Join(wt, "keep.txt"), "stays\n")
	gitT(t, wt, "add", "-A")
	gitCommit(t, wt, "work")
	tip := gitOut(t, wt, "rev-parse", "refs/heads/"+LegacyShared)

	for _, by := range []model.Actor{
		{},
		{Autonomous: true},
		{Autonomous: true, OnBehalfOf: "alice"},
		{User: "alice", Autonomous: true},
	} {
		if err := b.ResetToDefault(ctx, by); err == nil {
			t.Errorf("ResetToDefault accepted non-person actor %+v", by)
		}
	}
	if got := gitOut(t, wt, "rev-parse", "refs/heads/"+LegacyShared); got != tip {
		t.Errorf("a refused reset moved the workbench (%s → %s)", tip, got)
	}
	if !exists(wt, "keep.txt") {
		t.Errorf("a refused reset discarded work")
	}
}
