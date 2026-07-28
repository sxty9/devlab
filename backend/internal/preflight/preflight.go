// Package preflight derives a task's state at a repo (K-3): collecting is impure (git, ledger,
// GitHub), deriving is pure. The state has NO storage location — it is observed fresh each time
// (TaskState "unknown" when a source is unreachable; never guessed).
package preflight

import (
	"context"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// Sources are the observation points preflight collects from — fixture-testable.
type Sources interface {
	// WorkbenchState reports whether the workbench branch is ahead of the default branch, and
	// its head commit.
	WorkbenchState(ctx context.Context, repo string) (aheadOfDefault bool, headCommit string, err error)
	// OpenDelivery returns the repo's open execution delivery from the ledger, nil when none.
	OpenDelivery(repo string) (*runs.Delivery, error)
	// OpenPRByHead returns the open PR with the given head branch, nil when none.
	OpenPRByHead(ctx context.Context, repo, head string) (*model.PRRef, error)
	// ContainedInDefault reports whether commit is contained in the default branch.
	ContainedInDefault(ctx context.Context, repo, commit string) (bool, error)
}

// Finding is the derived observation: the task state WITH its evidence (REQ-031.3). Err names
// an unreachable source (state unknown then).
type Finding struct {
	State      model.TaskState `json:"state"`
	Evidence   []string        `json:"evidence"`
	ObservedAt time.Time       `json:"observedAt"`
	OpenPR     *model.PRRef    `json:"openPr,omitempty"`
	Err        string          `json:"err,omitempty"`
}

// Derive observes one repo for one run: not-implemented | implemented-undelivered | delivered |
// unknown, each with evidence. Pure over what Sources report.
func Derive(ctx context.Context, src Sources, repo string, run runs.Run) (Finding, error) {
	panic("TODO(B3)")
}

// SyncStartupTodos is the startup reconciliation (REQ-039.2, B-5): every open todo whose work
// verifiably arrived in the default branch is checked off via a SYNTHETIC completed result
// (MergedAt set, authorship autonomous, one preflight stage "arrived in default branch
// @<commit>" with the evidence commit) plus a notice. GitHub unreachable ⇒ named deferral,
// never a guess. Returns how many todos it reconciled.
func SyncStartupTodos(ctx context.Context, src Sources, rs *runs.Store, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher) (int, error) {
	panic("TODO(B3)")
}
