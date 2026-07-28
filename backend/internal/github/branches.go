// Branch operations (C F14 02e6c75). DeleteBranch is called by deliver.Maintain — the SAME
// place that merges also prunes the delivery branch; a 404 (branch already gone) is classified
// Satisfied by faultclass.
package github

import (
	"context"
	"fmt"
)

// StatusError is a typed GitHub API failure — the classification input for faultclass
// (404/403/422 ⇒ Permanent at the classifier; a 404 on a delete-like call is the caller's
// Satisfied).
type StatusError struct {
	Status int
	Msg    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: status %d: %s", e.Status, e.Msg)
}

// DeleteBranch deletes a branch ref. A missing branch surfaces as *StatusError{Status: 404}.
func DeleteBranch(ctx context.Context, token, fullName, branch string) error {
	panic("TODO(B4)")
}
