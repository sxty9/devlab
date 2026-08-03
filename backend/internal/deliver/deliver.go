// Package deliver is the ONE PR path (S10; the merge guard is folded in, M-1*): pull-request
// creation, adoption, stacked bases, reversal (counter-booking), branch protection
// (ensure/verify/restore), the origin status, merge + prune, and the documented emergency
// override. In the WHOLE rebuild, github.CreatePullRequest has exactly ONE caller — here
// (K-6): the chain AND the IDE route both call this package.
package deliver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/faultclass"
	"devlab/backend/internal/github"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workbench"
)

// OriginStatusContext is the one status-context constant of the delivery origin (REQ-033).
const OriginStatusContext = "devlab/delivery-origin"

// maintainMaxBackoff caps the growing interval between retries of a SELF-ENDING obstacle (a server
// hiccup, a dropped connection, a passing rate limit). Such an obstacle is never given up on — there
// is no attempt count that stills the record — but the wait between attempts grows toward this named
// ceiling and never past it, so a repository full of stuck deliveries cannot itself storm the shared
// request budget the way 67 entries did on 2026-07-31. A DURABLE obstacle (the pull request or repo
// is gone, the rights are missing, the request is invalid) is a different matter: it is stilled at
// once and waits for a person, since no repetition can change it.
const maintainMaxBackoff = time.Hour

// prunePrefix opens the fault reason recorded when the delivery-branch prune fails. It is a named
// constant because the startup revive sweep (ReviveStaleBlocks) matches against it to recognise a
// block that was only ever a prune whose branch had already vanished — a goal reached, not a fault.
const prunePrefix = "pruning the delivery branch failed: "

// readFailPrefix opens the fault reason recorded when READING a tracked pull request fails. It is a
// named constant because the maintenance loop matches against it to recognise a block that was only
// ever a read GitHub answered 404/410 — a vanished pull request, which is a reached goal, not a
// fault waiting on a person (blockIsReadGone).
const readFailPrefix = "reading the pull request failed: "

// The K-5 classification seams. They default to the ONE classification point (faultclass);
// they exist as variables solely so this package's tests stay deterministic while faultclass
// is filled in the same wave — production wiring never overrides them.
var (
	classify       = faultclass.Classify
	advanceBackoff = faultclass.Next
)

// GitHubOps is the slice of the GitHub client this package needs — fixture-testable. The
// production implementation (NewGitHub, github.go) closes over the runner token; fixtures
// substitute the whole interface.
type GitHubOps interface {
	CreatePullRequest(ctx context.Context, repo, head, base, title, body string) (model.PRRef, error)
	FindOpenPRByHead(ctx context.Context, repo, head string) (*model.PRRef, error)
	GetPullRequest(ctx context.Context, repo string, number int) (PRState, error)
	ListOpenPullRequests(ctx context.Context, repo string) ([]PRState, error)
	MergePullRequest(ctx context.Context, repo string, number int, method string) error
	ClosePullRequest(ctx context.Context, repo string, number int, reason string) error
	DeleteBranch(ctx context.Context, repo, branch string) error
	CreateRepo(ctx context.Context, name string, private bool) (fullName string, err error)
	ProtectDefaultBranch(ctx context.Context, repo, requiredStatus string) error
	GetProtection(ctx context.Context, repo string) (Protection, error)
	PostCommitStatus(ctx context.Context, repo, sha, statusContext, state, desc string) error
	DefaultBranch(ctx context.Context, repo string) (string, error)
	// RateBudget reports the last observed GitHub request budget of the token's hourly window, so
	// the maintenance can stop reading BEFORE the budget is spent. It makes no network call.
	RateBudget() RateBudget
}

// RateBudget is the maintenance view of the shared, finite request window: how much of the hourly
// budget is left and when it refills. It is a property of the TOKEN, not of any single pull request,
// which is why an empty budget is one shared standstill rather than a blockade on each entry.
type RateBudget struct {
	Known     bool      // false until a real response was observed (a cold start reads normally)
	Remaining int       // requests left in the current window
	Limit     int       // the window's size
	Reset     time.Time // when the window refills — the moment a standstill ends by itself
}

// Exhausted reports whether the budget is at or below the reserve floor and the window has not reset
// yet: the maintenance must stand down until Reset. A budget never observed (Known false) is NOT
// exhausted — the first read learns the true figure.
func (b RateBudget) Exhausted(now time.Time, floor int) bool {
	return b.Known && b.Remaining <= floor && now.Before(b.Reset)
}

// PRState is the maintenance view of one pull request: whether it is open, merged (and when),
// and where its head sits.
type PRState struct {
	Number   int
	State    string // "open" | "closed"
	Merged   bool
	MergedAt *time.Time
	HeadRef  string
	HeadSHA  string
}

// Protection is the observed branch-protection state.
type Protection struct {
	RequirePR      bool
	RequiredStatus []string
	AllowForcePush bool
	AllowDeletion  bool
	MergeMethods   []string
}

// NextPRBase is pure: the stacked base of the next PR — the most recent HEALTHY open delivery's
// branch OTHER THAN head (the branch being delivered), else the default branch (REQ-024).
//
// head is excluded INSIDE the function, never left to the caller. A PR's base is never its own
// head: the branch being delivered is regularly the newest open delivery — it was just written
// to the ledger — so a caller that only skipped the workbench branch would hand GitHub head==base
// and earn a 422 "No commits between X and X", a PR opened against itself. Every same-branch open
// record is skipped, so a re-run whose earlier delivery is still open stacks on somebody else's
// work, not on its own. The workbench branch is never a base candidate either: it is never a
// delivery branch (S9), and a defensive skip keeps a corrupt ledger from ever stacking on it.
//
// A FAILED delivery is never a base either: its work did not ship, so stacking a new PR on it
// would inherit an unfinished layer. The chain never reaches this point on a failed tip — the
// implement stage holds the run BEFORE it branches (FailedTip) — so skipping the failed record
// here is a defensive belt: were a base ever resolved past one, it would not pick the broken layer.
func NextPRBase(open []runs.Delivery, head, defaultBranch string) string {
	for i := len(open) - 1; i >= 0; i-- {
		d := open[i]
		if d.Branch != "" && d.Branch != head && d.Branch != workbench.LegacyShared && !d.Failed() {
			return d.Branch
		}
	}
	return defaultBranch
}

// FailedTip reports the failed delivery sitting at the tip of a repo's stack, or nil when the tip
// is sound. It is the ledger side of the axiom "a failed order becomes the tip and the stack does
// not grow past it": the NEWEST unsettled delivery of the repo is the tip, and a tip that is a
// failed delivery blocks any new branch until it is resolved or rolled back. A repo with no
// unsettled delivery, or whose newest unsettled delivery is a healthy open one, has a sound tip.
func FailedTip(ledger *runs.DeliveryStore, repo string) (*runs.Delivery, error) {
	all, err := ledger.All()
	if err != nil {
		return nil, err
	}
	// All() is oldest-first, so the last unsettled delivery of the repo is the tip.
	var tip *runs.Delivery
	for i := range all {
		if all[i].Repo == repo && all[i].OpenState() {
			d := all[i]
			tip = &d
		}
	}
	if tip != nil && tip.Failed() {
		return tip, nil
	}
	return nil, nil
}

// FailedDeliveryIn is the record a run leaves when it committed work but could not ship it. The
// DeliveryID is the one the chain already minted for the (attempted) delivery, so a re-run finds
// the same record instead of proliferating duplicates.
type FailedDeliveryIn struct {
	DeliveryID  string
	ExecutionID string
	Repo        string
	Branch      string
	FromCommit  string
	ToCommit    string
	Reason      string
}

