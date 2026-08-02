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
// human outcome.
type ProdOutcome struct {
	Running       bool
	NotApplicable bool
	Evidence      string
	Detail        string
}

// ProdDeployer ships a merged delivery's default-branch state to production and PROVES it runs there
// — the SAME honest gate the dev delivery uses (WHAT-2), executed on the target host. It returns
// Running only when the service is up in production; a NotApplicable outcome names the proven
// property that made production inapplicable; any other case (an unreachable receiver, an
// unconfigured production target, a failed remote install) is an error. It is the ONE seam the
// production pass reaches the outside world through, wired in the api layer and substituted by a
// fixture in tests.
type ProdDeployer interface {
	DeployProd(ctx context.Context, repo string) (ProdOutcome, error)
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
func MaintainProd(ctx context.Context, prod ProdDeployer, ledger *runs.DeliveryStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher) error {
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
			continue
		}

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
			// Proven running in production — the last step of the chain succeeded. Stamp it and settle.
			at := now
			d.ProdDeployedAt = &at
			d.ProdFailedAt, d.ProdFailedReason, d.ProdBackoff = nil, "", nil
			if err := ledger.Put(d); err != nil {
				firstErr = orFirst(firstErr, err)
				continue
			}
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
	return firstErr
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
