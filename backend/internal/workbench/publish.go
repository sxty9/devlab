// Publish: the workbench is pushed as a backup WHILE it grows — publish after every commit
// (REQ-023.3, K-1), so an interruption costs at most the work since the last commit. It is
// never itself turned into a pull request (REQ-022).
package workbench

import (
	"context"
	"errors"
	"fmt"

	"devlab/backend/internal/workspace"
)

// publishAttempts bounds the push→fold→retry loop. The per-repo lock makes a racing pusher
// rare; two folds cover a remote that advanced twice mid-publish.
const publishAttempts = 3

// Publish pushes the workbench. A rejected push folds the remote in and retries — publish
// after every commit (REQ-023.3), so an abort after a commit means: already pushed. The
// push is a plain fast-forward, NEVER forced: force lives only behind the explicit reset
// (REQ-022.4). A fold conflict during the retry is named and returned — the local state
// stays exactly as it is, nothing is rolled back.
func (b *Bench) Publish(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < publishAttempts; attempt++ {
		if _, err := b.ex.PushRefs(ctx, b.repo, b.token, false, b.branch); err == nil {
			return nil
		} else {
			lastErr = err
		}
		// Rejected (or failed): bring the remote in and try again — fold, never reset.
		if err := b.ex.Fetch(ctx, b.repo, b.token); err != nil {
			return fmt.Errorf("publish %s: push failed (%v) and refetch failed: %w", b.branch, lastErr, err)
		}
		if !b.refExists(ctx, "refs/remotes/origin/"+b.branch) {
			continue // nothing to fold — the failure was not a remote advance; retry the push
		}
		if err := b.ex.FoldInBranch(ctx, b.repo, b.branch); err != nil {
			if errors.Is(err, workspace.ErrMergeConflict) {
				names := b.conflictNames(ctx, "refs/heads/"+b.branch, "refs/remotes/origin/"+b.branch)
				return fmt.Errorf("publish %s: push rejected and the pushed state does not fold in cleanly (conflicts: %s); local state kept unchanged", b.branch, nameList(names))
			}
			return fmt.Errorf("publish %s: fold before retry: %w", b.branch, err)
		}
	}
	return fmt.Errorf("publish %s: %w", b.branch, lastErr)
}
