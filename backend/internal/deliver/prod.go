// The production step of the delivery chain (WHAT-1) — the LAST step, AFTER the stack. Every other
// step of the chain runs INSIDE an execution (preflight → implement → deliver-dev → publish →
// pull-request); the merge and this production send happen afterwards, in the recurring maintenance.
// This file adds the production send as a SEPARATE pass over the ledger so it is, by construction, a
// matter of its own after the stack: a failed send is booked on the delivery it belongs to and never
// on the merged layer beneath it (WHAT-3).
package deliver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// maintainProdMaxBackoff caps the growing wait between production retries — the same ceiling the PR
// maintenance uses for a self-ending obstacle. A production send that fails on something that ends by
// itself (the receiver is briefly unreachable, the target host is restarting) is retried for ever,
// only ever more slowly, never given up on (WHAT-3, mirrors the "self-ending obstacle" rule).
const maintainProdMaxBackoff = time.Hour

// ProdOutcome is the honest result of one production delivery — proven, never assumed. Running is
// set ONLY when the service is active in production and answers (the same gate the dev delivery
// proves with, executed on the target). NotApplicable states a PROVEN property of the repository
// that makes production inapplicable (it is no service); Evidence attests it. Detail is the one-line
// human outcome. Commit is the standard-branch SHA the send built from and shipped — the state
// production now carries — so the pass can record it and later tell whether the standard branch has
// moved past it.
type ProdOutcome struct {
	Running       bool
	NotApplicable bool
	Evidence      string
	Detail        string
	Commit        string
}

// ProdDeployer ships a merged delivery's default-branch state to production and PROVES it runs there
// — the SAME honest gate the dev delivery uses (WHAT-2), executed on the target host. It returns
// Running only when the service is up in production; a NotApplicable outcome names the proven
// property that made production inapplicable; any other case (an unreachable receiver, an
// unconfigured production target, a failed remote install) is an error. It is the ONE seam the
// production pass reaches the outside world through, wired in the api layer and substituted by a
// fixture in tests.
//
// DefaultBranchTip reports the current commit at the tip of a repository's standard (default)
// branch — the state production MUST carry. The reconciliation reads it to MEASURE, not assume,
// whether the standard branch has advanced past what production runs; "" (with no error) means the
// tip could not be resolved and the reconciliation leaves the repository alone rather than guess.
type ProdDeployer interface {
	DeployProd(ctx context.Context, repo string) (ProdOutcome, error)
	DefaultBranchTip(ctx context.Context, repo string) (string, error)
}

