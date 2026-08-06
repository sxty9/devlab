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
	// HostKeyChanged is set when the send failed specifically because the production target's ssh host
	// key no longer matches the recorded one — a reinstalled (or intercepted) host, NOT a plain
	// connection failure. HostKeyTarget names that host. The production pass turns this into its own
	// distinct reason and a deliberate approval, rather than a masked retry (task part 2).
	HostKeyChanged bool
	HostKeyTarget  string
}

// HostKeyGate is the deliberate accept path for a CHANGED production host key, wired in the api layer
// (deploy.HostKeyManager) and faked in tests. The production pass NEVER trusts a new key on its own:
// ScanFingerprint only READS the key the target currently presents (to show the human and to pin the
// approval); Accept re-pins the known-hosts file to that key ONLY after re-confirming it still matches
// the approved fingerprint, so an approval covers exactly one key and nothing that changed after it.
// nil means production is unarmed or no gate is wired — the change is still reported, just not accepted.
type HostKeyGate interface {
	// Target is the production host whose key the gate manages (for dedup and the approval record).
	Target() string
	// ScanFingerprint reads the SHA256 fingerprint the target currently presents. Reading is not trust.
	ScanFingerprint(ctx context.Context) (string, error)
	// Accept re-pins the known-hosts file to the target's current key iff it still hashes to
	// approvedFingerprint; a key that changed again is refused (the approval was for a different key).
	Accept(ctx context.Context, approvedFingerprint string) error
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
func MaintainProd(ctx context.Context, prod ProdDeployer, ledger *runs.DeliveryStore, prodState *runs.ProdStateStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher, questions *runs.QuestionStore, hostkey HostKeyGate) error {
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
	// Before any send: if the operator APPROVED a new production host key on the Blocked surface,
	// accept it now (verified against the pinned fingerprint) so the held sends below can succeed. This
	// never trusts a key on its own — the gate re-reads it and refuses a key that changed again.
	applyApprovedHostKey(ctx, questions, hostkey, n, pub)
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
			// A CHANGED production host key gets its OWN distinct reason and a deliberate approval — never
			// a masked "connection failed" retried in silence (task part 2). Everything else reports as a
			// plain undelivered production send. Both keep the merged layer beneath untouched.
			if out.HostKeyChanged {
				raiseHostKeyQuestion(ctx, questions, hostkey, out.HostKeyTarget, d.Repo)
				notify(n, runs.NoticeProdHostKeyChanged, d.Repo, prodHostKeyChangedText(out.HostKeyTarget, reason))
			} else {
				// The message carries NO attempt count or next-time on purpose, so a repeat of the SAME
				// failure coalesces into one record (the user hears it once) while the growing retry state
				// stays visible on the delivery itself (ProdBackoff). A CHANGED reason is a new finding.
				notify(n, runs.NoticeProdUndelivered, d.Repo, prodUndeliveredText(reason))
			}
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

// applyApprovedHostKey redeems an operator's approval of a new production host key: it accepts the
// key the human approved (re-verified against the pinned fingerprint) and re-pins the known-hosts file
// so the held production sends resume. It runs once per pass, before any send. The approval is
// single-use — it is consumed (Resolve) whether the accept succeeds or is refused, so a key that
// changed AGAIN after approval does not silently carry the stale approval forward: the next send fails
// afresh and asks again for the key now present. A success is a positive transition the user sees;
// a refusal is reported with its own reason.
func applyApprovedHostKey(ctx context.Context, questions *runs.QuestionStore, hostkey HostKeyGate, n *runs.NoticeStore, pub live.Publisher) {
	if questions == nil || hostkey == nil {
		return
	}
	target := hostkey.Target()
	if target == "" {
		return
	}
	q, err := questions.ApprovedHostKeyQuestion(target)
	if err != nil || q == nil {
		return
	}
	acceptErr := hostkey.Accept(ctx, q.HostKeyFingerprint)
	_ = questions.Resolve(q.ID) // single-use: consumed whether it took or was refused
	if acceptErr != nil {
		// The key changed again since the approval, or could not be read: name it, do not trust it.
		notify(n, runs.NoticeProdHostKeyChanged, q.Repo, "the approval of the production host key could NOT be applied: "+acceptErr.Error()+
			" — nothing was trusted; the next production send will surface the key now present so it can be approved afresh.")
		publishDeliveries(pub)
		return
	}
	notify(n, runs.NoticeProdHostKeyAccepted, q.Repo, fmt.Sprintf("the new ssh host key of the production target %q was approved and recorded (%s); "+
		"the held production sends resume on this pass.", target, q.HostKeyFingerprint))
	publishDeliveries(pub)
}

// raiseHostKeyQuestion asks the user, ONCE per host, to deliberately approve the changed production
// host key — the same Blocked/approval path the root-wrapper renewal uses (task part 2.8: reuse the
// existing approval, do not build a second one beside it). It reads (does not trust) the fingerprint
// the target now presents and pins the question to it, so the later approval covers exactly that key.
// A question already open for the host is left as is (asked once); if the key cannot even be read,
// no question is raised this pass (there is nothing to pin) — the distinct notice already told the
// user, and a later pass retries the read.
func raiseHostKeyQuestion(ctx context.Context, questions *runs.QuestionStore, hostkey HostKeyGate, target, repo string) {
	if questions == nil || hostkey == nil || target == "" {
		return
	}
	if existing, err := questions.OpenHostKeyQuestion(target); err != nil || existing != nil {
		return
	}
	fp, err := hostkey.ScanFingerprint(ctx)
	if err != nil || fp == "" {
		return // cannot read the new key to pin it — the notice stands; a later pass tries again
	}
	_, _ = questions.Raise(runs.Question{
		QKind:              runs.QuestionProdHostKey,
		Repo:               repo,
		RunTitle:           "Production host key",
		HostKeyTarget:      target,
		HostKeyFingerprint: fp,
		Question: fmt.Sprintf("The production target %q presents a NEW ssh host key (fingerprint %s). "+
			"Every production delivery is held until this key is deliberately approved — it is never trusted "+
			"silently. Approve ONLY if you can confirm the host was reinstalled (or the key otherwise changed "+
			"for a reason you know); a key that changed without cause is exactly the interception this check "+
			"guards against.", target, fp),
		Recommendation: "If the production machine was just reinstalled, verify this fingerprint against the host " +
			"out-of-band (e.g. on its console) and approve. Otherwise do NOT approve — investigate why the key changed.",
		Detail: fmt.Sprintf("host: %s\nnew key fingerprint: %s\n\nApproving records this exact key in the durable "+
			"known-hosts file. The acceptance re-reads the host's key at apply time and refuses to install it if it "+
			"no longer matches this fingerprint, so the approval covers this one key only.", target, fp),
	})
}

// prodHostKeyChangedText is the disturbance the user sees when the production host key changed — its
// OWN reason, stating the effect (production delivery is held) and the cause (a reinstalled or
// intercepted host), never a masked connection error.
func prodHostKeyChangedText(target, reason string) string {
	return fmt.Sprintf("production delivery is HELD: the ssh host key of the production target %q has CHANGED. "+
		"The machine was reinstalled, or the connection is being intercepted — this is NOT a plain connection "+
		"failure. Nothing is trusted automatically; approve the new key on the Blocked surface after confirming it, "+
		"and the held sends resume. (%s)", target, reason)
}

// orFirst keeps the FIRST error of a pass, so a later one does not mask the one that started it.
func orFirst(first, err error) error {
	if first == nil {
		return err
	}
	return first
}
