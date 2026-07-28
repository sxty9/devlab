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
	"sort"
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

// maintainMaxAttempts bounds the transient retries of one maintenance action before the PR
// record is honestly blocked (K-5) and waits for an explicit resume.
const maintainMaxAttempts = 5

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

// NextPRBase is pure: the stacked base of the next PR — the last open delivery's branch, else
// the default branch (REQ-024). The workbench branch is never a base candidate: it is never a
// delivery branch (S9), and a defensive skip keeps a corrupt ledger from ever stacking on it.
func NextPRBase(open []runs.Delivery, defaultBranch string) string {
	for i := len(open) - 1; i >= 0; i-- {
		if b := open[i].Branch; b != "" && b != workbench.Branch {
			return b
		}
	}
	return defaultBranch
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
	if in.Head == workbench.Branch {
		// mercury-dev is pushed as a backup and is NEVER itself turned into a pull request (S9).
		return model.PRRef{}, false, fmt.Errorf("deliver: %s is never turned into a pull request", workbench.Branch)
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
		// The reversal itself is open now; its base must stack on what came BEFORE it.
		withoutSelf := make([]runs.Delivery, 0, len(open))
		for _, o := range open {
			if o.ID != revID {
				withoutSelf = append(withoutSelf, o)
			}
		}
		base := NextPRBase(withoutSelf, cb.DefaultBranch)
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
		if d.PRNumber != 0 {
			if err := gh.ClosePullRequest(ctx, d.Repo, d.PRNumber, rollbackCloseReason(d, by)); err != nil {
				return out, fmt.Errorf("close PR: %w", err)
			}
			out.ClosedPR = &model.PRRef{Number: d.PRNumber, URL: d.PRURL, HeadBranch: d.Branch}
		}
		d.ClosedAt = &now
		d.ClosedReason = rolledBackReasonPrefix + actorName(by) + " — counter-booked on " + workbench.Branch + "@" + shortSHA(cb.After)
		if err := ledger.Put(d); err != nil {
			return out, err
		}
		if out.Detail == "" {
			out.Detail = "open delivery counter-booked and its PR closed with the justification"
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
		": its changes were withdrawn from " + workbench.Branch + " as a counter-booking commit. " +
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
			out = append(out, ProtectionReport{Repo: repo, Detail: "protection unreadable: " + err.Error()})
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

// Maintain is the recurring PR maintenance: auto-merge after the window (method EXCLUSIVELY
// "merge"); the SAME place merges AND deletes the delivery branch (F14/REQ-026.4; branch 404 =
// Satisfied); sets Result.MergedAt per the B-8 rule; backoff/blockade at the PR record;
// mercury-dev NEVER falls under prune. It also re-posts the delivery-origin status on every
// open PR of the repos it manages, so a hand-raised PR carries its explained rejection.
func Maintain(ctx context.Context, gh GitHubOps, prs *runs.PRStore, ledger *runs.DeliveryStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher) error {
	if prs == nil || ledger == nil {
		return errors.New("deliver: maintain needs the PR pool and the ledger")
	}
	tracked, err := prs.List()
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	// Auto-merge strictly in creation order per repo: an older open PR gates the younger ones
	// (the stack collapses front to back), whatever the individual outcome.
	sort.SliceStable(tracked, func(i, j int) bool { return tracked[i].CreatedAt.Before(tracked[j].CreatedAt) })
	queueBlocked := map[string]bool{}
	repos := map[string]bool{}

	var firstErr error
	for _, p := range tracked {
		repos[p.Repo] = true
		if p.Blocked {
			// F4: an honestly blocked record waits for an explicit resume — and gates its repo.
			queueBlocked[p.Repo] = true
			continue
		}
		if p.Backoff != nil && now.Before(p.Backoff.NextAt) {
			queueBlocked[p.Repo] = true
			continue
		}

		st, err := gh.GetPullRequest(ctx, p.Repo, p.Number)
		if err != nil {
			recordFault(prs, n, p, "reading the pull request failed: "+err.Error(), err, now)
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
	// carries the ledger-derived status — a hand-PR gets its explained rejection here.
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
	if branch != "" && branch != workbench.Branch {
		if err := gh.DeleteBranch(ctx, p.Repo, branch); err != nil && !isSatisfiedDelete(err) {
			// The delete stays owed: keep the record tracked with a backoff so the next tick
			// retries the prune (the merge itself is already mirrored — re-finalizing is safe).
			recordFault(prs, n, p, "pruning the delivery branch failed: "+err.Error(), err, now)
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

// SettleExecution applies the B-8 rule: once ALL deliveries of an execution are merged, rolled
// back or closed with a reason, the execution's Result.MergedAt is the time of the LAST one —
// and only then. Idempotent; a still-open delivery leaves the result untouched.
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
		switch {
		case d.MergedAt != nil:
			if d.MergedAt.After(last) {
				last = *d.MergedAt
			}
		case d.ClosedAt != nil:
			if d.ClosedAt.After(last) {
				last = *d.ClosedAt
			}
		default:
			return nil // one delivery still open — the execution stays in the list (B-8)
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

// recordFault advances the K-5 state at the PR record: a transient fault backs off with
// growing intervals; exhausted attempts (or a permanent fault) block the record honestly —
// reason, time, attempts — until an explicit resume.
func recordFault(prs *runs.PRStore, n *runs.NoticeStore, p runs.PendingPR, reason string, cause error, now time.Time) {
	permanent := classify(cause) == faultclass.Permanent
	_, _ = prs.Update(p.Repo, p.Number, func(rec *runs.PendingPR) {
		if permanent {
			rec.Blocked = true
			rec.BlockedReason = reason
			rec.BlockedAt = now
			return
		}
		b := model.Backoff{Reason: reason, Class: "transient", FirstAt: now}
		if rec.Backoff != nil {
			b = *rec.Backoff
			b.Reason = reason
		}
		next, blocked := advanceBackoff(b, now, maintainMaxAttempts)
		rec.Backoff = &next
		if blocked {
			rec.Blocked = true
			rec.BlockedReason = fmt.Sprintf("%s (after %d attempts)", reason, next.Attempts)
			rec.BlockedAt = now
		}
	})
	// Surface a fresh blockade in the notices (portioned: only the transition, not every retry).
	if cur, err := prs.List(); err == nil {
		for _, rec := range cur {
			if rec.Repo == p.Repo && rec.Number == p.Number && rec.Blocked && !p.Blocked {
				notify(n, "delivery-blocked", p.Repo, fmt.Sprintf("pull request #%d is blocked: %s", p.Number, rec.BlockedReason))
			}
		}
	}
}

// isSatisfiedDelete reports the delete-like Satisfied: the branch is already gone (404).
func isSatisfiedDelete(err error) bool {
	var se *github.StatusError
	return errors.As(err, &se) && se.Status == 404
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