// MaintainProd is the LAST step of the delivery chain (WHAT-1): after the stack — after every pull
// request that is due has merged — it ships each merged delivery that still owes production to the
// production host and proves it running there. It is a SEPARATE pass from Maintain on purpose
// (WHAT-3): a production send is a matter of its own, after the stack, so its outcome is booked on
// the delivery it belongs to and can never invalidate the merged layer beneath it.
//
// What a SUCCESS does: the delivery is stamped ProdDeployedAt, which settles it — and, once every
// delivery of its execution is settled, historizes the task (deliver.SettleExecution / B-8).
// What a FAILURE does: the task is NOT historized (its execution stays open), the failure is
// reported as a disturbance the user sees (NoticeProdUndelivered), and the send backs off and is
// retried by itself on the next pass, for ever (a self-ending obstacle is never given up on). The
// stack builds on unchanged either way — a production failure holds no branch and blocks no repo.
//
// A NOT-APPLICABLE outcome is honoured only from a proven property (the repository is no service):
// it settles the delivery like a success, because there is genuinely nothing to run in production.
// A MISSING production configuration is NOT such a property — it is a deficiency, so the deployer
// surfaces it as an error and it is booked as a failure that keeps retrying and reporting, never as
// a silent skip ("Kein stummes Ausbleiben").
func MaintainProd(ctx context.Context, prod ProdDeployer, ledger *runs.DeliveryStore, prodState *runs.ProdStateStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher) error {
	if ledger == nil {
		return errors.New("deliver: the production pass needs the delivery ledger")
	}
	if prod == nil {
		return nil // no production deployer wired (a bare test harness) — nothing to attempt
	}
	all, err := ledger.All()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var firstErr error
	// The repositories this pass touched through a booked delivery — the reconciliation below leaves
	// them alone, because the chain just handled them: re-measuring the same repository in the same
	// pass would only spend a request and risk a second send.
	handled := map[string]bool{}
	for _, d := range all {
		if !d.NeedsProd() {
			continue
		}
		if ctx.Err() != nil {
			return firstErr // a shutdown stops the pass cleanly; the rest waits for the next one
		}
		// A self-ending failure waits out its growing backoff — an attempt before its next time changes
		// nothing but spends a request, so it is skipped until then. The backoff record stays visible.
		if d.ProdBackoff != nil && now.Before(d.ProdBackoff.NextAt) {
			handled[d.Repo] = true // still owed and mid-retry — not a silent drift for the reconciliation
			continue
		}
		handled[d.Repo] = true

		out, derr := prod.DeployProd(ctx, d.Repo)
		if ctx.Err() != nil {
			return firstErr // a shutdown mid-send is not this delivery's fault
		}

		switch {
		case derr == nil && out.NotApplicable:
			// A proven property (no service) — production genuinely does not apply. Settle it like a
			// success: the merged work is as delivered as it will ever be.
			d.ProdNotApplicable, d.ProdEvidence = true, out.Evidence
			d.ProdFailedAt, d.ProdFailedReason, d.ProdBackoff = nil, "", nil
			if err := ledger.Put(d); err != nil {
				firstErr = orFirst(firstErr, err)
				continue
			}
			settleAfterProd(ledger, res, d)
			publishDeliveries(pub)

		case derr == nil && out.Running:
			// Proven running in production — the last step of the chain succeeded. Stamp it and settle,
			// and record the standard-branch commit production now carries so the reconciliation can
			// tell later whether the standard branch has moved past it.
			at := now
			d.ProdDeployedAt = &at
			d.ProdFailedAt, d.ProdFailedReason, d.ProdBackoff = nil, "", nil
			if err := ledger.Put(d); err != nil {
				firstErr = orFirst(firstErr, err)
				continue
			}
			recordProdCarries(prodState, d.Repo, out.Commit, now)
			settleAfterProd(ledger, res, d)
			publishDeliveries(pub)

		default:
			// A failure that says nothing about the stack: an unreachable receiver, a target still
			// restarting, an unconfigured production target (a deficiency). Book it on this delivery,
			// back it off for the next pass, report it once — and leave the merged layer untouched.
			reason := prodFailReason(out, derr)
			at := now
			d.ProdFailedAt, d.ProdFailedReason = &at, reason
			b := model.Backoff{Reason: reason, Class: "prod", FirstAt: now}
			if d.ProdBackoff != nil {
				b = *d.ProdBackoff
				b.Reason = reason
			}
			next, _ := advanceBackoff(b, now, 0, maintainProdMaxBackoff) // maxAttempts 0 = never give up
			d.ProdBackoff = &next
			if err := ledger.Put(d); err != nil {
				firstErr = orFirst(firstErr, err)
				continue
			}
			// The message carries NO attempt count or next-time on purpose, so a repeat of the SAME
			// failure coalesces into one record (the user hears it once) while the growing retry state
			// stays visible on the delivery itself (ProdBackoff). A CHANGED reason is a new finding.
			notify(n, runs.NoticeProdUndelivered, d.Repo, prodUndeliveredText(reason))
			publishDeliveries(pub)
			firstErr = orFirst(firstErr, derr)
		}
	}

	// After the booked deliveries, close the SILENT-DRIFT gap: a repository whose standard branch has
	// advanced past what production carries WITHOUT a booked delivery to account for it. Nothing above
	// would ever notice — the production send only ever fires for a merged delivery that still owes
	// production, so the standard branch moving on another path (a hand-merged pull request) leaves
	// production quietly behind while the ledger reads "delivered". reconcileProd measures that gap,
	// names it ("Kein stummes Ausbleiben"), and brings production up over the SAME production path.
	reconcileProd(ctx, prod, all, prodState, n, pub, handled, now)
	return firstErr
}

// reconcileProd is the gap-closing pass: for every repository production is expected to run but that
// the booked-delivery loop did NOT just handle, it MEASURES the standard-branch tip against the
// commit production is recorded to carry. When they differ — the standard branch advanced with no
// delivery to account for it — it NAMES the drift (a disturbance the user sees) and brings production
// up to the standard branch over the existing production send. When they match it does nothing at
// all: no message, no send (test b).
//
// It only ever considers a repository production actually runs (one whose delivery reached production
// before) and only when that repository is quiescent — no open, failed, or production-owing delivery,
// because those the chain itself is still driving. A read it cannot complete (the tip could not be
// resolved) is left for the next pass rather than guessed at; a reconciliation read failure is never
// the production pass's error, so it does not mask a real delivery failure.
func reconcileProd(ctx context.Context, prod ProdDeployer, all []runs.Delivery, prodState *runs.ProdStateStore, n *runs.NoticeStore, pub live.Publisher, handled map[string]bool, now time.Time) {
	if prodState == nil {
		return // nowhere to record what production carries — the reconciliation has no reference
	}
	for _, repo := range reconcileRepos(all, handled) {
		if ctx.Err() != nil {
			return
		}
		tip, err := prod.DefaultBranchTip(ctx, repo)
		if err != nil || tip == "" {
			continue // could not measure the standard branch — leave it for the next pass, never guess
		}
		rec, ok, err := prodState.Get(repo)
		if err != nil {
			continue
		}
		if ok && rec.Commit == tip {
			continue // production carries the standard branch — no drift, no message, no send
		}

		// The standard branch moved past what production carries with no delivery to account for it.
		notify(n, runs.NoticeProdDrift, repo, prodDriftText(tip, rec.Commit, ok))
		publishDeliveries(pub)

		// Bring production up over the EXISTING production path (no second path is built). On proof it
		// runs, record the standard-branch commit production now carries so the next pass reads even.
		out, derr := prod.DeployProd(ctx, repo)
		if ctx.Err() != nil {
			return
		}
		switch {
		case derr == nil && out.Running:
			recordProdCarries(prodState, repo, out.Commit, now)
			publishDeliveries(pub)
		case derr == nil && out.NotApplicable:
			// The reference said production runs this repository, yet the send proves it no service now
			// (it was reshaped). Nothing runs in production, so there is nothing to keep even.
		default:
			// The drift is named, but production could not be brought current (an unreachable receiver,
			// an unconfigured target). Report it the same way a merged delivery's failed send is.
			notify(n, runs.NoticeProdUndelivered, repo, prodUndeliveredText(prodFailReason(out, derr)))
			publishDeliveries(pub)
		}
	}
}

