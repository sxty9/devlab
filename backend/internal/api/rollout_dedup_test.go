package api

import (
	"testing"

	"devlab/backend/internal/github"
)

func rolloutPR(number int, ref string) github.PullRequest {
	pr := github.PullRequest{Number: number, State: "open", HTMLURL: "http://pr/x"}
	pr.Head.Ref = ref
	return pr
}

// TestRolloutPRSelection pins the one-open-rollout-PR-per-repo decision: the newest rollout PR is kept
// (reused + updated), the older rollout PRs are selected for closing, and a non-rollout PR is never
// touched. When none is open, nothing is kept (a fresh one will be created).
func TestRolloutPRSelection(t *testing.T) {
	t.Run("none open", func(t *testing.T) {
		keep, stale := rolloutPRSelection([]github.PullRequest{
			rolloutPR(9, "feature/x"), // a human PR, ignored
		})
		if keep != nil || len(stale) != 0 {
			t.Fatalf("no rollout PR open → keep nil, no stale; got keep=%v stale=%v", keep, stale)
		}
	})

	t.Run("several open — newest kept, older closed", func(t *testing.T) {
		open := []github.PullRequest{
			rolloutPR(11, rolloutBranchPrefix+"20260727-090000"),
			rolloutPR(7, rolloutBranchPrefix+"20260727-080000"),
			rolloutPR(20, "feature/unrelated"), // higher number but NOT a rollout PR — must be ignored
			rolloutPR(14, rolloutBranchPrefix+"20260727-093000"),
		}
		keep, stale := rolloutPRSelection(open)
		if keep == nil || keep.Number != 14 {
			t.Fatalf("newest rollout PR (#14) must be kept, got %v", keep)
		}
		if len(stale) != 2 {
			t.Fatalf("the two older rollout PRs must be stale, got %d", len(stale))
		}
		for _, pr := range stale {
			if pr.Number == 14 || pr.Number == 20 {
				t.Errorf("stale set must not contain the kept PR or a non-rollout PR, got #%d", pr.Number)
			}
		}
	})

	t.Run("exactly one open — kept, nothing stale (converged)", func(t *testing.T) {
		keep, stale := rolloutPRSelection([]github.PullRequest{
			rolloutPR(14, rolloutBranchPrefix+"20260727-093000"),
		})
		if keep == nil || keep.Number != 14 || len(stale) != 0 {
			t.Fatalf("a single rollout PR must be kept with no stale, got keep=%v stale=%v", keep, stale)
		}
	})
}