// RecordFailedDelivery leaves the "Lieferung gescheitert" mark (WHAT-2.1): a durable, visible
// ledger entry for work that was committed but not shipped. Idempotent by DeliveryID — a re-run
// that fails again updates the same record. A delivery that has meanwhile been merged or closed is
// left untouched: the mark is only ever set on an unsettled record, never used to reopen a settled
// one.
func RecordFailedDelivery(ledger *runs.DeliveryStore, in FailedDeliveryIn) error {
	if ledger == nil {
		return errors.New("deliver: no ledger")
	}
	if in.DeliveryID == "" || in.Repo == "" {
		return errors.New("deliver: a failed-delivery mark needs its delivery id and repo")
	}
	now := time.Now().UTC()
	if d, ok, err := ledger.ByID(in.DeliveryID); err != nil {
		return err
	} else if ok {
		if !d.OpenState() {
			return nil // already merged or closed — the mark does not resurrect a settled delivery
		}
		d.FailedAt = &now
		d.FailedReason = in.Reason
		return ledger.Put(d)
	}
	return ledger.Put(runs.Delivery{
		ID: in.DeliveryID, ExecutionID: in.ExecutionID,
		Repo: in.Repo, Branch: in.Branch,
		FromCommit: in.FromCommit, ToCommit: in.ToCommit,
		CreatedAt: now, FailedAt: &now, FailedReason: in.Reason,
	})
}

// PRIn describes the PR to open. DeliveryID "" means a HUMAN PR (the IDE route, B-1): no
// ledger entry is written — and REQ-033 keeps it from merging without one.
type PRIn struct {
	Repo, Head, Base, Title, Body string
	DeliveryID                    string
}

// ErrDeliveryNotFound is returned when an operation names a delivery the ledger doesn't have.
var ErrDeliveryNotFound = errors.New("delivery not found")

// OpenOrAdoptPR is the ONLY caller of github.CreatePullRequest in the entire rebuild (K-6
// grep). Order: head search (adoption, REQ-019.5) → ledger intent (with DeliveryID) → create →
// write back the PR number. Afterwards the delivery-origin status is posted best-effort, so a
// ledger PR turns mergeable (and a human PR gets its explained rejection) without waiting for
// the next maintenance tick.
func OpenOrAdoptPR(ctx context.Context, gh GitHubOps, ledger *runs.DeliveryStore, in PRIn) (model.PRRef, bool, error) {
	if in.Repo == "" || in.Head == "" {
		return model.PRRef{}, false, errors.New("deliver: repo and head are required")
	}
	if in.Head == workbench.LegacyShared {
		// mercury-dev is pushed as a backup and is NEVER itself turned into a pull request (S9).
		return model.PRRef{}, false, fmt.Errorf("deliver: %s is never turned into a pull request", workbench.LegacyShared)
	}

	// 1. Head search — an open PR with the same head is ADOPTED, never duplicated (REQ-019.5).
	if existing, err := gh.FindOpenPRByHead(ctx, in.Repo, in.Head); err == nil && existing != nil {
		ref := *existing
		if ref.HeadBranch == "" {
			ref.HeadBranch = in.Head
		}
		if in.DeliveryID != "" {
			if err := recordPROnDelivery(ledger, in.DeliveryID, ref); err != nil {
				return ref, true, err
			}
		}
		postOriginStatusBestEffort(ctx, gh, ledger, in.Repo, ref.Number)
		return ref, true, nil
	}

	// 2. Ledger intent: a chain PR's delivery record exists BEFORE the PR (intent-before-effect).
	if in.DeliveryID != "" {
		if _, ok, err := ledger.ByID(in.DeliveryID); err != nil {
			return model.PRRef{}, false, err
		} else if !ok {
			return model.PRRef{}, false, fmt.Errorf("deliver: no ledger intent %s — the delivery must be recorded before its PR: %w", in.DeliveryID, ErrDeliveryNotFound)
		}
	}

	// 3. Create.
	base := in.Base
	if base == "" {
		var err error
		if base, err = gh.DefaultBranch(ctx, in.Repo); err != nil {
			return model.PRRef{}, false, err
		}
	}
	ref, err := gh.CreatePullRequest(ctx, in.Repo, in.Head, base, in.Title, in.Body)
	if err != nil {
		// A 422 race ("a PR for this head already exists") resolves by adoption — exactly one
		// named fallback attempt, never a loop (K-5).
		if adopted, ferr := gh.FindOpenPRByHead(ctx, in.Repo, in.Head); ferr == nil && adopted != nil {
			ref = *adopted
			if ref.HeadBranch == "" {
				ref.HeadBranch = in.Head
			}
			if in.DeliveryID != "" {
				if rerr := recordPROnDelivery(ledger, in.DeliveryID, ref); rerr != nil {
					return ref, true, rerr
				}
			}
			postOriginStatusBestEffort(ctx, gh, ledger, in.Repo, ref.Number)
			return ref, true, nil
		}
		return model.PRRef{}, false, err
	}
	if ref.HeadBranch == "" {
		ref.HeadBranch = in.Head
	}

	// 4. Write the PR number back onto the intent.
	if in.DeliveryID != "" {
		if err := recordPROnDelivery(ledger, in.DeliveryID, ref); err != nil {
			return ref, false, err
		}
	}
	postOriginStatusBestEffort(ctx, gh, ledger, in.Repo, ref.Number)
	return ref, false, nil
}

// recordPROnDelivery mirrors an opened/adopted PR onto its ledger intent.
func recordPROnDelivery(ledger *runs.DeliveryStore, deliveryID string, ref model.PRRef) error {
	if ledger == nil {
		return errors.New("deliver: no ledger")
	}
	d, ok, err := ledger.ByID(deliveryID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("deliver: ledger intent %s vanished: %w", deliveryID, ErrDeliveryNotFound)
	}
	d.PRNumber = ref.Number
	d.PRURL = ref.URL
	// The delivery just shipped a pull request — it is a healthy open delivery again. A "Lieferung
	// gescheitert" mark from an earlier failed attempt is cleared here, at the ONE point a delivery
	// recovers, so resolving the failure at the tip (re-running the order) needs no separate step.
	d.FailedAt = nil
	d.FailedReason = ""
	return ledger.Put(d)
}

// CreateProtectedRepo is the ONE way the system creates a repository (REQ-006.2/REQ-033.6):
// creation and protection happen in the SAME pass. An already-existing repo is Satisfied; a
// failure to set the protection fails the creation.
func CreateProtectedRepo(ctx context.Context, gh GitHubOps, name string) (string, error) {
	full, err := gh.CreateRepo(ctx, name, true)
	if err != nil {
		return "", err
	}
	if _, err := EnsureProtection(ctx, gh, full); err != nil {
		return "", fmt.Errorf("deliver: repository %s was created but protecting it failed — the creation counts as failed: %w", full, err)
	}
	return full, nil
}

// ── Rollback (counter-booking, REQ-025) ──────────────────────────────────────────────────

// CounterBookResult is what the git side of a rollback reports back: whether the reverse
// conflicted, whether it changed anything (false = the effect was already gone, an idempotent
// no-op), the workbench tip before/after the counter-booking commit, and the default branch.
type CounterBookResult struct {
	Conflicted    bool
	ConflictFiles []string
	Changed       bool
	Before, After string
	DefaultBranch string
}

// GitSide is the git half of a rollback — revert the delivery's range as ONE counter-booking
// commit on the workbench (never a history rewrite), snapshot the reversal branch when given,
// push, and afterwards re-deliver the dev state. The caller wires the workbench-backed
// implementation (the API handler); fixtures substitute it. It is discovered on the GitHubOps
// value via interface upgrade so the frozen Rollback signature stays untouched.
type GitSide interface {
	CounterBook(ctx context.Context, d runs.Delivery, reversalBranch string) (CounterBookResult, error)
	RedeliverDev(ctx context.Context, repo string) error
}

// RollbackOutcome names what a counter-booking did and what follows from it.
type RollbackOutcome struct {
	ReversalDeliveryID string
	ClosedPR           *model.PRRef
	ReversalPR         *model.PRRef
	ConflictTodoID     string
	Detail             string
}