// reconcileRepos is the work list of the reconciliation: every repository production is expected to
// run (a delivery of it reached production) that is quiescent (no open, failed, or production-owing
// delivery) and was not just handled by the booked-delivery loop. The order is stable (repository
// name) so a pass is deterministic.
func reconcileRepos(all []runs.Delivery, handled map[string]bool) []string {
	expected := map[string]bool{}
	blocked := map[string]bool{}
	for _, d := range all {
		if d.ProdDeployedAt != nil {
			expected[d.Repo] = true
		}
		// A repository the chain is still driving is not a silent drift: an unmerged (open) delivery, a
		// failed dev tip, or a merged delivery still owing production all mean the chain will act on it.
		if d.OpenState() || d.Failed() || d.NeedsProd() {
			blocked[d.Repo] = true
		}
	}
	out := []string{}
	for repo := range expected {
		if handled[repo] || blocked[repo] {
			continue
		}
		out = append(out, repo)
	}
	sort.Strings(out)
	return out
}

// recordProdCarries records the standard-branch commit production now carries for a repository. An
// empty commit is not recorded — an unproven state is worse than an absent one, since the
// reconciliation reads an absent record as "measure afresh" and a wrong one as "even".
func recordProdCarries(prodState *runs.ProdStateStore, repo, commit string, now time.Time) {
	if prodState == nil || commit == "" {
		return
	}
	_ = prodState.Put(runs.ProdRecord{Repo: repo, Commit: commit, DeployedAt: now})
}

// prodDriftText is the disturbance the user sees when the standard branch has advanced past what
// production carries. It states the effect (production runs an older state than the standard branch)
// and that the standard branch moved without a delivery — the governance finding — and that
// production is being brought current over the existing path. carriedKnown false means production's
// running commit was not on record at all, which is itself the gap being closed.
func prodDriftText(tip, carried string, carriedKnown bool) string {
	if !carriedKnown {
		return fmt.Sprintf("the standard branch is at %s and production's running commit was not on "+
			"record — production is being brought up to the standard branch over the existing production "+
			"path so the two are provably equal. A change that reaches the standard branch belongs to a "+
			"recorded delivery.", shortSHA(tip))
	}
	return fmt.Sprintf("the standard branch advanced to %s but production carries %s — no recorded "+
		"delivery accounts for the change, so production ran an older state while the ledger read "+
		"'delivered'. Production is being brought up to the standard branch over the existing production "+
		"path. A change that reaches the standard branch outside a recorded delivery should be delivered "+
		"as an order.", shortSHA(tip), shortSHA(carried))
}

// settleAfterProd re-runs the B-8 completion rule for the delivery's execution now that its
// production step is done — which may historize the task (all its deliveries settled).
func settleAfterProd(ledger *runs.DeliveryStore, res *runs.ResultStore, d runs.Delivery) {
	if d.ExecutionID != "" {
		_ = SettleExecution(ledger, res, d.ExecutionID)
	}
}

// prodFailReason names the failure honestly: the deployer's own detail when it gave one, else the
// error. Never an empty sentence.
func prodFailReason(out ProdOutcome, err error) string {
	if out.Detail != "" {
		return out.Detail
	}
	if err != nil {
		return err.Error()
	}
	return "production delivery reported no proof that the service is running"
}

// prodUndeliveredText is the disturbance the user sees when a merged delivery has not reached
// production. It states the effect (not live yet) and that the send retries by itself.
func prodUndeliveredText(reason string) string {
	return fmt.Sprintf("the merged work has NOT reached production yet — %s. The task stays open until "+
		"the service runs in production; the production send is retried by itself and never given up on. "+
		"The merged work on the default branch is unaffected.", reason)
}

// orFirst keeps the FIRST error of a pass, so a later one does not mask the one that started it.
func orFirst(first, err error) error {
	if first == nil {
		return err
	}
	return first
}
