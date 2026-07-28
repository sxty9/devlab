// Package deliver is the ONE PR path (S10; the merge guard is folded in, M-1*): pull-request
// creation, adoption, stacked bases, reversal (counter-booking), branch protection
// (ensure/verify/restore), the origin status, merge + prune, and the documented emergency
// override. In the WHOLE rebuild, github.CreatePullRequest has exactly ONE caller — here
// (K-6): the chain AND the IDE route both call this package.
package deliver

import (
	"context"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// OriginStatusContext is the one status-context constant of the delivery origin (REQ-033).
const OriginStatusContext = "devlab/delivery-origin"

// GitHubOps is the slice of the GitHub client this package needs — fixture-testable. The
// production implementation (NewGitHub, github.go) closes over the runner token; fixtures
// substitute the whole interface.
type GitHubOps interface {
	CreatePullRequest(ctx context.Context, repo, head, base, title, body string) (model.PRRef, error)
	FindOpenPRByHead(ctx context.Context, repo, head string) (*model.PRRef, error)
	MergePullRequest(ctx context.Context, repo string, number int, method string) error
	ClosePullRequest(ctx context.Context, repo string, number int, reason string) error
	DeleteBranch(ctx context.Context, repo, branch string) error
	CreateRepo(ctx context.Context, name string, private bool) error
	ProtectDefaultBranch(ctx context.Context, repo, requiredStatus string) error
	GetProtection(ctx context.Context, repo string) (Protection, error)
	PostCommitStatus(ctx context.Context, repo, sha, statusContext, state, desc string) error
	DefaultBranch(ctx context.Context, repo string) (string, error)
}

// Protection is the observed branch-protection state.
type Protection struct {
	RequirePR      bool
	RequiredStatus []string
	AllowForcePush bool
	AllowDeletion  bool
	MergeMethods   []string
}

// NextPRBase is pure: the stacked base of the next PR — the last open delivery's branch, else
// the default branch (REQ-024).
func NextPRBase(open []runs.Delivery, defaultBranch string) string {
	panic("TODO(B4)")
}

// PRIn describes the PR to open. DeliveryID "" means a HUMAN PR (the IDE route, B-1): no
// ledger entry is written — and REQ-033 keeps it from merging without one.
type PRIn struct {
	Repo, Head, Base, Title, Body string
	DeliveryID                    string
}

// OpenOrAdoptPR is the ONLY caller of github.CreatePullRequest in the entire rebuild (K-6
// grep). Order: head search (adoption, REQ-019.5) → ledger intent (with DeliveryID) → create →
// write back the PR number.
func OpenOrAdoptPR(ctx context.Context, gh GitHubOps, ledger *runs.DeliveryStore, in PRIn) (model.PRRef, bool, error) {
	panic("TODO(B4)")
}

// RollbackOutcome names what a counter-booking did and what follows from it.
type RollbackOutcome struct {
	ReversalDeliveryID string
	ClosedPR           *model.PRRef
	ReversalPR         *model.PRRef
	ConflictTodoID     string
	Detail             string
}

// Rollback counter-books one delivery (REQ-025): RevertRange idempotence; an open PR is closed
// with a reason; a merged one gets a reversal PR through the SAME chain; a build conflict is
// named and becomes an automatic todo; afterwards dev is delivered anew. Never a history
// rewrite.
func Rollback(ctx context.Context, gh GitHubOps, ledger *runs.DeliveryStore, rs *runs.Store, deliveryID string, by model.Actor) (RollbackOutcome, error) {
	panic("TODO(B4)")
}

// ProtectionReport names one repo's protection state and what was changed.
type ProtectionReport struct {
	Repo     string
	OK       bool
	Restored bool
	Detail   string
}

// EnsureProtection enforces the protection contract on a repo: PR requirement + required
// status OriginStatusContext + merge method "merge" only. On repo creation it runs in the SAME
// pass; a failure to set protection fails the creation; "repo already exists" is Satisfied
// (REQ-033.6).
func EnsureProtection(ctx context.Context, gh GitHubOps, repo string) (ProtectionReport, error) {
	panic("TODO(B4)")
}

// VerifyProtection re-checks protection on every repo, restores removed requirements and
// records the deviation (REQ-033.7).
func VerifyProtection(ctx context.Context, gh GitHubOps, repos []string, n *runs.NoticeStore) ([]ProtectionReport, error) {
	panic("TODO(B4)")
}

// PostOriginStatus posts the origin status derived SOLELY from the ledger; a rejection
// explains the admissible path (a todo in Mercury).
func PostOriginStatus(ctx context.Context, gh GitHubOps, ledger *runs.DeliveryStore, pr model.PRRef) error {
	panic("TODO(B4)")
}

// Maintain is the recurring PR maintenance: auto-merge after the window (method EXCLUSIVELY
// "merge"); the SAME place merges AND deletes the delivery branch (F14/REQ-026.4; branch 404 =
// Satisfied); sets Result.MergedAt per the B-8 rule; backoff/blockade at the PR record;
// mercury-dev NEVER falls under prune.
func Maintain(ctx context.Context, gh GitHubOps, prs *runs.PRStore, ledger *runs.DeliveryStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher) error {
	panic("TODO(B4)")
}

// AdminOverride records the emergency path: who, when, which PR, why — as a notice and in the
// daily report (REQ-033.4). It never performs the merge itself.
func AdminOverride(ctx context.Context, n *runs.NoticeStore, by model.Actor, pr model.PRRef, reason string) error {
	panic("TODO(B4)")
}