// reversalDeliveryID derives the DETERMINISTIC id of a delivery's reversal, so a retried
// rollback reuses the same record instead of proliferating duplicates.
func reversalDeliveryID(deliveryID string) string {
	return "dlv_rev_" + strings.TrimPrefix(deliveryID, "dlv_")
}

// reversalBranchFor derives the DETERMINISTIC reversal branch in the uniform naming form
// (REQ-026): fix/revert_<description>-<delivery-suffix>.
func reversalBranchFor(d runs.Delivery) string {
	desc := d.Branch
	if i := strings.IndexByte(desc, '/'); i >= 0 {
		desc = desc[i+1:]
	}
	if desc == "" {
		desc = d.ID
	}
	return runs.BranchName(runs.BranchKindFix, "revert_"+desc, strings.TrimPrefix(d.ID, "dlv_"))
}

// Rollback counter-books one delivery (REQ-025): RevertRange idempotence; an open PR is closed
// with a reason; a merged one gets a reversal PR through the SAME chain; a build conflict is
// named and becomes an automatic todo; afterwards dev is delivered anew. Never a history
// rewrite.
func Rollback(ctx context.Context, gh GitHubOps, ledger *runs.DeliveryStore, rs *runs.Store, deliveryID string, by model.Actor) (RollbackOutcome, error) {
	out := RollbackOutcome{}
	d, ok, err := ledger.ByID(deliveryID)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, ErrDeliveryNotFound
	}

	// Idempotence: an existing reversal means this delivery IS rolled back — report it, do
	// nothing twice.
	all, err := ledger.All()
	if err != nil {
		return out, err
	}
	for _, r := range all {
		if r.ReversalOf == d.ID {
			out.ReversalDeliveryID = r.ID
			out.Detail = "already rolled back — reversal " + r.ID + " exists"
			return out, nil
		}
	}
	if d.ClosedAt != nil && strings.HasPrefix(d.ClosedReason, rolledBackReasonPrefix) {
		out.Detail = "already rolled back — " + d.ClosedReason
		return out, nil
	}

	gs, ok := gh.(GitSide)
	if !ok {
		return out, errors.New("deliver: no git side wired for counter-booking")
	}

	// Authoritative merged state: the ledger mirror is the fallback, GitHub the truth (a merge
	// can land between maintenance ticks).
	merged := d.MergedAt != nil
	if d.PRNumber != 0 {
		if st, gerr := gh.GetPullRequest(ctx, d.Repo, d.PRNumber); gerr == nil {
			merged = st.Merged
		}
	}

	reversalBranch := ""
	if merged {
		reversalBranch = reversalBranchFor(d)
	}

	cb, err := gs.CounterBook(ctx, d, reversalBranch)
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()

	if cb.Conflicted {
		// Make no guess (REQ-025.4): later work built on this delivery and the reverse does not
		// apply cleanly. Raise a concrete ToDo that counter-books by hand; nothing is discarded.
		later := laterOpenDeliveries(all, d)
		todo := buildRollbackTodo(d, later, cb.ConflictFiles, by, now)
		if rs == nil {
			return out, errors.New("deliver: rollback conflicts but no run store to raise the todo in")
		}
		if err := rs.Put(todo); err != nil {
			return out, err
		}
		out.ConflictTodoID = todo.ID
		out.Detail = fmt.Sprintf("counter-booking conflicts with %d later open delivery/ies — raised todo %s instead of guessing", len(later), todo.ID)
		return out, nil
	}

	if !cb.Changed {
		// The delivery's effect is already absent (a repeated rollback, or later work removed
		// it). Close the open PR anyway, mark the record — an idempotent no-op otherwise.
		if !merged && d.PRNumber != 0 {
			if err := gh.ClosePullRequest(ctx, d.Repo, d.PRNumber, rollbackCloseReason(d, by)); err != nil {
				return out, fmt.Errorf("close PR: %w", err)
			}
			out.ClosedPR = &model.PRRef{Number: d.PRNumber, URL: d.PRURL, HeadBranch: d.Branch}
		}
		if !merged {
			d.ClosedAt = &now
			d.ClosedReason = rolledBackReasonPrefix + actorName(by) + " — effect was already absent"
			if err := ledger.Put(d); err != nil {
				return out, err
			}
		}
		out.Detail = "nothing to counter-book — the delivery's effect is already absent"
		redeliver(ctx, gs, d.Repo, &out)
		return out, nil
	}

	if merged {
		// The reversal is itself a delivery with a stacked PR through the SAME chain (REQ-025.3):
		// intent BEFORE the PR, base = the last open delivery, else the default branch.
		revID := reversalDeliveryID(d.ID)
		reversal := runs.Delivery{
			ID: revID, Repo: d.Repo, Branch: reversalBranch,
			FromCommit: cb.Before, ToCommit: cb.After,
			CreatedAt: now, ReversalOf: d.ID,
		}
		if err := ledger.Put(reversal); err != nil {
			return out, err
		}
		open, err := ledger.Open(d.Repo)
		if err != nil {
			return out, err
		}
		// The reversal itself is open now; NextPRBase excludes the branch being delivered
		// (reversalBranch), so its base stacks on what came BEFORE it — never on itself.
		base := NextPRBase(open, reversalBranch, cb.DefaultBranch)
		ref, _, err := OpenOrAdoptPR(ctx, gh, ledger, PRIn{
			Repo: d.Repo, Head: reversalBranch, Base: base,
			Title:      "Revert delivery " + d.ID,
			Body:       reversalPRBody(d, by),
			DeliveryID: revID,
		})
		if err != nil {
			return out, fmt.Errorf("reversal PR: %w", err)
		}
		out.ReversalPR = &ref
		out.ReversalDeliveryID = revID
		out.Detail = "merged delivery counter-booked — reversal PR #" + fmt.Sprint(ref.Number) + " runs the same chain"
	} else {
		// Both an OPEN and a FAILED delivery take this path — neither is merged, so the same
		// counter-booking withdraws the work from the dev branch. A failed delivery regularly has no
		// pull request to close; the close is conditional on one existing, so the wording never
		// claims a PR that was never opened. Rolling back a failed tip is the axiom's second way back:
		// the broken layer is dismantled and the last sound layer becomes the tip again.
		failed := d.Failed()
		if d.PRNumber != 0 {
			if err := gh.ClosePullRequest(ctx, d.Repo, d.PRNumber, rollbackCloseReason(d, by)); err != nil {
				return out, fmt.Errorf("close PR: %w", err)
			}
			out.ClosedPR = &model.PRRef{Number: d.PRNumber, URL: d.PRURL, HeadBranch: d.Branch}
		}
		d.ClosedAt = &now
		reasonWhat := "counter-booked"
		if failed {
			reasonWhat = "failed delivery rolled back — counter-booked"
		}
		d.ClosedReason = rolledBackReasonPrefix + actorName(by) + " — " + reasonWhat + " on " + workbench.LegacyShared + "@" + shortSHA(cb.After)
		// A failed mark is subsumed by the close: the delivery is now settled (rolled back), not failed.
		d.FailedAt, d.FailedReason = nil, ""
		if err := ledger.Put(d); err != nil {
			return out, err
		}
		if out.Detail == "" {
			switch {
			case failed && out.ClosedPR != nil:
				out.Detail = "failed delivery rolled back — its work counter-booked off the dev branch and its pull request closed"
			case failed:
				out.Detail = "failed delivery rolled back — its work counter-booked off the dev branch"
			default:
				out.Detail = "open delivery counter-booked and its PR closed with the justification"
			}
		}
	}
	redeliver(ctx, gs, d.Repo, &out)
	return out, nil
}

const rolledBackReasonPrefix = "rolled back by "

// redeliver re-delivers the dev state after a clean counter-booking (REQ-025.5) — best-effort:
// a failure is named in the outcome, never silent, and never undoes the counter-booking.
func redeliver(ctx context.Context, gs GitSide, repo string, out *RollbackOutcome) {
	if err := gs.RedeliverDev(ctx, repo); err != nil {
		if out.Detail != "" {
			out.Detail += "; "
		}
		out.Detail += "dev re-delivery pending: " + err.Error()
	}
}

