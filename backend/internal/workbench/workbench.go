// Package workbench is the ONE fold-in state machine of the working state (S9, K-1): the
// persistent branch mercury-dev grows, is folded into, published — and NEVER reset by the
// machinery. There is no reset primitive on the automated path: Prepare folds the remote in
// (merge), a conflict is NAMED and nothing is rolled back, orphaned commits get a rescue ref.
// ResetToDefault exists ONLY behind the explicit UI confirmation (REQ-022.4).
//
// mercury-dev is pushed as a backup and is NEVER itself turned into a pull request.
//
// All git operations go through the ported workspace.Executor primitives — the runner works
// exclusively in the runner user's workspace (guard, B 1.3a).
package workbench

import (
	"context"

	"devlab/backend/internal/model"
	"devlab/backend/internal/workspace"
)

// Branch is the ONE constant naming the working-state branch.
const Branch = "mercury-dev"

// Bench operates the working state of one repo.
type Bench struct {
	ex   *workspace.Executor
	repo string
}

// New builds a bench. Guard: the runner works ONLY in the runner user's workspace (B 1.3a) —
// a foreign workspace path is refused.
func New(ex *workspace.Executor, repo string) (*Bench, error) {
	panic("TODO(B1)")
}

// PrepareResult reports what Prepare did — honestly, per dimension.
type PrepareResult struct {
	Created       bool
	FoldedRemote  bool
	FoldedDefault bool
	Conflicted    bool
	ConflictFiles []string
	Orphans       []Orphan
	Head          string
}

// Prepare makes the workbench current WITHOUT ever discarding local work: a locally present
// branch STAYS and the remote is folded IN (merge); otherwise the branch comes from the
// remote, else from the default branch. NEVER `checkout -f -B`, NEVER a reset onto origin
// (REQ-023.1); a conflict is named, nothing is rolled back, and the service log records it.
func (b *Bench) Prepare(ctx context.Context) (PrepareResult, error) {
	panic("TODO(B1)")
}

// FoldReport names what a default-branch fold-in brought.
type FoldReport struct {
	Folded        bool
	Conflicted    bool
	ConflictFiles []string
	Head          string
}

// FoldInDefault folds the default branch INTO the workbench — the workbench only ever grows
// (REQ-022.2).
func (b *Bench) FoldInDefault(ctx context.Context) (FoldReport, error) {
	panic("TODO(B1)")
}

// Publish pushes the workbench. A rejected push folds the remote in and retries — publish
// after every commit (REQ-023.3), so an abort after a commit means: already pushed.
func (b *Bench) Publish(ctx context.Context) error {
	panic("TODO(B1)")
}

// CleanUntracked removes ONLY untracked/uncommitted files — NEVER commits (REQ-023.4).
func (b *Bench) CleanUntracked(ctx context.Context) error {
	panic("TODO(B1)")
}

// Orphan is a commit that would otherwise be lost, secured under a rescue ref.
type Orphan struct {
	Commit    string
	RescueRef string // refs/devlab/rescue/<ts>-<short>
	Pushed    bool
}

// CollectOrphans secures orphaned commits: a rescue ref refs/devlab/rescue/<ts>-<short>, a
// best-effort push, and a notice (REQ-023.5).
func (b *Bench) CollectOrphans(ctx context.Context) ([]Orphan, error) {
	panic("TODO(B1)")
}

// ResetToDefault discards the workbench in favor of the default branch. Reachable ONLY through
// the explicit UI path with its confirmation (REQ-022.4) — never called by the machinery.
func (b *Bench) ResetToDefault(ctx context.Context, by model.Actor) error {
	panic("TODO(B1)")
}

// Head names the workbench's current commit — the nameable delivered state (REQ-022.3).
func (b *Bench) Head(ctx context.Context) (string, error) {
	panic("TODO(B1)")
}
