// Branch protection + origin status (REQ-033). Repo creation sets protection in the SAME pass
// (REQ-033.6); a failure to set it fails the creation. Protection: PR required, no force-push,
// no deletion, merge method "merge" only (squash/rebase off), required status = the delivery
// origin context.
package github

import (
	"context"
	"fmt"
	"net/http"
)

// Protection is the observed protection state of a default branch.
type Protection struct {
	RequirePR      bool
	RequiredStatus []string
	AllowForcePush bool
	AllowDeletion  bool
	MergeMethods   []string
}

// ProtectDefaultBranch enforces the protection contract, requiredStatus being the required
// commit-status context. It writes BOTH halves in one pass: the repository merge methods
// (merge commits on, squash and rebase OFF — the same place that protects also disarms the
// history-rewriting merges, REQ-033.5) and the default branch's protection (PR required, the
// origin status required, no force-pushes, no deletions).
func ProtectDefaultBranch(ctx context.Context, token, fullName, requiredStatus string) error {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return err
	}
	branch, err := DefaultBranch(ctx, token, fullName)
	if err != nil {
		return err
	}
	// Half 1: exactly one merge method (REQ-033.5).
	methods := map[string]any{
		"allow_merge_commit": true,
		"allow_squash_merge": false,
		"allow_rebase_merge": false,
	}
	if res, perr := doMethod(ctx, http.MethodPatch, token, apiBase+"/repos/"+owner+"/"+name, methods, nil); perr != nil {
		return typed(res, perr)
	}
	// Half 2: the branch protection carrying the delivery-origin status requirement.
	// enforce_admins stays false — that is the documented emergency path for humans with
	// admin rights; every use of it is recorded as an incident (REQ-033.4).
	protection := map[string]any{
		"required_status_checks": map[string]any{
			"strict":   false,
			"contexts": []string{requiredStatus},
		},
		"enforce_admins": false,
		"required_pull_request_reviews": map[string]any{
			"required_approving_review_count": 0,
		},
		"restrictions":            nil,
		"allow_force_pushes":      false,
		"allow_deletions":         false,
		"allow_fork_syncing":      false,
		"lock_branch":             false,
		"required_linear_history": false,
	}
	res, perr := doMethod(ctx, http.MethodPut, token,
		fmt.Sprintf("%s/repos/%s/%s/branches/%s/protection", apiBase, owner, name, branch), protection, nil)
	return typed(res, perr)
}

// protectionWire is the wire shape of GET …/branches/{branch}/protection.
type protectionWire struct {
	RequiredStatusChecks *struct {
		Contexts []string `json:"contexts"`
	} `json:"required_status_checks"`
	RequiredPullRequestReviews *struct{} `json:"required_pull_request_reviews"`
	AllowForcePushes           *struct {
		Enabled bool `json:"enabled"`
	} `json:"allow_force_pushes"`
	AllowDeletions *struct {
		Enabled bool `json:"enabled"`
	} `json:"allow_deletions"`
}

// GetProtection reads the protection state (typed errors for faultclass). An unprotected
// branch (GitHub answers 404 on the protection resource) is returned as the zero protection —
// a state to restore, not an error.
func GetProtection(ctx context.Context, token, fullName string) (Protection, error) {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return Protection{}, err
	}
	// The repository half: which merge methods are on.
	var repo struct {
		DefaultBranch    string `json:"default_branch"`
		AllowMergeCommit *bool  `json:"allow_merge_commit"`
		AllowSquashMerge *bool  `json:"allow_squash_merge"`
		AllowRebaseMerge *bool  `json:"allow_rebase_merge"`
	}
	if res, gerr := do(ctx, token, apiBase+"/repos/"+owner+"/"+name, &repo); gerr != nil {
		return Protection{}, typed(res, gerr)
	}
	p := Protection{}
	// GitHub omits the flags on some tokens; a missing flag defaults to GitHub's default (on).
	on := func(b *bool) bool { return b == nil || *b }
	if on(repo.AllowMergeCommit) {
		p.MergeMethods = append(p.MergeMethods, "merge")
	}
	if on(repo.AllowSquashMerge) {
		p.MergeMethods = append(p.MergeMethods, "squash")
	}
	if on(repo.AllowRebaseMerge) {
		p.MergeMethods = append(p.MergeMethods, "rebase")
	}
	// The branch half.
	var wire protectionWire
	res, gerr := do(ctx, token,
		fmt.Sprintf("%s/repos/%s/%s/branches/%s/protection", apiBase, owner, name, repo.DefaultBranch), &wire)
	if gerr != nil {
		if res != nil && res.StatusCode == http.StatusNotFound {
			return p, nil // unprotected — the zero branch half, honestly reported
		}
		return Protection{}, typed(res, gerr)
	}
	p.RequirePR = wire.RequiredPullRequestReviews != nil
	if wire.RequiredStatusChecks != nil {
		p.RequiredStatus = wire.RequiredStatusChecks.Contexts
	}
	p.AllowForcePush = wire.AllowForcePushes != nil && wire.AllowForcePushes.Enabled
	p.AllowDeletion = wire.AllowDeletions != nil && wire.AllowDeletions.Enabled
	return p, nil
}

// PostCommitStatus posts one commit status (context/state/description). state is one of
// "success" | "failure" | "error" | "pending"; desc is capped by GitHub at 140 characters.
func PostCommitStatus(ctx context.Context, token, fullName, sha, statusContext, state, desc string) error {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return err
	}
	if len(desc) > 140 {
		desc = desc[:137] + "..."
	}
	payload := map[string]any{"state": state, "context": statusContext, "description": desc}
	res, perr := doPost(ctx, token, fmt.Sprintf("%s/repos/%s/%s/statuses/%s", apiBase, owner, name, sha), payload, nil)
	return typed(res, perr)
}