// laterOpenDeliveries lists the OPEN deliveries of d's repo created after d — the "later work"
// a conflicting rollback names.
func laterOpenDeliveries(all []runs.Delivery, d runs.Delivery) []runs.Delivery {
	var out []runs.Delivery
	for _, o := range all {
		if o.Repo == d.Repo && o.ID != d.ID && o.OpenState() && o.CreatedAt.After(d.CreatedAt) {
			out = append(out, o)
		}
	}
	return out
}

// buildRollbackTodo raises the concrete ToDo a conflicting rollback leaves behind (REQ-025.4):
// it targets the repo, names the delivery and the later work, and forbids history rewriting.
func buildRollbackTodo(d runs.Delivery, later []runs.Delivery, conflictFiles []string, by model.Actor, now time.Time) runs.Run {
	var b strings.Builder
	fmt.Fprintf(&b, "Counter-book delivery %s (%s, branch %s, commits %s..%s) by hand.\n\n",
		d.ID, d.Repo, d.Branch, shortSHA(d.FromCommit), shortSHA(d.ToCommit))
	b.WriteString("The automatic reversal conflicts with later work that builds on this delivery:\n")
	for _, l := range later {
		fmt.Fprintf(&b, "- delivery %s (branch %s, commits %s..%s)\n", l.ID, l.Branch, shortSHA(l.FromCommit), shortSHA(l.ToCommit))
	}
	if len(later) == 0 {
		b.WriteString("- (no later open delivery recorded; the conflict came from the working state)\n")
	}
	if len(conflictFiles) > 0 {
		b.WriteString("\nConflicting files:\n")
		for _, f := range conflictFiles {
			b.WriteString("- " + f + "\n")
		}
	}
	b.WriteString("\nRevert the delivery's effect as ONE new counter-booking commit that keeps the later work intact. Do not rewrite history and do not force-push any branch.")
	actor := model.Actor{User: by.User, Autonomous: true, OnBehalfOf: by.User}
	return runs.Run{
		ID:      runs.NewID(),
		Kind:    model.KindTodo,
		Title:   "Counter-book delivery " + d.ID + " by hand",
		Task:    b.String(),
		Targets: []runs.Target{{Repo: repoShortName(d.Repo)}},
		DueAt:   &now,
		Authorship: model.Authorship{
			Created: actor, CreatedAt: now,
			Updated: actor, UpdatedAt: now,
		},
	}
}

// rollbackCloseReason is the justification a rolled-back delivery's open PR is closed with.
func rollbackCloseReason(d runs.Delivery, by model.Actor) string {
	return "Delivery " + d.ID + " was rolled back by " + actorName(by) +
		": its changes were withdrawn from " + workbench.LegacyShared + " as a counter-booking commit. " +
		"This pull request is closed because the work it proposes is no longer part of the dev state."
}

// reversalPRBody is the description of a reversal PR.
func reversalPRBody(d runs.Delivery, by model.Actor) string {
	return "Counter-booking of delivery " + d.ID + " (" + d.Repo + ", " + shortSHA(d.FromCommit) + ".." + shortSHA(d.ToCommit) + "), " +
		"requested by " + actorName(by) + ". The delivery was already merged, so this reversal runs the same delivery chain: " +
		"one reverting commit, no history rewrite."
}

