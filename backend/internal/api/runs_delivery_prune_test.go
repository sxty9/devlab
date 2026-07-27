package api

import (
	"context"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/runs"
)

// TestMaintainPrunesMergedDeliveryBranchKeepsDevBranch pins req 23/24/26: when a delivery's PR merges,
// Maintain deletes that delivery's per-delivery snapshot branch — but never the persistent dev branch,
// which is the only durable backup of built-but-unmerged work. A second, pathological delivery whose
// snapshot branch IS the dev branch name proves the exemption guard holds.
func TestMaintainPrunesMergedDeliveryBranchKeepsDevBranch(t *testing.T) {
	now := time.Now()
	prs := tempPRStore(t)
	if err := prs.Add(runs.PendingPR{Repo: "o/x", Number: 7, MergeBy: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	deliv := tempDeliveryStore(t)
	// The normal case: an open delivery whose PR is about to be observed merged.
	if err := deliv.Add(runs.Delivery{
		ID: "d1", Repo: "o/x", Branch: "mercury-run/r/d1", DevBranch: "mercury-dev",
		PRNumber: 7, Status: runs.DeliveryOpen, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	// The exemption case: an already-merged delivery whose snapshot branch is (pathologically) the dev
	// branch itself — it must NEVER be deleted.
	if err := deliv.Add(runs.Delivery{
		ID: "dDev", Repo: "o/x", Branch: "mercury-dev", DevBranch: "mercury-dev",
		PRNumber: 0, Status: runs.DeliveryMerged, CreatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	var deleted []string
	x := &runExecutor{
		s: &Server{runPRs: prs, deliveries: deliv}, mode: "full", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: true, State: "closed"}, nil
		},
		prodDeployFn:   func(context.Context, string, runs.PendingPR) (string, error) { return "ok", nil },
		deployTargetFn: func(string) bool { return true },
		deleteBranchFn: func(_ context.Context, _, _, branch string) error {
			deleted = append(deleted, branch)
			return nil
		},
	}

	x.Maintain(context.Background())

	// The merged delivery's snapshot branch is gone; the dev branch was never touched.
	sawDelivery, sawDev := false, false
	for _, b := range deleted {
		if b == "mercury-run/r/d1" {
			sawDelivery = true
		}
		if b == "mercury-dev" {
			sawDev = true
		}
	}
	if !sawDelivery {
		t.Errorf("the merged delivery branch must be deleted, deleted=%v", deleted)
	}
	if sawDev {
		t.Errorf("the persistent dev branch must NEVER be deleted, deleted=%v", deleted)
	}
	if d, _, _ := deliv.Get("d1"); !d.BranchPruned {
		t.Errorf("the pruned delivery must be flagged BranchPruned so it is not re-swept")
	}

	// A second pass must not re-delete an already-pruned branch (the flag gates it).
	deleted = nil
	x.Maintain(context.Background())
	for _, b := range deleted {
		if b == "mercury-run/r/d1" {
			t.Errorf("an already-pruned branch must not be deleted again, deleted=%v", deleted)
		}
	}
}
