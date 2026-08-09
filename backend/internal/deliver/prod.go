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
	"strings"
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
	// ReceiverStale is set when the production host still carries a DIFFERENT version of the root receiver
	// scripts (devlab-deploy-recv / devlab-setup-lib.sh) than the merged delivery ships — measured from
	// the receiver's own self-report. The chain deliberately cannot install them (the deploy key is a
	// forced command that cannot overwrite its own gatekeeper), so this part of the delivery did NOT reach
	// the host: the send is not a full delivery and must NOT settle live. The production pass surfaces the
	// outstanding step as a deliberate approval on the Blocked surface, carrying the operator command and
	// the checksums that bind it. ReceiverTarget names the production host; ReceiverGrants pins each script
	// and the sha256 it must reach (the same (name, checksum) shape the wrapper renewal uses).
	ReceiverStale  bool
	ReceiverTarget string
	ReceiverGrants []runs.WrapperGrant
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

// LandscapeSource names the services that make up the Holistic landscape — the SOURCE OF TRUTH for
// what production must carry. It exists to break a circle: the reconciliation used to define "what
// production should carry" as "what the chain already delivered there", so a service the chain never
// shipped stood in no work-list, was never missed, and stayed silent. The roster is DERIVED from what
// the instance actually deploys (the edge routes, the way Atlas derives ports and stores nothing),
// never from the delivery history and never from a hand-kept list that goes stale.
//
// Services returns the FULL repository name (owner/repo) of every landscape member, in a deterministic
// order. It includes the two load-bearing members that carry no edge route of their own and so are
// invisible to a routes-only read: devlab itself (which holds Mercury and the chain — without it on
// production, production cannot even keep itself supplied) and the central dashboard (built
// differently from the Go services, but part of the landscape all the same). A repository that is no
// service is not a member: it carries no route, so it never enters the roster and raises nothing.
//
// A nil source, or an error from Services, leaves the roster unknown for that pass: the reconciliation
// then falls back to what production is KNOWN to have run (the ledger) and never guesses a service
// into or out of existence — the same "degrade to what can be read" stance the rest of the derivation
// takes.
type LandscapeSource interface {
	Services(ctx context.Context) ([]string, error)
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
func MaintainProd(ctx context.Context, prod ProdDeployer, landscape LandscapeSource, ledger *runs.DeliveryStore, prodState *runs.ProdStateStore, res *runs.ResultStore, n *runs.NoticeStore, pub live.Publisher, questions *runs.QuestionStore, hostkey HostKeyGate) error {
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
	// The identity of the host production actually is, read AFTER any approval was applied so a
	// just-reinstalled host reads as itself. A production-state record naming a DIFFERENT host key is a
	// claim about a machine that has been replaced — void, not stale (see recordProdCarries and
	// validProdRecords). "" means the identity could not be read (production unarmed, or the host is
	// unreachable this pass): the reading side then honours records as they stand rather than void a
	// valid record on a transient read failure, the same "degrade to what can be read" stance the
	// roster read takes.
	currentHostKey := currentProdHostKey(ctx, hostkey)
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
			recordProdCarries(prodState, d.Repo, out.Commit, currentHostKey, now)
			settleAfterProd(ledger, res, d)
			// The delivery is fully live — for the self repo that also PROVES the production host now carries
			// the receiver scripts it ships (DeployProd would have returned ReceiverStale otherwise). Retire
			// any receiver approval still open for this repository: the drift the operator was asked to close
			// is measured closed, so the question has done its job and drops off the Blocked surface.
			if questions != nil {
				_, _ = questions.RetireProdReceiverQuestions(d.Repo, "the production host now carries the receiver scripts this delivery ships; the delivery settled live")
			}
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
			// A CHANGED production host key, and a STALE production receiver, each get their OWN distinct
			// reason and a deliberate approval — never a masked "connection failed" or a false "shipped and
			// live" (task part 2). Everything else reports as a plain undelivered production send. All three
			// keep the merged layer beneath untouched.
			if out.HostKeyChanged {
				raiseHostKeyQuestion(ctx, questions, hostkey, out.HostKeyTarget, d.Repo)
				notify(n, runs.NoticeProdHostKeyChanged, d.Repo, prodHostKeyChangedText(out.HostKeyTarget, reason))
			} else if out.ReceiverStale {
				raiseReceiverQuestion(questions, d.Repo, out.ReceiverTarget, out.ReceiverGrants, out.Commit)
				notify(n, runs.NoticeProdUndelivered, d.Repo, prodReceiverUndeliveredText(out.ReceiverTarget))
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

	// After the booked deliveries, close BOTH silent gaps the landscape roster now makes visible:
	//   - a service that BELONGS to the landscape but is not on production at all (never delivered), and
	//   - a service production runs at an OLDER state than the standard branch, with no delivery to
	//     account for it (a hand-merged pull request).
	// Neither is visible from the ledger alone: the booked loop only ever fires for a merged delivery
	// that still owes production, so a service the chain never shipped — or one the standard branch
	// moved past on another path — stays silent. reconcileProd measures both against the roster and the
	// recorded production state, names each ("Kein stummes Ausbleiben"), and brings production up over
	// the SAME production path.
	reconcileProd(ctx, prod, landscapeRoster(ctx, landscape), all, prodState, currentHostKey, n, pub, questions, handled, now)
	return firstErr
}

// currentProdHostKey reads the identity of the production host as it is RIGHT NOW — the ssh host-key
// fingerprint the target currently presents — used to tell a production-state record about the host
// still there from one about a host that has been replaced. Reading a key is not trusting it (the
// gate's own ScanFingerprint only reads); trust is a separate, approved step. It returns "" when
// there is no gate (production unarmed) or the key cannot be read this pass, and the reading side then
// makes no identity judgement rather than void a valid record on a transient failure.
func currentProdHostKey(ctx context.Context, hostkey HostKeyGate) string {
	if hostkey == nil {
		return ""
	}
	fp, err := hostkey.ScanFingerprint(ctx)
	if err != nil {
		return ""
	}
	return fp
}

// landscapeRoster reads the landscape's service roster for this pass — the full repository names of
// every service production must carry, derived (edge routes + devlab + the dashboard), never the
// delivery history. A nil source or a read error yields no roster: the reconciliation then reasons
// only about what production is KNOWN to have run, rather than guess a service into or out of
// existence. The read failure is deliberately quiet — Atlas-style, the derivation degrades to what it
// can see and the next pass re-reads — and never masks a real production failure of this pass.
func landscapeRoster(ctx context.Context, landscape LandscapeSource) []string {
	if landscape == nil {
		return nil
	}
	svc, err := landscape.Services(ctx)
	if err != nil {
		return nil
	}
	return svc
}

// reconcileProd is the gap-closing pass: for every service the landscape expects on production but
// that the booked-delivery loop did NOT just handle, it MEASURES the standard-branch tip against the
// commit production is recorded to carry, and reports the truth honestly in three cases:
//   - MATCH: production carries exactly the standard branch — nothing at all, no message, no send.
//   - DRIFT: production runs the service but at an OLDER state than the standard branch (a change
//     reached the standard branch on another path). Named as a drift and brought current.
//   - MISSING: production does not run the service at all — no valid record puts it there (never
//     delivered, or delivered only to a host that has since been replaced). Named as missing and
//     delivered for the first time to the host now there, over the SAME production path.
//
// Both DRIFT and MISSING are brought up to the standard branch over the existing production send; no
// second path is built. Which of the two a service is in is decided by ONE reading — whether a VALID
// production-state record (bound to the host currently there) names it. A delivery's ProdDeployedAt
// is not consulted: it records the lifecycle of a send to whatever host existed then, so it must not
// answer "does production run this now".
//
// It only considers a service that is quiescent — no open, failed, or production-owing delivery,
// because those the chain itself is still driving. A read it cannot complete (the tip could not be
// resolved) is left for the next pass rather than guessed at; a reconciliation read failure is never
// the production pass's error, so it does not mask a real delivery failure.
func reconcileProd(ctx context.Context, prod ProdDeployer, roster []string, all []runs.Delivery, prodState *runs.ProdStateStore, currentHostKey string, n *runs.NoticeStore, pub live.Publisher, questions *runs.QuestionStore, handled map[string]bool, now time.Time) {
	if prodState == nil {
		return // nowhere to record what production carries — the reconciliation has no reference
	}
	// The SINGLE source of truth for "what does production carry": the commit recorded for each service,
	// read through the host-identity filter so a record about a REPLACED host counts as absent. Every
	// judgement below — even, drift, missing, and the in-flight hold in reconcileRepos — reads THIS map,
	// so no second path (a delivery's ProdDeployedAt, which records the OLD host's lifecycle) can claim
	// "production ran this before" after the host was rebuilt.
	carried := validProdRecords(prodState, currentHostKey)
	for _, repo := range reconcileRepos(roster, all, carried, handled) {
		if ctx.Err() != nil {
			return
		}
		tip, err := prod.DefaultBranchTip(ctx, repo)
		if err != nil || tip == "" {
			continue // could not measure the standard branch — leave it for the next pass, never guess
		}
		commit, ranBefore := carried[repo]
		if ranBefore && commit == tip {
			continue // production carries the standard branch — no drift, no message, no send
		}

		// Name the gap by its true kind. A service production is recorded to run but that now trails is a
		// DRIFT; a service with no valid record — never run, or run only by a host that has been replaced
		// — is MISSING and delivered for the first time to the host now there.
		if ranBefore {
			notify(n, runs.NoticeProdDrift, repo, prodDriftText(tip, commit))
		} else {
			notify(n, runs.NoticeProdMissing, repo, prodMissingText(tip))
		}
		publishDeliveries(pub)

		// Bring production up over the EXISTING production path (no second path is built). On proof it
		// runs, record the standard-branch commit production now carries so the next pass reads even.
		out, derr := prod.DeployProd(ctx, repo)
		if ctx.Err() != nil {
			return
		}
		switch {
		case derr == nil && out.Running:
			recordProdCarries(prodState, repo, out.Commit, currentHostKey, now)
			// The reconciled send is fully live — for the self repo it proves the host carries the receiver
			// scripts too, so any receiver approval still open for it is measured closed and retired.
			if questions != nil {
				_, _ = questions.RetireProdReceiverQuestions(repo, "the production host now carries the receiver scripts the standard branch ships; the reconciled send is live")
			}
			publishDeliveries(pub)
		case derr == nil && out.NotApplicable:
			// The roster listed this repository, yet the send proves it no service (a routed id whose
			// repository is a library, or a member that was reshaped). Nothing runs in production, so
			// there is nothing to keep even — and nothing further to report.
		case out.ReceiverStale:
			// The reconciled send shipped the service but the production host still runs an older receiver
			// than the standard branch ships — the chain cannot install it (task): name it its own way and
			// raise the same deliberate approval the booked loop does, never a masked undelivered.
			raiseReceiverQuestion(questions, repo, out.ReceiverTarget, out.ReceiverGrants, out.Commit)
			notify(n, runs.NoticeProdUndelivered, repo, prodReceiverUndeliveredText(out.ReceiverTarget))
			publishDeliveries(pub)
		default:
			// The gap is named, but production could not be brought current (an unreachable receiver,
			// an unconfigured target). Report it the same way a merged delivery's failed send is.
			notify(n, runs.NoticeProdUndelivered, repo, prodUndeliveredText(prodFailReason(out, derr)))
			publishDeliveries(pub)
		}
	}
}

// reconcileRepos is the work list of the reconciliation: every service the landscape expects on
// production, UNION every repository production is known to have run before, minus the ones the
// booked loop just handled or that an in-flight delivery holds back FOR A REASON THAT APPLIES. The
// landscape roster is the SOURCE OF TRUTH for what production should carry; the ledger-derived set
// is only a safety net, so a drift is still reconciled for a service the roster read happened to
// miss this pass. The order is stable (repository name) so a pass is deterministic.
//
// The hold is where absence and drift part ways. An open, in-flight delivery is a change on the way;
// waiting for it is right ONLY when production ALREADY runs the service and merely trails it (a
// DRIFT — production carries an older state, the delivery will replace it, so a send now would ship
// a state the delivery is about to supersede). It is WRONG when production is MISSING the service
// entirely: an absence is not made good by future work, so the standard branch is delivered NOW,
// open deliveries or not — otherwise every open delivery of a repository keeps a never-shipped
// service off production for as long as work keeps coming. A FAILED delivery never holds anything
// back: its failure concerns its own unmerged content, not the merged state production must carry;
// and a stuck delivery is not a delivery "in motion". A NeedsProd (merged, owing) delivery is
// already shipped by the booked loop above (it is in `handled`), so it needs no separate hold here.
//
// "Production already runs it" is measured the SAME way reconcileProd tells DRIFT from MISSING — a
// VALID recorded production-state commit (carried) — and from no other source. The two passes read
// the same map, so one never waits while the other delivers. A delivery's ProdDeployedAt is NOT
// consulted here: it records the lifecycle of a send to whatever host was there when it ran, so after
// a rebuild it would falsely claim "production runs this" for a machine that no longer exists — the
// exact parallel path this consolidation removes.
func reconcileRepos(roster []string, all []runs.Delivery, carried map[string]string, handled map[string]bool) []string {
	expected := map[string]bool{}
	for _, repo := range roster {
		if repo != "" {
			expected[repo] = true
		}
	}
	// The set of repositories production is KNOWN to run — a VALID recorded production commit for the
	// host currently there. Exactly the notion reconcileProd uses for DRIFT-vs-MISSING. A service
	// production runs is reconciled for drift even if the roster read happened to miss it this pass.
	onProd := map[string]bool{}
	for repo := range carried {
		onProd[repo] = true
		expected[repo] = true
	}
	// A repository held back this pass: an open, in-flight (not failed) delivery, but ONLY where
	// production already runs the service. Missing services fall through and are delivered.
	waiting := map[string]bool{}
	for _, d := range all {
		if d.OpenState() && !d.Failed() && onProd[d.Repo] {
			waiting[d.Repo] = true
		}
	}
	out := []string{}
	for repo := range expected {
		if handled[repo] || waiting[repo] {
			continue
		}
		out = append(out, repo)
	}
	sort.Strings(out)
	return out
}

// recordProdCarries records the standard-branch commit production now carries for a repository, BOUND
// to the identity of the host it carries it on (hostKey, the fingerprint read this pass). Binding the
// record to the host is what lets a later pass tell a live claim from a claim about a machine that has
// since been replaced. An empty commit is not recorded — an unproven state is worse than an absent
// one, since the reconciliation reads an absent record as "measure afresh" and a wrong one as "even".
// hostKey may be "" when the identity could not be read this pass; the record is still written (the
// commit is proven) and the reading side treats an unbound record as one it cannot vouch for.
func recordProdCarries(prodState *runs.ProdStateStore, repo, commit, hostKey string, now time.Time) {
	if prodState == nil || commit == "" {
		return
	}
	_ = prodState.Put(runs.ProdRecord{Repo: repo, Commit: commit, HostKey: hostKey, DeployedAt: now})
}

// validProdRecords is the reading-side judgement the passive pool must not make: it returns, per
// repository, the commit production is recorded to carry — but ONLY for records still bound to the
// host currently there. A record naming a DIFFERENT host key describes a machine that has been
// replaced (a rebuilt host presents a new key); that record is not stale but VOID, so it is dropped
// and the repository reads as absent — "measure afresh", which the reconciliation then does. This is
// the ONE place "what does production carry" is derived; every reader takes this map so no second
// path can disagree with it.
//
// currentHostKey "" means the host identity could not be read this pass (production unarmed, or the
// host unreachable). Identity cannot be judged then, so every record is honoured as it stands rather
// than voided wholesale — voiding a valid record on a transient read failure would re-deliver the
// whole landscape for nothing. A record with an empty HostKey (written before the binding existed, or
// on a pass whose identity read failed) matches no non-empty currentHostKey and so reads as absent:
// it is re-measured and re-recorded, this time bound to the host.
func validProdRecords(prodState *runs.ProdStateStore, currentHostKey string) map[string]string {
	out := map[string]string{}
	if prodState == nil {
		return out
	}
	recs, err := prodState.All()
	if err != nil {
		return out
	}
	for _, r := range recs {
		if currentHostKey != "" && r.HostKey != currentHostKey {
			continue // a claim about a host that has been replaced — void, measure afresh
		}
		out[r.Repo] = r.Commit
	}
	return out
}

// prodDriftText is the disturbance the user sees when the standard branch has advanced past what
// production carries. It states the effect (production runs an older state than the standard branch)
// and that the standard branch moved without a delivery — the governance finding — and that
// production is being brought current over the existing path. It is reached only with a KNOWN carried
// commit: a service with no valid record is MISSING (prodMissingText), never a drift, so there is no
// "unknown carried commit" drift case to word.
func prodDriftText(tip, carried string) string {
	return fmt.Sprintf("the standard branch advanced to %s but production carries %s — no recorded "+
		"delivery accounts for the change, so production ran an older state while the ledger read "+
		"'delivered'. Production is being brought up to the standard branch over the existing production "+
		"path. A change that reaches the standard branch outside a recorded delivery should be delivered "+
		"as an order.", shortSHA(tip), shortSHA(carried))
}

// prodMissingText is the disturbance the user sees when a service that belongs to the landscape is
// not on production at all. It states the effect (a landscape service is absent, so production is
// incomplete), the governance finding (it was never delivered, so nothing ever missed it — the exact
// silence the roster now breaks), and that it is being delivered for the first time over the existing
// production path.
func prodMissingText(tip string) string {
	return fmt.Sprintf("this service belongs to the landscape but is NOT on production — no delivery "+
		"ever put it there, so production has been running WITHOUT it and nothing flagged the gap. The "+
		"standard branch is at %s; the service is being delivered to production for the first time over "+
		"the existing production path so production carries the whole landscape, not only what happened "+
		"to be delivered before.", shortSHA(tip))
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

// raiseReceiverQuestion asks the user to bring the production host's root receiver scripts current —
// the SAME Blocked/approval path the wrapper renewal and the host-key change use (reuse the existing
// approval, do not build a second one beside it). Unlike those two, the chain performs NOTHING on this
// approval: it deliberately lacks the right to install the receiver on the production host (the deploy
// key is a forced command that cannot overwrite its own gatekeeper), so the question carries the exact
// one-line command an operator with root runs on the target and the checksums the scripts must reach,
// and the production pass re-measures the host before it settles the delivery live. It is asked once per
// repository while the SAME checksums stand; a newer delivery that changes the receiver again (different
// pinned checksums) retires the stale question and raises a fresh one so the surface always names the
// current version.
func raiseReceiverQuestion(questions *runs.QuestionStore, repo, target string, grants []runs.WrapperGrant, commit string) {
	if questions == nil || len(grants) == 0 {
		return
	}
	if existing, err := questions.OpenProdReceiverQuestion(repo); err == nil && existing != nil {
		if sameGrantSet(existing.Wrappers, grants) {
			return // the same version is already asked for — do not duplicate it
		}
		_ = questions.Resolve(existing.ID) // a newer version supersedes it — retire and re-ask below
	}
	command := prodReceiverCommand(commit)
	_, _ = questions.Raise(runs.Question{
		QKind:               runs.QuestionProdReceiver,
		Repo:                repo,
		RunTitle:            "Production receiver scripts",
		ProdReceiverTarget:  target,
		ProdReceiverCommand: command,
		Wrappers:            grants,
		Question: fmt.Sprintf("The production host %q still runs an OLDER version of the root receiver scripts "+
			"(%s) than the merged delivery ships. The delivery chain cannot install them itself — the deploy key is a "+
			"forced command that can only rsync into staging and trigger an install, never overwrite its own gatekeeper "+
			"— so this part of the delivery has NOT reached production and the delivery is held NOT live. Bring the "+
			"scripts current with the one command below, then it settles live on its own.", target, grantNames(grants)),
		Recommendation: "Run the command below on the production host as root, from a checkout of the merged standard " +
			"branch. It installs exactly the reviewed bytes and prints their sha256, so you can check the installed scripts " +
			"against the checksums here.",
		Detail: fmt.Sprintf("command: %s\n\nscripts that must reach the production host, each pinned to the checksum "+
			"the merged delivery ships:\n%s", command, renderReceiverGrants(grants)),
	})
}

// prodReceiverCommand is the ONE line an operator with root runs on the production host to bring the
// root receiver scripts current — the existing operator installer (devlab-install-recv), which installs
// exactly the reviewed bytes beside it and prints their sha256 (Handarbeit kommt als Skript; reuse
// before build). The merged commit is named so the operator checks out the exact standard-branch state.
func prodReceiverCommand(commit string) string {
	return "sudo ./deploy/devlab-install-recv   # on the production host, as root, from a checkout of the standard branch at " + shortSHA(commit)
}

// grantNames lists the receiver scripts for a one-line sentence.
func grantNames(grants []runs.WrapperGrant) string {
	names := make([]string, len(grants))
	for i, g := range grants {
		names[i] = g.Name
	}
	return strings.Join(names, ", ")
}

// renderReceiverGrants renders each receiver script and the checksum it must reach, one per line, for
// the question detail a human reads before running the command.
func renderReceiverGrants(grants []runs.WrapperGrant) string {
	var b strings.Builder
	for _, g := range grants {
		b.WriteString("  - " + g.Name + " → sha256 " + g.SHA)
		if g.Summary != "" {
			b.WriteString(" (" + g.Summary + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sameGrantSet reports whether two grant sets pin the same files to the same checksums — the test for
// "the same receiver version is already asked for", so a repeated pass does not raise a duplicate.
func sameGrantSet(a, b []runs.WrapperGrant) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]string, len(a))
	for _, g := range a {
		m[g.Name] = g.SHA
	}
	for _, g := range b {
		if m[g.Name] != g.SHA {
			return false
		}
	}
	return true
}

// prodReceiverUndeliveredText is the disturbance the user sees when the merged work reached production
// as a running service but the root receiver scripts it also ships did not — because the chain cannot
// install them. It states the effect (held NOT live), the cause, and where the exact command waits.
func prodReceiverUndeliveredText(target string) string {
	return fmt.Sprintf("the merged work has NOT fully reached production: the production host %q still runs an OLDER "+
		"version of the root receiver scripts than this delivery ships, and the chain cannot install them itself (the "+
		"deploy key is a forced command that cannot overwrite its own gatekeeper). The delivery is held NOT live until "+
		"an operator brings the receiver current — the exact command waits on the Blocked surface. The merged work on "+
		"the default branch is unaffected.", target)
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