func actorName(a model.Actor) string {
	switch {
	case a.User != "":
		return a.User
	case a.Autonomous:
		return "the autonomous system"
	default:
		return "unknown"
	}
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// repoShortName reduces owner/name to the bare repo name run targets are keyed by.
func repoShortName(fullName string) string {
	if i := strings.LastIndexByte(fullName, '/'); i >= 0 {
		return fullName[i+1:]
	}
	return fullName
}

// ── Branch protection (REQ-033) ──────────────────────────────────────────────────────────

// ProtectionReport names one repo's protection state and what was changed.
type ProtectionReport struct {
	Repo     string
	OK       bool
	Restored bool
	Detail   string
}

// protectionSatisfied reports whether p carries the full contract.
func protectionSatisfied(p Protection) (ok bool, missing []string) {
	if !p.RequirePR {
		missing = append(missing, "pull requests are not required")
	}
	hasStatus := false
	for _, c := range p.RequiredStatus {
		if c == OriginStatusContext {
			hasStatus = true
		}
	}
	if !hasStatus {
		missing = append(missing, "the delivery-origin status is not required")
	}
	if p.AllowForcePush {
		missing = append(missing, "force-pushes are allowed")
	}
	if p.AllowDeletion {
		missing = append(missing, "branch deletion is allowed")
	}
	if len(p.MergeMethods) != 1 || p.MergeMethods[0] != "merge" {
		missing = append(missing, fmt.Sprintf("merge methods are %v, want exactly [merge]", p.MergeMethods))
	}
	return len(missing) == 0, missing
}

// EnsureProtection enforces the protection contract on a repo: PR requirement + required
// status OriginStatusContext + merge method "merge" only. On repo creation it runs in the SAME
// pass; a failure to set protection fails the creation; "repo already exists" is Satisfied
// (REQ-033.6).
func EnsureProtection(ctx context.Context, gh GitHubOps, repo string) (ProtectionReport, error) {
	rep := ProtectionReport{Repo: repo}
	if cur, err := gh.GetProtection(ctx, repo); err == nil {
		if ok, _ := protectionSatisfied(cur); ok {
			rep.OK = true
			rep.Detail = "protection already satisfied"
			return rep, nil
		}
	}
	if err := gh.ProtectDefaultBranch(ctx, repo, OriginStatusContext); err != nil {
		rep.Detail = "setting protection failed: " + err.Error()
		return rep, err
	}
	rep.OK = true
	rep.Restored = true
	rep.Detail = "protection written"
	return rep, nil
}

// VerifyProtection re-checks protection on every repo, restores removed requirements and
// records the deviation (REQ-033.7). The findings surface in the notices and hence the daily
// report; the function itself only reports, never judges.
func VerifyProtection(ctx context.Context, gh GitHubOps, repos []string, n *runs.NoticeStore) ([]ProtectionReport, error) {
	var out []ProtectionReport
	var firstErr error
	for _, repo := range repos {
		cur, err := gh.GetProtection(ctx, repo)
		if err != nil {
			// An UNREADABLE protection is a FINDING, not a quiet skip: the pass cannot say whether
			// this repository is protected, and that is exactly the state a drift hides in. Two
			// repositories kept requiring a status context nobody writes any more; the pass met
			// them, could not read them, wrote a line into a report nobody reads and stayed silent
			// (measured 2026-07-30: 403 "Upgrade to GitHub Pro or make this repository public").
			out = append(out, ProtectionReport{Repo: repo, Detail: "protection unreadable: " + err.Error()})
			notify(n, "protection-deviation", repo, "branch protection could not be READ, so whether this repository is protected is UNKNOWN: "+err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		ok, missing := protectionSatisfied(cur)
		if ok {
			out = append(out, ProtectionReport{Repo: repo, OK: true, Detail: "protection satisfied"})
			continue
		}
		deviation := strings.Join(missing, "; ")
		if err := gh.ProtectDefaultBranch(ctx, repo, OriginStatusContext); err != nil {
			out = append(out, ProtectionReport{Repo: repo, Detail: "deviation found (" + deviation + ") but restoring failed: " + err.Error()})
			notify(n, "protection-deviation", repo, "branch protection deviated ("+deviation+") and restoring it FAILED: "+err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, ProtectionReport{Repo: repo, OK: true, Restored: true, Detail: "deviation restored: " + deviation})
		notify(n, "protection-deviation", repo, "branch protection deviated ("+deviation+") and was restored")
	}
	return out, firstErr
}

// ── Origin status (REQ-033) ──────────────────────────────────────────────────────────────

// originVerdict derives, SOLELY from the ledger, whether a PR head is an admissible delivery:
// its head branch must be a recorded delivery of the repo AND the head commit must be exactly
// the recorded end of that delivery's span.
func originVerdict(ledger *runs.DeliveryStore, repo, headRef, headSHA string) (ok bool, desc string, err error) {
	all, err := ledger.All()
	if err != nil {
		return false, "", err
	}
	branchKnown := false
	for _, d := range all {
		if d.Repo != repo || d.Branch != headRef {
			continue
		}
		branchKnown = true
		if d.ToCommit == headSHA {
			return true, "recorded delivery " + d.ID + " (" + shortSHA(d.FromCommit) + ".." + shortSHA(d.ToCommit) + ")", nil
		}
	}
	if branchKnown {
		return false, "head moved past the recorded delivery span — deliver through the chain (raise a todo in Mercury)", nil
	}
	return false, "not from a recorded delivery — deliver through the chain (raise a todo in Mercury)", nil
}

// PostOriginStatus posts the origin status derived SOLELY from the ledger; a rejection
// explains the admissible path (a todo in Mercury). The PR's repo is resolved from its URL
// (…/owner/repo/pull/N), with the ledger's branch record as the fallback.
func PostOriginStatus(ctx context.Context, gh GitHubOps, ledger *runs.DeliveryStore, pr model.PRRef) error {
	repo := repoFromPRURL(pr.URL)
	if repo == "" && pr.HeadBranch != "" {
		if all, err := ledger.All(); err == nil {
			for _, d := range all {
				if d.Branch == pr.HeadBranch {
					repo = d.Repo
					break
				}
			}
		}
	}
	if repo == "" {
		return fmt.Errorf("deliver: cannot resolve the repository of PR #%d (%s)", pr.Number, pr.URL)
	}
	return PostOriginStatusFor(ctx, gh, ledger, repo, pr.Number)
}

// repoFromPRURL extracts "owner/repo" from a canonical PR URL (…/{owner}/{repo}/pull/{n}).
func repoFromPRURL(url string) string {
	parts := strings.Split(strings.Trim(url, "/"), "/")
	for i, p := range parts {
		if p == "pull" && i >= 2 {
			return parts[i-2] + "/" + parts[i-1]
		}
	}
	return ""
}

// PostOriginStatusFor posts the origin status of one PR of one repo — the repo-scoped form
// this package's own call sites use.
func PostOriginStatusFor(ctx context.Context, gh GitHubOps, ledger *runs.DeliveryStore, repo string, number int) error {
	st, err := gh.GetPullRequest(ctx, repo, number)
	if err != nil {
		return err
	}
	ok, desc, err := originVerdict(ledger, repo, st.HeadRef, st.HeadSHA)
	if err != nil {
		return err
	}
	state := "failure"
	if ok {
		state = "success"
	}
	return gh.PostCommitStatus(ctx, repo, st.HeadSHA, OriginStatusContext, state, desc)
}

// postOriginStatusBestEffort is the fire-and-forget form used right after opening/adopting.
func postOriginStatusBestEffort(ctx context.Context, gh GitHubOps, ledger *runs.DeliveryStore, repo string, number int) {
	if ledger == nil {
		return
	}
	_ = PostOriginStatusFor(ctx, gh, ledger, repo, number)
}

// ── Maintenance (auto-merge + prune + status, F14/REQ-026.4/B-8) ─────────────────────────

// EnvMaintainEnforce names the switch that ARMS the writing half of Maintain — a named handover by
// the operator, never a default.
//
// FOREIGN EFFECT, HELD BY DEFAULT: this maintenance merges pull requests, deletes their branches and
// writes commit statuses in OTHER organisations' repositories. On the first start of a rebuilt
// instance it would do all of that from the very first scheduler tick, over a pool that was imported
// minutes earlier and that nobody has looked at — the one effect no restart may cause on its own. So
// it is held: unarmed, the pass establishes the situation and REPORTS it, and writes nothing at all.
// The switch is read here, inside the one function that performs the effect, so every caller — the
// scheduler tick, the maintenance route, a test harness — is covered by the same hold.
const EnvMaintainEnforce = "DEVLAB_RUNS_MAINTAIN_ENFORCE"

// ArmedByEnv reads an arming switch: a foreign-effect pass writes only where the operator has
// affirmatively armed it. ONE parser for every such switch — the protection hold reads the same word
// set — so two holds can never disagree about what "armed" means, and the runbook has one answer.
func ArmedByEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// MaintainArmed reports whether the operator armed the writing half of the maintenance.
func MaintainArmed() bool { return ArmedByEnv(EnvMaintainEnforce) }

// maintainHeldText is the standstill as the feed states it. It carries NO counts on purpose: an
// unchanged message bundles into ONE record with a count and a period (REQ-032.5), while a text that
// moved with every tick would file a new row every tick. What is waiting is visible where it lives —
// the delivery ledger — and this sentence says why none of it moves.
const maintainHeldText = "pull-request maintenance is HELD: nothing is merged, no delivery branch " +
	"is pruned and no delivery-origin status is written until the operator arms it — set " +
	EnvMaintainEnforce + "=1. Until then every tracked pull request stays exactly as it is; the " +
	"first armed pass then merges what is due, in creation order."

// reportMaintainStandstill is the UNARMED pass: it establishes the situation from DevLab's OWN pools
// and reports it — no foreign call is made at all, so it needs neither token nor network, and no
// record of DevLab's own is changed either. A pass that mirrored half its findings and held back the
// other half would leave a mixed state nobody can reason about; a standstill is a standstill. The
// report is raised only while something is actually waiting: with an empty pool there is nothing to
// explain and the feed stays silent.
func reportMaintainStandstill(prs *runs.PRStore, ledger *runs.DeliveryStore, n *runs.NoticeStore) error {
	tracked, err := prs.List()
	if err != nil {
		return err
	}
	waiting := len(tracked)
	if all, lerr := ledger.All(); lerr == nil {
		for _, d := range all {
			if d.OpenState() {
				waiting++
			}
		}
	}
	if waiting == 0 {
		return nil
	}
	notify(n, runs.NoticeDeliveryHeld, "", maintainHeldText)
	return nil
}

// The maintenance reads GitHub sparingly. Three reasons bound the read count, and each names why
// it holds:
//
//   - rateFloor is the reserve the pass keeps UNUSED. It stops reading once fewer than this many
//     requests remain in the hourly window, so the budget is never discovered empty by a rejected
//     request, and enough is left for the chain's own writes (pushes, PR creation, protection).
//   - recheckMin/recheckMax bound the per-pull-request recheck cadence, and recheckDivisor DERIVES it
//     from the merge deadline: a pull request due in T is re-read about once every T/recheckDivisor,
//     clamped to [recheckMin, recheckMax]. A deadline weeks away therefore yields a coarse cadence
//     (capped at recheckMax) and a near one a fine cadence (floored at recheckMin) — the frequency
//     follows the deadline, never a flat number, and a not-yet-due pull request is not read every tick.
const (
	rateFloor      = 200
	recheckMin     = 2 * time.Minute
	recheckMax     = 30 * time.Minute
	recheckDivisor = 4
)

// recheckInterval derives, FROM the merge deadline, how often a not-yet-due pull request is re-read
// to notice a human merge or close: about once every (time-until-due)/recheckDivisor, clamped to
// [recheckMin, recheckMax].
func recheckInterval(mergeBy, now time.Time) time.Duration {
	remaining := mergeBy.Sub(now)
	if remaining <= 0 {
		return 0
	}
	iv := remaining / recheckDivisor
	if iv < recheckMin {
		return recheckMin
	}
	if iv > recheckMax {
		return recheckMax
	}
	return iv
}

// dueForRead reports whether a tracked pull request warrants a GitHub read THIS pass. A due,
// un-gated pull request is read — it is about to be merged and needs its authoritative state. Any
// other is read only once its deadline-derived recheck interval has elapsed since the last read;
// until then a GitHub read cannot change the outcome, so it is skipped. This is the single point
// that turns "N reads every tick" into "N reads at most once per recheck interval".
func dueForRead(p runs.PendingPR, gated bool, now time.Time) bool {
	if !now.Before(p.MergeBy) && !gated {
		return true
	}
	return now.Sub(p.LastChecked) >= recheckInterval(p.MergeBy, now)
}

// maintainQuotaText states the shared budget standstill. The reset time is stable for the whole
// window, so the message coalesces into ONE record per exhaustion episode instead of a row per pass.
func maintainQuotaText(reset time.Time) string {
	when := "as soon as GitHub refills it"
	if !reset.IsZero() {
		when = "at " + reset.UTC().Format(time.RFC3339)
	}
	return "GitHub's hourly request budget is exhausted, so pull-request maintenance pauses and " +
		"resumes by itself " + when + ". No pull request is blocked — an empty budget is a property " +
		"of the shared request window, not of any single pull request."
}

// Maintain is the recurring PR maintenance: auto-merge after the window (method EXCLUSIVELY
// "merge"); the SAME place merges AND deletes the delivery branch (F14/REQ-026.4; branch 404 =
// Satisfied); sets Result.MergedAt per the B-8 rule; backoff/blockade at the PR record;
// mercury-dev NEVER falls under prune. It also re-posts the delivery-origin status on every
// open PR of the repos it manages, so a hand-raised PR carries its explained rejection.
//
// Every line below writes into foreign repositories, so the whole pass is behind the hold above.
//
// gh may be nil, and only while the hold stands: the held pass makes no foreign call, so it needs
// no identity — which is what lets the first, deliberately identity-less start still SAY that the
// maintenance stands still. Armed without ops is refused by name one line below the hold, so the
// tolerance can never reach a writing line.
func Maintain(ctx context.Context, gh GitHubOps, prs *runs.PRStore, ledger *runs.DeliveryStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher) error {
	if prs == nil || ledger == nil {
		return errors.New("deliver: maintain needs the PR pool and the ledger")
	}
	if !MaintainArmed() {
		return reportMaintainStandstill(prs, ledger, n)
	}
	if gh == nil {
		return errors.New("deliver: the armed maintenance writes into foreign repositories and has no GitHub identity to do it with")
	}
	now := time.Now().UTC()

	// The shared, self-ending budget standstill: if the last observed budget is at or below the
	// reserve and the window has not reset, the WHOLE pass stands down before spending a request. An
	// empty budget is a property of the request window shared by all pull requests, so it is one
	// standstill, not a blockade filed on each of 78 entries — and it ends by itself at Reset.
	if budget := gh.RateBudget(); budget.Exhausted(now, rateFloor) {
		notify(n, runs.NoticeGitHubQuota, "", maintainQuotaText(budget.Reset))
		return nil
	}

	tracked, err := prs.List()
	if err != nil {
		return err
	}

	// Auto-merge strictly in creation order per repo: an older open PR gates the younger ones
	// (the stack collapses front to back), whatever the individual outcome.
	sort.SliceStable(tracked, func(i, j int) bool { return tracked[i].CreatedAt.Before(tracked[j].CreatedAt) })
	queueBlocked := map[string]bool{}
	repos := map[string]bool{}

	var firstErr error
	for _, p := range tracked {
		repos[p.Repo] = true
		if p.Blocked {
			// A block that was only ever a READ GitHub answered 404/410 is stale: the pull request is
			// gone, which is a reached goal, not a fault waiting on a person. Untrack it here so a
			// pre-existing block of this cause dissolves on the next maintenance run — no restart and no
			// human release needed. Any other block still waits for an explicit resume.
			if blockIsReadGone(p.BlockedReason) {
				untrackVanished(ledger, prs, res, n, pub, p, now)
				continue
			}
			// F4: an honestly blocked record waits for an explicit resume — and gates its repo.
			queueBlocked[p.Repo] = true
			continue
		}
		if p.Backoff != nil && now.Before(p.Backoff.NextAt) {
			queueBlocked[p.Repo] = true
			continue
		}
		// Read only when a read can change the outcome. A not-yet-due pull request rechecked recently
		// keeps its repo gated and costs no request — this is what stops N reads per tick.
		if !dueForRead(p, queueBlocked[p.Repo], now) {
			queueBlocked[p.Repo] = true
			continue
		}
		// Stop before the reserve is touched: the remaining pull requests wait for the next pass (or
		// the window reset), and none of them is blocked for it.
		if budget := gh.RateBudget(); budget.Exhausted(now, rateFloor) {
			notify(n, runs.NoticeGitHubQuota, "", maintainQuotaText(budget.Reset))
			break
		}

		st, err := gh.GetPullRequest(ctx, p.Repo, p.Number)
		_ = prs.Touch(p.Repo, p.Number, now) // record the read time so the recheck cadence advances
		if err != nil {
			// A canceled pass is a shutdown, not this pull request's fault — stop cleanly, fault nothing.
			if ctx.Err() != nil {
				return firstErr
			}
			// An exhausted budget — or an explicit rate-limit rejection (429) — is the shared,
			// self-ending standstill, never THIS pull request's fault. The failed read already refreshed
			// the observed budget, so consult it (and the status) before faulting the record: a quota
			// that ends by itself must not be filed as a blockade on each of 67 entries.
			if budget := gh.RateBudget(); budget.Exhausted(now, rateFloor) || isRateLimited(err) {
				notify(n, runs.NoticeGitHubQuota, "", maintainQuotaText(budget.Reset))
				break
			}
			// A pull request GitHub no longer knows on a READ (404, or 410 gone) is a reached goal, not
			// an obstacle: there is nothing left to follow. Untrack it exactly as a closed pull request —
			// the read-side twin of isSatisfiedDelete on the write side — rather than blocking on a human
			// to wave away something already gone. Missing rights (401/403) are NOT this: they fall
			// through to recordFault below and stay blocked, because "gone" and "forbidden" are told apart
			// by the status number, never by the error text.
			if isReadGone(err) {
				untrackVanished(ledger, prs, res, n, pub, p, now)
				continue
			}
			recordFault(prs, n, p, readFailPrefix+err.Error(), err, now)
			queueBlocked[p.Repo] = true
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		switch {
		case st.Merged:
			mergedAt := now
			if st.MergedAt != nil {
				mergedAt = st.MergedAt.UTC()
			}
			if err := finalizeMerged(ctx, gh, prs, ledger, res, n, pub, p, st, mergedAt, now); err != nil {
				queueBlocked[p.Repo] = true
				if firstErr == nil {
					firstErr = err
				}
			}
		case st.State == "closed":
			// Closed without merge — a human rejection. Mirror it with its reason; the run stays
			// restartable (nothing is silently marked done).
			mirrorClosed(ledger, p, st, "pull request #"+fmt.Sprint(p.Number)+" was closed without merging", now)
			_ = prs.Remove(p.Repo, p.Number)
			settleExecutionOf(ledger, res, p)
			publishDeliveries(pub)
		default: // open
			due := !now.Before(p.MergeBy)
			if !due || queueBlocked[p.Repo] {
				queueBlocked[p.Repo] = true
				continue
			}
			// One merge per repo per tick: a successful merge just moved the default branch, a
			// failed one leaves the older PR open — either way the queue behind it waits.
			queueBlocked[p.Repo] = true
			if err := gh.MergePullRequest(ctx, p.Repo, p.Number, "merge"); err != nil {
				if ctx.Err() != nil {
					return firstErr // a shutdown mid-merge is not this pull request's fault
				}
				if budget := gh.RateBudget(); budget.Exhausted(now, rateFloor) || isRateLimited(err) {
					notify(n, runs.NoticeGitHubQuota, "", maintainQuotaText(budget.Reset))
					break
				}
				recordFault(prs, n, p, "auto-merge failed: "+err.Error(), err, now)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if err := finalizeMerged(ctx, gh, prs, ledger, res, n, pub, p, st, now, now); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	// Origin-status pass: every open PR of the managed repos (tracked ∪ open ledger entries)
	// carries the ledger-derived status — a hand-PR gets its explained rejection here. It, too, is
	// behind the reserve: an exhausted budget defers the restamp to the next armed pass rather than
	// spending the last requests and hitting the empty-quota rejection.
	if budget := gh.RateBudget(); budget.Exhausted(now, rateFloor) {
		return firstErr
	}
	if all, err := ledger.All(); err == nil {
		for _, d := range all {
			if d.OpenState() {
				repos[d.Repo] = true
			}
		}
	}
	for repo := range repos {
		heads, err := gh.ListOpenPullRequests(ctx, repo)
		if err != nil {
			continue // transient; the required-but-absent status keeps unvetted PRs unmergeable
		}
		for _, h := range heads {
			ok, desc, verr := originVerdict(ledger, repo, h.HeadRef, h.HeadSHA)
			if verr != nil {
				continue
			}
			state := "failure"
			if ok {
				state = "success"
			}
			_ = gh.PostCommitStatus(ctx, repo, h.HeadSHA, OriginStatusContext, state, desc)
		}
	}
	return firstErr
}

// finalizeMerged is the ONE place a merged delivery is completed: the ledger mirrors MergedAt,
// the SAME place deletes the delivery branch (404 = Satisfied; the workbench branch never),
// the pool entry goes, and the execution's result closes per the B-8 rule.
func finalizeMerged(ctx context.Context, gh GitHubOps, prs *runs.PRStore, ledger *runs.DeliveryStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher, p runs.PendingPR, st PRState, mergedAt, now time.Time) error {
	d, ok := deliveryFor(ledger, p)
	if ok && d.MergedAt == nil {
		d.MergedAt = &mergedAt
		if err := ledger.Put(d); err != nil {
			return err
		}
	}
	branch := st.HeadRef
	if branch == "" && ok {
		branch = d.Branch
	}
	if branch != "" && branch != workbench.LegacyShared {
		if err := gh.DeleteBranch(ctx, p.Repo, branch); err != nil && !isSatisfiedDelete(err) {
			// The delete stays owed: keep the record tracked with a backoff so the next tick
			// retries the prune (the merge itself is already mirrored — re-finalizing is safe).
			recordFault(prs, n, p, prunePrefix+err.Error(), err, now)
			return err
		}
	}
	_ = prs.Remove(p.Repo, p.Number)
	settleExecutionOf(ledger, res, p)
	publishDeliveries(pub)
	return nil
}

// deliveryFor resolves the ledger entry of a tracked PR — by DeliveryID first, by repo+number
// for legacy records.
func deliveryFor(ledger *runs.DeliveryStore, p runs.PendingPR) (runs.Delivery, bool) {
	if p.DeliveryID != "" {
		if d, ok, err := ledger.ByID(p.DeliveryID); err == nil && ok {
			return d, true
		}
	}
	all, err := ledger.All()
	if err != nil {
		return runs.Delivery{}, false
	}
	for _, d := range all {
		if d.Repo == p.Repo && d.PRNumber == p.Number {
			return d, true
		}
	}
	return runs.Delivery{}, false
}

// mirrorClosed mirrors a closed-without-merge PR onto its delivery with the reason.
func mirrorClosed(ledger *runs.DeliveryStore, p runs.PendingPR, st PRState, reason string, now time.Time) {
	d, ok := deliveryFor(ledger, p)
	if !ok || d.ClosedAt != nil || d.MergedAt != nil {
		return
	}
	d.ClosedAt = &now
	d.ClosedReason = reason
	_ = ledger.Put(d)
}

// settleExecutionOf closes the execution result of a tracked PR's delivery per B-8.
func settleExecutionOf(ledger *runs.DeliveryStore, res *runs.ResultStore, p runs.PendingPR) {
	if d, ok := deliveryFor(ledger, p); ok && d.ExecutionID != "" {
		_ = SettleExecution(ledger, res, d.ExecutionID)
	}
}

// SettleExecution applies the B-8 + WHAT-1 rule: once ALL deliveries of an execution are SETTLED
// (rolled back, closed with a reason, or merged AND delivered to production), the execution's
// Result.MergedAt is the time of the LAST settling — and only then. A merged delivery whose
// production step is still outstanding is NOT settled, so it keeps the execution in the open list:
// the task is done only when it runs in production, never merely on merge. Idempotent.
func SettleExecution(ledger *runs.DeliveryStore, res *runs.ResultStore, executionID string) error {
	if res == nil || executionID == "" {
		return nil
	}
	all, err := ledger.All()
	if err != nil {
		return err
	}
	var last time.Time
	found := false
	for _, d := range all {
		if d.ExecutionID != executionID {
			continue
		}
		found = true
		if !d.Settled() {
			return nil // one delivery still owes a step (merge or production) — execution stays open
		}
		if t, ok := d.SettleTime(); ok && t.After(last) {
			last = t
		}
	}
	if !found {
		return nil
	}
	r, ok, err := res.Get(executionID)
	if err != nil || !ok {
		return err
	}
	if r.MergedAt != nil {
		return nil
	}
	r.MergedAt = &last
	return res.Put(r)
}

// recordFault advances the K-5 state at the PR record. It splits obstacles by their NATURE, not by
// a retry count — because the two kinds ask for opposite reactions:
//
//   - A DURABLE obstacle ends only when a person acts: the pull request or repository is gone
//     (404/410), the rights are missing (401/403), or the request is malformed (422 or another 4xx).
//     Each of these is decided in faultclass.Classify as Permanent. Repetition cannot change it, so
//     the record is STILLED at once (blocked: reason, time) and waits for an explicit resume.
//   - A SELF-ENDING obstacle ends on its own: a server hiccup (5xx), a dropped connection, a passing
//     rate limit. Giving up here turns a minute's wait into a standstill — exactly what stilled 67
//     entries for 30 hours on 2026-07-31. So it is NEVER given up on: the record backs off with a
//     growing interval capped at maintainMaxBackoff and keeps retrying indefinitely. No attempt count
//     stills it. It stays fully visible (reason, attempts, first seen, next attempt) — visibility is
//     what the give-up was poorly standing in for, so it replaces it rather than needing it.
func recordFault(prs *runs.PRStore, n *runs.NoticeStore, p runs.PendingPR, reason string, cause error, now time.Time) {
	permanent := classify(cause) == faultclass.Permanent
	_, _ = prs.Update(p.Repo, p.Number, func(rec *runs.PendingPR) {
		if permanent {
			rec.Blocked = true
			rec.BlockedReason = reason
			rec.BlockedAt = now
			rec.Backoff = nil
			return
		}
		b := model.Backoff{Reason: reason, Class: "transient", FirstAt: now}
		if rec.Backoff != nil {
			b = *rec.Backoff
			b.Reason = reason
		}
		// maxAttempts 0 = never block: a self-ending obstacle is retried for ever, only ever more
		// slowly, up to maintainMaxBackoff between attempts.
		next, _ := advanceBackoff(b, now, 0, maintainMaxBackoff)
		rec.Backoff = &next
	})
	// A fresh block is a DISTURBANCE the user must see (a person is the only way out), surfaced once —
	// only the transition, never a retry. A self-ending obstacle raises NO notice: it clears itself,
	// and its retry state stays visible in the delivery surface (the backoff record) without alarming
	// anyone for something that fixes itself.
	if !permanent {
		return
	}
	if cur, err := prs.List(); err == nil {
		for _, rec := range cur {
			if rec.Repo == p.Repo && rec.Number == p.Number && rec.Blocked && !p.Blocked {
				notify(n, runs.NoticeDeliveryBlocked, p.Repo, fmt.Sprintf("pull request #%d is blocked: %s", p.Number, rec.BlockedReason))
			}
		}
	}
}

// isSatisfiedDelete reports the delete-like Satisfied: the branch is ALREADY GONE, which is the
// goal of the prune — never a fault. GitHub reports an already-gone ref two ways, and both mean
// the same thing: a 404, or — just as often for a ref delete — a 422 "Reference does not exist".
// Before this second form was recognised, five merged deliveries were stilled for reaching exactly
// what the prune was meant to reach.
func isSatisfiedDelete(err error) bool {
	var se *github.StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Status == 404 || (se.Status == 422 && strings.Contains(se.Msg, "Reference does not exist"))
}

// isReadGone reports the read-side reached goal: GitHub answers a pull-request READ with 404 (not
// found) or 410 (gone). Both mean the pull request is no longer there — nothing left to follow — so
// on a READ this is a goal reached, never a fault. It is the read-side twin of isSatisfiedDelete on
// the write side (where 404 on a delete means the branch is already gone). The distinction hangs on
// the STATUS NUMBER, never on the message text, so a missing right (401/403) can never be mistaken
// for a vanished pull request or the reverse.
func isReadGone(err error) bool {
	var se *github.StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Status == 404 || se.Status == 410
}

// untrackVanished removes a pull request GitHub no longer knows from the tracked set and pulls the
// ledger along, exactly as a closed pull request is handled: the ledger entry is mirrored closed —
// so NextPRBase stops counting it open and the stack tip never points at a vanished branch — the
// pool entry is dropped, the execution settles per B-8, the delivery surface republishes, and a
// visible self-check notice records the untracking so nothing disappears silently. A block recorded
// for this same cause dissolves the moment this runs: the record it stilled is gone.
func untrackVanished(ledger *runs.DeliveryStore, prs *runs.PRStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher, p runs.PendingPR, now time.Time) {
	reason := fmt.Sprintf("pull request #%d no longer exists on GitHub and was untracked", p.Number)
	mirrorClosed(ledger, p, PRState{}, reason, now)
	_ = prs.Remove(p.Repo, p.Number)
	settleExecutionOf(ledger, res, p)
	publishDeliveries(pub)
	notify(n, runs.NoticeDeliverySelfCheck, p.Repo, reason)
}

// blockIsReadGone reports whether an already-recorded block was only ever a READ GitHub answered
// 404/410 — a vanished pull request, which is a reached goal, not a durable fault. Such a block is
// no longer a block: the maintenance loop untracks the record instead of keeping it stilled for a
// person. It reads the stored reason the same way ReviveStaleBlocks does (the reason is the only
// thing a persisted block keeps) and hangs the decision on the status number, never the message text
// — so a stored 401/403 (a genuine missing right) stays a block.
func blockIsReadGone(reason string) bool {
	return strings.HasPrefix(reason, readFailPrefix) && isReadGone(errorFromReason(reason))
}

// isRateLimited reports GitHub's explicit "too many requests" (429): the request window is spent.
// This is the shared, self-ending standstill — a property of the token's hourly budget, not of any
// single pull request — so it must never be filed as a fault on an individual entry. A primary limit
// GitHub answers with 403 and an empty X-RateLimit-Remaining is caught by the budget reserve instead
// (RateBudget.Exhausted); this covers the 429 form the reserve cannot see coming.
func isRateLimited(err error) bool {
	var se *github.StatusError
	return errors.As(err, &se) && se.Status == 429
}

// ReviveStaleBlocks lifts, at service start, every persisted delivery block whose reason is NO
// LONGER a durable fault under the current classification (point 4 of the order). A block is a
// terminal state that a stored record keeps forever until a person releases it — so a record
// stilled by the OLD, wrong reading (a prune that reached its goal, a timeout that ends by itself)
// stays stilled across restarts and still needs a human, which was the very problem. This sweep
// re-decides each stored block from the reason it kept and releases the ones that are no longer
// blocks; a genuine durable fault (missing rights, a gone pull request) stays. It talks to no
// GitHub — it only re-reads the local records — so it is safe on the boot path.
//
// It returns how many records it released.
func ReviveStaleBlocks(prs *runs.PRStore) (int, error) {
	if prs == nil {
		return 0, nil
	}
	cur, err := prs.List()
	if err != nil {
		return 0, err
	}
	revived := 0
	for _, p := range cur {
		if !p.Blocked || !blockNowStale(p.BlockedReason) {
			continue
		}
		freed, err := prs.ResumeBlocked(p.Repo, p.Number)
		if err != nil {
			return revived, err
		}
		revived += len(freed)
	}
	return revived, nil
}

// blockNowStale reports whether an ALREADY-recorded block would, under the current classification,
// not be a block at all. It replays the two decisions recordFault makes, from the reason string the
// block preserved (the only thing a persisted block keeps): first the delete-satisfied rule — a
// prune that failed only because the branch was already gone reached its goal — then
// faultclass.Classify on the error reconstructed from the reason. Anything that is not Permanent (a
// timeout, a dropped connection, a secondary-rate-limit 403) is self-ending and no longer a block.
func blockNowStale(reason string) bool {
	err := errorFromReason(reason)
	if err == nil {
		return false
	}
	if strings.HasPrefix(reason, prunePrefix) && isSatisfiedDelete(err) {
		return true
	}
	return classify(err) != faultclass.Permanent
}

// errorFromReason rebuilds a representative error from a stored fault reason so the ONE classifier
// (faultclass.Classify) decides its class — the reclassification adds no parallel set of rules, it
// only turns the preserved text back into the kind of error it named. A wait cut short (deadline,
// client timeout, canceled) becomes the matching context error; a "github: status NNN:" reason
// becomes that StatusError; anything else stays an opaque error (Transient by default).
func errorFromReason(reason string) error {
	if reason == "" {
		return nil
	}
	switch {
	case strings.Contains(reason, "context deadline exceeded"), strings.Contains(reason, "Client.Timeout"):
		return context.DeadlineExceeded
	case strings.Contains(reason, "context canceled"):
		return context.Canceled
	}
	if status, ok := statusFromReason(reason); ok {
		return &github.StatusError{Status: status, Msg: reason}
	}
	return errors.New(reason)
}

// statusFromReason extracts the HTTP status a StatusError reason carries ("...status NNN:...").
func statusFromReason(reason string) (int, bool) {
	const marker = "status "
	i := strings.Index(reason, marker)
	if i < 0 {
		return 0, false
	}
	j := i + len(marker)
	k := j
	for k < len(reason) && reason[k] >= '0' && reason[k] <= '9' {
		k++
	}
	if k == j {
		return 0, false
	}
	n, err := strconv.Atoi(reason[j:k])
	if err != nil {
		return 0, false
	}
	return n, true
}

// ── Emergency override (REQ-033.4) ───────────────────────────────────────────────────────

// AdminOverride records the emergency path: who, when, which PR, why — as a notice and in the
// daily report (REQ-033.4). It never performs the merge itself.
func AdminOverride(ctx context.Context, n *runs.NoticeStore, by model.Actor, pr model.PRRef, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("deliver: an override must name its reason")
	}
	if by.User == "" {
		return errors.New("deliver: an override must name who overrides")
	}
	if n == nil {
		return errors.New("deliver: no notice store — the incident cannot be recorded, so the override is refused")
	}
	notify(n, "admin-override", "",
		fmt.Sprintf("%s overrode the delivery-origin protection for PR #%d (%s) at %s: %s",
			by.User, pr.Number, pr.URL, time.Now().UTC().Format(time.RFC3339), reason))
	return nil
}

// notify bridges S10 findings into the notice pool. The pool's record (B10) is
// assignment-shaped today; the finding rides in Kind + Reason until the pool grows its
// repo/text fields — one place to adjust.
func notify(n *runs.NoticeStore, kind, repo, text string) {
	if n == nil {
		return
	}
	msg := text
	if repo != "" {
		msg = repo + ": " + text
	}
	_ = n.Add(runs.Notice{Kind: kind, Reason: msg})
}

// publishDeliveries ticks the deliveries topic after a successful ledger write.
func publishDeliveries(pub live.Publisher) {
	if pub != nil {
		pub.Publish(live.TopicDeliveries)
	}
}
