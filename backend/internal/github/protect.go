// Branch protection + origin status (REQ-033). Repo creation sets protection in the SAME pass
// (REQ-033.6); a failure to set it fails the creation. Protection: PR required, no force-push,
// no deletion, merge method "merge" only (squash/rebase off), required status = the delivery
// origin context.
package github

import "context"

// Protection is the observed protection state of a default branch.
type Protection struct {
	RequirePR      bool
	RequiredStatus []string
	AllowForcePush bool
	AllowDeletion  bool
	MergeMethods   []string
}

// ProtectDefaultBranch enforces the protection contract, requiredStatus being the required
// commit-status context.
func ProtectDefaultBranch(ctx context.Context, token, fullName, requiredStatus string) error {
	panic("TODO(B4)")
}

// GetProtection reads the protection state (typed errors for faultclass).
func GetProtection(ctx context.Context, token, fullName string) (Protection, error) {
	panic("TODO(B4)")
}

// PostCommitStatus posts one commit status (context/state/description).
func PostCommitStatus(ctx context.Context, token, fullName, sha, statusContext, state, desc string) error {
	panic("TODO(B4)")
}
