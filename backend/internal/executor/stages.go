// The five stage implementations referenced by the frozen chain list (chain.go). Each stage is
// idempotent per ARCHITEKTUR §6.3: effect · proof probe · repeat safety. not-applicable is
// only ever returned WITH attested repo evidence (REQ-031.3); the motor refuses evidence-free
// skips.
package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"devlab/backend/internal/faultclass"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/telemetry"
)

// transientMaxAttempts bounds every transient retry loop in the chain (K-5): after this many
// growing intervals the operation is honestly blocked and waits for explicit resumption.
var transientMaxAttempts = 4

// replaceConstitution is mercury.ReplaceConstitutionBlock behind a seam variable so tests can
// substitute the renderer; production always uses the ONE renderer (B6).
var replaceConstitution = mercury.ReplaceConstitutionBlock

func isSatisfied(err error) bool { return faultclass.Classify(err) == faultclass.Satisfied }

func asBlocked(err error) *faultclass.BlockedError {
	var be *faultclass.BlockedError
	if errors.As(err, &be) {
		return be
	}
	return nil
}

// ownsFailedTip reports whether the failed tip is THIS order's own work — the resolve-at-the-tip
// case. Preflight surfaces the order's own unsettled delivery as OpenDelivery (joined by run, not
// by execution), so a tip whose id matches it belongs to this order and must not hold it: the rest
// path adopts it and re-delivers. Any other failed tip belongs to an earlier order and holds this one.
func ownsFailedTip(f preflight.Finding, tip *runs.Delivery) bool {
	return f.OpenDelivery != nil && tip != nil && f.OpenDelivery.ID == tip.ID
}

// heldOnFailedTip holds an order whose repository's tip is another order's failed delivery. It is a
// deliberate wait, not a fault: the order sits blocked with a named reason (the two ways back are
// spelled out) and stays in the open ToDo list until the tip is resolved or rolled back. It reuses
// the honest-blockade surfacing (K-5) so no second held state stands beside it.
func heldOnFailedTip(rc *RepoCtx, tip *runs.Delivery) error {
	now := rc.Deps.Now().UTC()
	reason := fmt.Sprintf(
		"held before branching: the delivery at the tip of %s failed and is not resolved yet — delivery %s on branch %s (%s). "+
			"No new order is branched past a failed layer. Resolve it by re-running the failed order, or roll it back in Deliveries, then resume this order.",
		rc.Repo, tip.ID, tip.Branch, nonEmpty(tip.FailedReason, "delivery failed"))
	return &faultclass.BlockedError{Backoff: model.Backoff{
		Reason: reason, Class: "held-on-failed-tip", Attempts: 1, FirstAt: now, LastAt: now, NextAt: now,
	}}
}

// heldOnOpenQuestion holds an order whose repository carries another order's unanswered question. It
// is the SAME deliberate wait as a failed tip, reusing the honest-blockade surfacing (K-5): the
// order sits blocked with a named reason and stays in the open ToDo list until the question is
// answered — no new work is branched past a decision the user has not made.
func heldOnOpenQuestion(rc *RepoCtx, q *runs.Question) error {
	now := rc.Deps.Now().UTC()
	reason := fmt.Sprintf(
		"held before branching: %s has an open question from run %q awaiting the user's answer — %q. "+
			"No new order is branched past an unanswered question. Answer it in the Blocked tab, then this order continues.",
		rc.Repo, nonEmpty(q.RunTitle, q.RunID), firstLine(q.Question))
	return &faultclass.BlockedError{Backoff: model.Backoff{
		Reason: reason, Class: "held-on-open-question", Attempts: 1, FirstAt: now, LastAt: now, NextAt: now,
	}}
}

// catchUpBranch rebases this order's branch onto the current stack tip when it rests on an older
// layer (the understack measured by preflight). It is the ONE place all four ways a branch goes
// stale are handled, so the fix does not hang off "resume" alone. Rules:
//   - nothing behind (or nothing measured) ⇒ no-op (a fresh branch is cut from the tip already);
//   - a RECORDED open delivery ⇒ left as it stands (that is a re-delivery of a fixed span, not
//     weiterbauen — rebasing it would rewrite an already-opened PR's branch);
//   - a clean catch-up ⇒ the branch is moved and the fact is marked for the agent prompt (no alarm);
//   - a CONFLICT ⇒ the branch is left EXACTLY as it was (the workbench aborted fully) and the order
//     is held with a named reason listing the files, reusing the honest-blockade surfacing (K-5).
func (rc *RepoCtx) catchUpBranch(ctx context.Context, wb WorkbenchOps, tipBranch string) error {
	u := rc.Finding.Understack
	if u == nil || u.Behind == 0 {
		return nil // already on the tip, or nothing to measure — nothing happens
	}
	if rc.Finding.OpenDelivery != nil {
		return nil // a recorded open delivery is re-shipped as its fixed span, not rebuilt on the tip
	}
	info, err := wb.CatchUpOnto(ctx, tipBranch)
	if err != nil {
		return fmt.Errorf("catch the branch up onto the current tip: %w", err)
	}
	if info.Conflicted {
		return rc.heldOnCatchUpConflict(u, info)
	}
	if info.Rebased {
		u.CaughtUp = true
		rc.logf("caught up: rebased %s from %s onto %s@%s — %d delivery/deliveries landed since",
			rc.branchName(), short(info.OldHead), u.TipBranch, short(info.NewHead), len(u.Intervening))
	}
	return nil
}

// heldOnCatchUpConflict holds an order whose branch could not be rebased onto the current tip
// without conflict. The workbench already aborted the rebase FULLY (the branch is byte-for-byte as
// before), so this is a clean, resumable wait — not a failed delivery and not a corrupted branch.
// It reuses the honest-blockade surfacing (K-5): a named reason on the Blocked surface listing the
// conflicting files, exactly like a failed tip or an open question holds a repository.
func (rc *RepoCtx) heldOnCatchUpConflict(u *preflight.Understack, info CatchUpInfo) error {
	now := rc.Deps.Now().UTC()
	files := "the branch's files"
	if len(info.ConflictFiles) > 0 {
		files = strings.Join(info.ConflictFiles, ", ")
	}
	reason := fmt.Sprintf(
		"catch-up blocked: this order's branch sits %d commit(s) behind the current tip %s and could not be "+
			"rebased onto it without conflict — the branch was left exactly as it was (@%s), nothing half-applied. "+
			"Conflicting files: %s. Resolve the conflict against the current tip, then resume this order.",
		u.Behind, u.TipBranch, short(info.OldHead), files)
	return &faultclass.BlockedError{Backoff: model.Backoff{
		Reason: reason, Class: "catch-up-conflict", Attempts: 1, FirstAt: now, LastAt: now, NextAt: now,
	}}
}

// raiseQuestion is the halt at the top of the stack when THIS run asks a question. It preserves the
// work already finished (commit loose + publish, K-1), records the question on the Blocked surface
// (which delivers it to the user), persists the continuation so the answer resumes this exact spot,
// and returns the honest block. It deliberately mints NO delivery id: the repo is held by the open
// question, not by a "Lieferung gescheitert" mark, so exactly one hold stands, never two.
func (rc *RepoCtx) raiseQuestion(ctx context.Context, wb WorkbenchOps, question, recommendation, report string) error {
	// Keep what the agent already finished before it stopped to ask.
	if dirty, derr := wb.HasUncommitted(ctx); derr == nil && dirty {
		if _, cerr := wb.CommitAll(ctx, "mercury-run: "+rc.Run.Title+" (work in progress — awaiting an answer)"); cerr != nil {
			rc.logf("could not commit work in progress before asking (it stays in the working tree): %s", firstLine(cerr.Error()))
		}
	}
	_ = wb.Publish(ctx)

	// Retire any decision question a since-ended execution of THIS order left for this repo, so the
	// fresh one below is the ONE current question and no stale, un-redeemable blocker lingers.
	_, _ = rc.Deps.Questions().WithdrawForRun(ctx, rc.Run.ID, rc.Repo, runs.QuestionDecision)

	q := runs.Question{
		RunID:          rc.Run.ID,
		RunTitle:       rc.Run.Title,
		Kind:           rc.Run.Kind,
		ExecutionID:    rc.Doc.ID,
		Repo:           rc.Repo,
		QKind:          runs.QuestionDecision,
		Autonomy:       rc.Run.Autonomy.Resolve(),
		Question:       question,
		Recommendation: recommendation,
		Progress:       clip(report),
		AskedBy:        model.Actor{Autonomous: true, OnBehalfOf: rc.Doc.Requested.Created.User},
	}
	if _, err := rc.Deps.Questions().Raise(ctx, q); err != nil {
		// A question that could not be recorded must NOT vanish into a silent guess: fail the stage
		// with the named reason so the repo blocks and the operator sees why.
		return fmt.Errorf("could not record the run's question (the run does not guess past it): %w", err)
	}
	// Resume continues the SAME agent conversation at implement (session.Resume), so persist the
	// continuation point; the block moves the execution to "blocked" and the repo to RepoBlocked.
	rc.Sink.Continuation(model.ContinuationView{Repo: rc.Repo, Stage: model.StageImplement})
	rc.logf("asked the user a question and stopped — the repository is blocked until it is answered: %s", firstLine(question))
	now := rc.Deps.Now().UTC()
	reason := fmt.Sprintf("waiting for your answer: %s", firstLine(question))
	if recommendation != "" {
		reason += " (recommendation: " + firstLine(recommendation) + ")"
	}
	return &faultclass.BlockedError{Backoff: model.Backoff{
		Reason: reason, Class: "awaiting-answer", Attempts: 1, FirstAt: now, LastAt: now, NextAt: now,
	}}
}

// raiseWrapperQuestion is THE FREED HANDLE (point 5): the self delivery refused because the root
// wrapper scripts under /usr/local/sbin are not what the delivered code expects, so the run asks the
// user to approve renewing them and blocks. It offers EXACTLY the content pinned per file to its
// checksum, and mints no delivery id (the repo is held by the open question, not by a failed delivery).
//
// ONE AUTHORITATIVE REFERENCE — the branch being delivered (task point 2). The self-delivery gate
// (deploy.GuardWrappersCurrent) proves that what is INSTALLED equals what THIS run DELIVERS, and what
// it delivers is its own task branch. So the delivering branch is the single stand the installed root
// scripts must match while a run changes them, and the renewal offers that branch's content. There is
// deliberately NO second yardstick: an earlier design ALSO measured the STACK TIP and preferred its
// content, so an approval that satisfied the tip check violated the delivering-branch gate the very
// next moment — the user drove a pendulum that no answer could ever stop (BEFUND 1). Two checks that
// measured the same installed scripts against different stands were the real redundancy; the stack-tip
// stand is gone from this path, because the delivering branch is what deliver-dev is proven against.
//
// A QUESTION IS ONLY POSED IF AN ANSWER RESOLVES THE STATE (task point 1). Before it asks, two guards
// turn an unanswerable state into a NAMED disturbance instead of a click that changes nothing:
//   - the gate refused as stale, yet the delivering branch shows NO difference from the installed
//     scripts: the two checks contradict and nothing is approvable — a Störung, not a question (test a);
//   - this order already approved EXACTLY this content, yet the scripts are still not it: the approval
//     was ineffective (a physical install failure — BEFUND 3), so the ≥2nd identical question is presumed
//     wirkungslos (tasks point 3 & 5) and the real reason is named instead (test b).
//
// SAFETY. Approval DOES install, but a forged approval installs nothing: the content must still hash to
// the approved checksum, and the grant the root tool reads sits in a daemon-owned, run-unwritable
// place. The run authored the delivering-branch bytes, so the human — not a merge — is the gate; that
// is admissible because the same four bindings hold (sha, single-use, run-unwritable approval,
// re-read-after-approval). They are spelled out on deploy.DeliveringBranchContent. The `drift` argument
// is the gate's original refusal text, kept for the log line and the disturbance reason.
func (rc *RepoCtx) raiseWrapperQuestion(ctx context.Context, drift string) error {
	now := rc.Deps.Now().UTC()
	// The AUTHORITATIVE requirement: the (file, checksum) set the gate itself measures — the delivering
	// branch. Installing exactly this content is the ONE answer that makes the gate pass.
	grants, werr := rc.Deps.Deploy().WorkingWrapperDrift(ctx, rc.Repo)
	if werr != nil {
		return fmt.Errorf("%w (could not read this run's delivering-branch wrapper scripts: %v)", ErrWrappersStale, werr)
	}
	if len(grants) == 0 {
		// TASK POINT 1 / TEST a: the gate refused as stale, but the stand it measures against — this
		// run's delivering branch — shows no difference from the installed scripts. Two checks disagree
		// about the same scripts, so NO approval could resolve it. That is not a question; name the
		// contradiction as a disturbance and hold, rather than hand the user a click that resolves nothing.
		return rc.wrapperNotAnswerable(ctx,
			"the self delivery refused because the installed root wrapper scripts are stale, but this run's "+
				"delivering branch — the very stand the delivery is measured against — shows no difference from "+
				"the installed scripts. Two checks disagree about the same scripts, so no approval could resolve "+
				"it. Underlying refusal: "+firstLine(drift),
			"wrapper-check-contradiction")
	}
	// TASK POINTS 3 & 5 / TEST b: has this ORDER already been asked AND answered for EXACTLY this
	// content, with the drift still standing? Then the approval did not take effect — a granted approval
	// whose install physically failed (BEFUND 3), not a fresh decision. A second identical question is
	// presumed wirkungslos, so escalate to a disturbance with the real reason instead of re-asking. A
	// prior approval for DIFFERENT content is NOT this case: the source genuinely moved, and a fresh
	// question for the new content is correct.
	if prior, perr := rc.Deps.Questions().AnsweredForRun(ctx, rc.Run.ID, rc.Repo); perr == nil && prior != nil &&
		prior.QKind == runs.QuestionWrapperRenewal && prior.Approved && sameWrapperGrants(prior.Wrappers, grants) {
		return rc.wrapperNotAnswerable(ctx,
			"you already approved renewing these exact root wrapper scripts for this run, but the installed "+
				"scripts are still not the approved version — the approval did not take effect (most likely the "+
				"install could not write /usr/local/sbin). Asking the same question again would change nothing; "+
				"the reason the install did not apply must be fixed first.",
			"wrapper-approval-ineffective")
	}
	// A prior answer for DIFFERENT content is stale — consume it so the surface shows the ONE current
	// question, not a settled-but-unfixed one.
	rc.consumeWrapperQuestion(ctx)
	q := runs.Question{
		RunID: rc.Run.ID, RunTitle: rc.Run.Title, Kind: rc.Run.Kind,
		ExecutionID: rc.Doc.ID, Repo: rc.Repo,
		QKind: runs.QuestionWrapperRenewal, Autonomy: rc.Run.Autonomy.Resolve(),
		Question:       wrapperQuestionText(),
		Recommendation: wrapperRecommendation(),
		Detail:         renderWrapperGrants(grants),
		Wrappers:       grants,
		WrapperSource:  runs.WrapperSourceWorking,
		AskedBy:        model.Actor{Autonomous: true, OnBehalfOf: rc.Doc.Requested.Created.User},
	}
	if _, err := rc.Deps.Questions().Raise(ctx, q); err != nil {
		return fmt.Errorf("%w (the wrapper-renewal question could not be recorded: %v)", ErrWrappersStale, err)
	}
	rc.logf("root wrappers differ from this run's delivering branch — asked the user to approve renewing them; nothing was installed (guard: %s)", firstLine(drift))
	return &faultclass.BlockedError{Backoff: model.Backoff{
		Reason: "waiting for your approval to renew the root wrapper scripts under /usr/local/sbin — approve on the Blocked surface; the approved content is then installed and verified",
		Class:  "awaiting-wrapper-approval", Attempts: 1, FirstAt: now, LastAt: now, NextAt: now,
	}}
}

// wrapperNotAnswerable turns an UNANSWERABLE wrapper situation into a named disturbance instead of a
// question (task points 1, 3, 5). A question whose answer cannot resolve the state is a Störung, not a
// decision: it retires this order's wrapper questions (so the Blocked surface shows the disturbance,
// not a click that resolves nothing), records the disturbance with the REAL reason, and holds the repo
// with that reason. Like a question it mints no delivery and holds the tip until the named cause is
// fixed — but it never asks, because no approval would clear it. The rejection of the question (or
// resolving the named cause and resuming) frees the tip.
func (rc *RepoCtx) wrapperNotAnswerable(ctx context.Context, reason, class string) error {
	rc.consumeWrapperQuestion(ctx)
	rc.Sink.Notice(NoticeEvent{
		Kind:     questionUnresolvableNotice,
		Repo:     rc.Repo,
		Text:     reason,
		NextStep: "this is a contradiction, not a decision — no approval can clear it; fix the named cause, then resume",
	})
	rc.logf("wrapper renewal is not answerable (%s) — recorded as a disturbance, no question posed: %s", class, reason)
	now := rc.Deps.Now().UTC()
	return &faultclass.BlockedError{Backoff: model.Backoff{
		Reason: "wrapper renewal cannot be resolved by a question — " + reason,
		Class:  class, Attempts: 1, FirstAt: now, LastAt: now, NextAt: now,
	}}
}

// sameWrapperGrants reports whether two grant sets pin the SAME files to the SAME checksums (order
// independent) — the test for "this exact content was already approved". It is what lets a repeated
// approval of identical content be recognised as ineffective (points 3 & 5) while a genuinely changed
// source still raises a fresh question.
func sameWrapperGrants(a, b []runs.WrapperGrant) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]string, len(a))
	for _, g := range a {
		seen[g.Name] = g.SHA
	}
	for _, g := range b {
		if sha, ok := seen[g.Name]; !ok || sha != g.SHA {
			return false
		}
	}
	return true
}

// wrapperQuestionText is the wording the user reads. It names WHY the scripts must be renewed — the
// installed scripts no longer match the version this run delivers — so a human can decide without
// opening the branch (task point 4).
func wrapperQuestionText() string {
	return "Renew the root wrapper scripts under /usr/local/sbin to the version this delivery ships? " +
		"The installed scripts no longer match this run's delivering branch, so the daemon cannot be " +
		"installed until you approve installing the delivered version of the changed scripts."
}

// wrapperRecommendation is the run's own proposed answer, stating what approval installs and why the
// approval stays safe (single-use, content-pinned).
func wrapperRecommendation() string {
	return "These scripts run as root, so this decision is always yours — it is never answered " +
		"automatically, not even at the autonomous level. Approving installs EXACTLY the delivered " +
		"version of the named files with the named checksums below — nothing else. The approval is " +
		"single-use: if the delivering branch changes afterwards, it installs nothing."
}

// renderWrapperGrants shows the user the exact difference the approval covers: each root wrapper that
// would be renewed and the checksum of the content it would be renewed to (task point 4).
func renderWrapperGrants(grants []runs.WrapperGrant) string {
	var b strings.Builder
	b.WriteString("These root wrapper scripts will be renewed to the version this delivery ships:\n")
	for _, g := range grants {
		if g.Summary != "" {
			fmt.Fprintf(&b, "  - %s — %s (sha256 %s)\n", g.Name, g.Summary, g.SHA)
		} else {
			fmt.Fprintf(&b, "  - %s → sha256 %s\n", g.Name, g.SHA)
		}
	}
	b.WriteString("These scripts run as root. Only these files, with these exact checksums, are installed; the approval is single-use.")
	return b.String()
}

// applyApprovedWrapperRenewal is the WRITE half's trigger on resume: if the ORDER's own
// wrapper-renewal question was answered AND approved, install the approved wrappers (from the stack tip
// or this run's delivering branch, per the question's source) through the root tool BEFORE the delivery
// re-checks the guard. The lookup keys by RUN, not execution: an approval belongs to the order, so a
// LATER execution (after a restart minted a fresh execution and a fresh question) still redeems the
// earlier approval — the very gap that let an approved renewal go unread once its execution had ended.
// It is idempotent — a renewal already applied (or partly applied) is skipped per file in the deploy
// layer — and a failure never marks the approval consumed, so the delivery simply re-checks and may ask
// again. The content-pin is the safety: the approval installs only bytes that still hash to the approved
// checksum, so a source that changed after the approval installs nothing and the delivery re-asks.
func (rc *RepoCtx) applyApprovedWrapperRenewal(ctx context.Context) {
	q, err := rc.Deps.Questions().AnsweredForRun(ctx, rc.Run.ID, rc.Repo)
	if err != nil || q == nil || q.QKind != runs.QuestionWrapperRenewal || !q.Approved || len(q.Wrappers) == 0 {
		return
	}
	if err := rc.Deps.Deploy().RenewApprovedWrappers(ctx, rc.Repo, *q); err != nil {
		rc.logf("could not renew the approved root wrapper scripts (%v) — the delivery re-checks and may ask again; your approval is not spent", err)
		return
	}
	names := make([]string, len(q.Wrappers))
	for i, g := range q.Wrappers {
		names[i] = g.Name
	}
	rc.logf("renewed root wrapper scripts from this run's delivering branch under your approval: %s", strings.Join(names, ", "))
	if err := rc.Deps.Questions().Resolve(ctx, q.ID); err != nil {
		rc.logf("root wrappers renewed but the question could not be marked resolved: %v", err)
	}
}

// consumeWrapperQuestion retires the ORDER's own wrapper-renewal question(s) for this repo — open OR
// answered — so exactly one current question stands on the Blocked surface. It keys by RUN, not
// execution, so a question a since-ended execution left behind is retired too (never a lingering
// second blocker); raising a fresh wrapper question calls it first, and a successful delivery calls
// it to clear the answered approval once the renewal has taken effect (best-effort).
func (rc *RepoCtx) consumeWrapperQuestion(ctx context.Context) {
	_, _ = rc.Deps.Questions().WithdrawForRun(ctx, rc.Run.ID, rc.Repo, runs.QuestionWrapperRenewal)
}

// answerContinuation frames the user's answer as the next turn of the run's own agent conversation.
// The agent already holds the full context (the conversation is resumed), so this is only the answer
// plus the reminder that the same ask policy still applies — a further genuine fork may ask again.
func answerContinuation(q runs.Question) string {
	var b strings.Builder
	b.WriteString("The user has answered the question you raised.\n\n")
	b.WriteString("Your question was:\n")
	b.WriteString(q.Question)
	b.WriteString("\n\nThe user's answer:\n")
	b.WriteString(q.Answer)
	b.WriteString("\n\nContinue the task from where you stopped, applying this answer. Do not start over. " +
		"If you reach another genuine decision you may ask again with the same question block; otherwise " +
		"finish and commit your work.")
	return b.String()
}

// ── preflight ────────────────────────────────────────────────────────────────────────────
//
// Effect: none (observation; the B-4 admission gate already ran BEFORE the document existed —
// this stage re-derives for prompt + protocol). Unreachable sources follow the REQ-032 policy:
// growing retries, then honestly blocked — never a guess (K-3).

func preflightApplies(ctx context.Context, rc *RepoCtx) (bool, string) { return true, "" }

func preflightRun(ctx context.Context, rc *RepoCtx) error {
	var f = rc.Finding
	err := faultclass.Retry(ctx, &model.Backoff{}, transientMaxAttempts, func() error {
		var derr error
		f, derr = rc.Deps.Preflight(ctx, rc.Repo, rc.Run)
		return derr
	})
	if err != nil {
		return fmt.Errorf("preflight sources unreachable: %w", err)
	}
	rc.Finding = f
	rc.logf("task state: %s", f.State)
	for _, ev := range f.Evidence {
		rc.logf("evidence: %s", ev)
	}
	if f.OpenPR != nil {
		rc.link = f.OpenPR.URL
	}

	// The understack: how this task's branch sits against the CURRENT stack tip (REQ point 1). It is
	// MEASURED, not vermutet — the fork commit, the distance behind the tip, and the deliveries that
	// landed in between all stand in the preflight protocol, so a branch resting on an older layer is
	// visible before the implement stage catches it up. An unreachable measurement is named and never
	// a guess; a behind branch is the NORMAL case of a growing stack, so it raises no alarm here.
	if u, uerr := rc.Deps.StackPosition(ctx, rc.Repo, rc.Run); uerr != nil {
		rc.logf("understack not measured (the branch is caught up when it is next worked on): %s", firstLine(uerr.Error()))
	} else if u != nil {
		rc.Finding.Understack = u
		switch {
		case u.Behind > 0:
			rc.logf("understack: this task's branch forks at %s and sits %d commit(s) behind the current tip %s@%s — it is caught up before more work is built on it",
				short(u.ForkCommit), u.Behind, u.TipBranch, short(u.TipCommit))
			for _, d := range u.Intervening {
				rc.logf("  landed since: %s (%s)", d.Title, d.Branch)
			}
		default:
			rc.logf("understack: this task's branch already sits on the current tip %s@%s — nothing to catch up", u.TipBranch, short(u.TipCommit))
		}
	}
	return nil
}

// ── implement ────────────────────────────────────────────────────────────────────────────
//
// Effect: agent commits on the workbench; publish after every commit round (K-1); attachments
// staged via the workspace manifest and removed BEFORE any commit (B-6); the CLAUDE.md
// constitution block is kept current via mercury.ReplaceConstitutionBlock as part of the
// delivery (B6 renders, B3 calls). On the rest path (implemented-undelivered) NOTHING new is
// created — only the span of the existing work is established (REQ-020.3).

func implementApplies(ctx context.Context, rc *RepoCtx) (bool, string) {
	if rc.Finding.State == model.TaskDelivered {
		return false, deliveredEvidence(rc)
	}
	return true, ""
}

// stage-vocabulary: the stage compared below is the continuation stamp of THIS execution's own
// state document, written by this chain in the chain's own vocabulary. The archive holds no
// continuation at all — it is a closed past, and nothing in it is ever resumed.
func implementRun(ctx context.Context, rc *RepoCtx) error {
	deps := rc.Deps
	wb := deps.Workbench(rc.Repo)

	// REQ-006.2/033.6: a target that names a NOT yet existing repository is created first —
	// and the creation sets the full protection in the SAME pass; failing to set it fails
	// the creation.
	if rc.Target.Create {
		if err := ensureRepoCreated(ctx, rc); err != nil {
			return err
		}
	}

	// A failed order becomes the tip and the stack does not grow past it: BEFORE this order branches,
	// the tip is measured. A failed tip that is NOT this order's own work HOLDS the order — it waits,
	// named, in the open ToDo list — instead of silently branching past the failed layer onto an
	// older, incomplete state (the very gap that let six orders sit on six branches with none carrying
	// all). This order's OWN failed tip is the resolve-at-the-tip path: the rest path below adopts it
	// and re-delivers, so it is never held by it.
	if tip, terr := deps.Deliver().FailedTip(ctx, rc.Repo); terr == nil && tip != nil && !ownsFailedTip(rc.Finding, tip) {
		return heldOnFailedTip(rc, tip)
	}

	// An open question is a tip whose state is not settled — it holds the repository exactly like a
	// failed delivery. BEFORE this order branches, a still-open question raised by ANOTHER order holds
	// it: no new work is stacked past a decision the user has not made yet. The order's OWN question
	// never holds it — the exclusion is by RUN, not execution, so even a later execution (after a
	// restart) is not deadlocked behind a question one of the order's earlier executions raised; it
	// resumes, redeems the answer, or re-asks below.
	if q, qerr := deps.Questions().OpenForRepo(ctx, rc.Repo, rc.Run.ID); qerr == nil && q != nil {
		return heldOnOpenQuestion(rc, q)
	}

	// One branch per TASK, named after the run, in every repository it targets. The name records
	// the owner, so what lies on the branch can never again be mistaken for somebody else's work.
	taskBranch := runs.TaskBranch(rc.Target.Create, rc.Run.Title, rc.Run.ID)
	// The task's branch is cut from the TOP OF THE STACK — the branch of the task before it in this
	// repository, or the default branch when none is open. So the tasks build on each other in the
	// order they ran, and each one's pull request shows only its OWN change against the one before.
	// Its OWN branch is excluded as the base: a re-run whose earlier delivery is still open must
	// not stack this task on itself.
	stackBase, err := rc.Deps.Deliver().NextPRBase(ctx, rc.Repo, taskBranch)
	if err != nil {
		return fmt.Errorf("resolve the base of this task's branch: %w", err)
	}
	prep, err := wb.Prepare(ctx, taskBranch, stackBase)
	if err != nil {
		return fmt.Errorf("prepare workbench: %w", err)
	}
	rc.prepared, rc.prep = true, prep
	if prep.Conflicted {
		// K-1/REQ-023.2: a fold-in conflict is NAMED, nothing is reset; the run proceeds on
		// the local state per the run rules.
		rc.logf("fold-in conflict named (nothing reset; proceeding on the local state): %s",
			strings.Join(prep.ConflictFiles, ", "))
	}
	if err := wb.CleanUntracked(ctx); err != nil {
		return fmt.Errorf("clean untracked leftovers: %w", err)
	}
	// Measure the branch actually worked on so every log line names the truth, not a constant.
	if b := wb.CurrentBranch(ctx); b != "" {
		rc.branch = b
	}

	// A RESTING BRANCH IS CAUGHT UP ONTO THE STACK TIP BEFORE MORE WORK IS BUILT ON IT. A branch cut
	// from an older layer would otherwise measure its delivery against a state that no longer exists;
	// so it is rebased onto the current tip here, before the agent runs — the ONE place all four ways
	// a branch goes stale (paused, deferred, a failed delivery re-run days later, an implemented ToDo
	// left lying) pass through. A clean catch-up is the normal case of a growing stack (no alarm, no
	// failed tip); only a conflict halts the order, and then the branch is left EXACTLY as it was.
	if err := rc.catchUpBranch(ctx, wb, stackBase); err != nil {
		return err
	}

	head, err := wb.Head(ctx)
	if err != nil {
		return fmt.Errorf("read workbench head: %w", err)
	}
	// The delivery base is the head AFTER the fold and any catch-up, so the span holds exactly the
	// agent's own commits — the range a counter-booking later reverses.
	rc.deliveryBase, rc.head = head, head

	// Rest path (K-3/REQ-020.3): already implemented ⇒ create nothing new; only establish the
	// span of the existing, undelivered work so the remaining stages can walk it. EXCEPTION:
	// a continuation that stopped INSIDE implement resumes the agent session instead — its
	// half-done work must be finished, not delivered half-way.
	resumingImplement := rc.session.Resume && rc.Doc.Continuation != nil && rc.Doc.Continuation.Stage == model.StageImplement
	if rc.Finding.State == model.TaskImplementedUndelivered && !resumingImplement {
		return implementRestPath(ctx, rc, wb)
	}

	// Stage the attachments (B-6): they reach the agent verifiably (manifest in the prompt)
	// and are removed again before ANY commit — deferred and explicit, idempotent.
	manifest := ""
	cleanup := func() error { return nil }
	if len(rc.Run.Attachments) > 0 {
		manifest, cleanup, err = deps.StageAttachments(ctx, rc.Repo, rc.Run.Attachments)
		if err != nil {
			return fmt.Errorf("stage attachments: %w", err)
		}
	}
	defer func() { _ = cleanup() }()

	// The examined stand of THIS repository (read per repo, never folded into the shared snapshot):
	// without it every prompt falls back to "never examined ⇒ examine everything".
	prompt := AssemblePrompt(rc.Run.PromptSnapshot, rc.Run.Autonomy, rc.Finding, deps.AxiomScope(ctx, rc.Repo, rc.Run), manifest)
	// A resume that carries the user's answer to this run's own question continues the SAME agent
	// conversation with that answer as its next turn — not the whole prompt again — so the run picks
	// up exactly where it stopped. The question is marked consumed once the agent has taken it.
	if resumingImplement {
		if q, aerr := deps.Questions().AnsweredForRun(ctx, rc.Run.ID, rc.Repo); aerr == nil && q != nil && q.QKind == runs.QuestionDecision {
			prompt = answerContinuation(*q)
			rc.answeredQuestionID = q.ID
			rc.Sink.Transcript(rc.Repo, transcriptLine("continuing with the user's answer to the open question"))
		}
	}
	// THE AGENT MUST KNOW it stands on a moved base: when the branch was just caught up onto the tip,
	// the prompt names from where to where and which deliveries landed in between, with the express
	// task of weighing its own work against them (REQ point 3). Appended to whichever prompt is used,
	// so a resume with an answered question carries it too.
	if u := rc.Finding.Understack; u != nil && u.CaughtUp {
		prompt += "\n" + catchUpSection(u)
	}
	stream, err := deps.Agent(ctx, rc.Repo, prompt, rc.Tuning, rc.session)
	if err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	out, _ := compactStreamFull(stream.Output(), rc.transcriptEmitter())
	werr := stream.Wait()

	// A resume whose conversation is GONE (the workspace was rebuilt, the agent's own store was
	// cleared) must not end the task. The committed work is on the workbench either way, so the
	// stage opens a FRESH conversation once and says so in the transcript — it never presents the
	// missing conversation as failed work.
	if rc.session.Resume && lostConversation(werr, out.RawFinal) {
		rc.settleUsage(out) // the failed attempt still consumed what it consumed
		rc.Sink.Transcript(rc.Repo, transcriptLine("the earlier conversation is gone — continuing in a new one on the same workbench"))
		rc.session.Resume = false
		stream, err = deps.Agent(ctx, rc.Repo, prompt, rc.Tuning, rc.session)
		if err != nil {
			return fmt.Errorf("start agent: %w", err)
		}
		out, _ = compactStreamFull(stream.Output(), rc.transcriptEmitter())
		werr = stream.Wait()
	}

	rc.settleUsage(out)
	deps.RecordAiUsage(telemetry.UsageSample{
		Source: "run", Repo: rc.Repo, Model: rc.Tuning.Model,
		In: out.Usage.InputTokens, Out: out.Usage.OutputTokens, At: deps.Now().UTC(),
	})

	// Usage-limit detection (REQ-016): the collective pause is triggered by the motor through
	// the injected hook; committed work is published first so nothing is lost (K-1).
	if limited, resetAt, hasReset := mercury.DetectUsageLimit(out.RawFinal, werr); limited {
		_ = cleanup()
		_ = wb.Publish(rc.parent)
		return &usageLimitError{
			msg:      "usage limit reached at " + rc.Repo + ": " + firstLine(limitMessage(out, werr)),
			resetAt:  resetAt,
			hasReset: hasReset,
		}
	}

	// The attachments must be gone BEFORE anything is committed (context only, B-6).
	if err := cleanup(); err != nil {
		return fmt.Errorf("attachment cleanup before commit: %w", err)
	}

	if werr != nil {
		// Keep what exists: agent commits are published (K-1), the transcript is the result
		// (F11). A budget overrun is named with value + achieved (REQ-010.4) by the motor.
		_ = wb.Publish(rc.parent)
		rc.failLog = out.TranscriptTail
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("agent failed: %s", firstLine(werr.Error()))
	}
	if out.IsError {
		_ = wb.Publish(rc.parent)
		rc.failLog = out.TranscriptTail
		return fmt.Errorf("agent reported an error: %s", firstLine(out.ResultText))
	}

	// The answer this resume carried has now been taken by the agent — mark it consumed so a later
	// resume does not feed it a second time.
	if rc.answeredQuestionID != "" {
		if err := deps.Questions().Resolve(ctx, rc.answeredQuestionID); err != nil {
			rc.logf("could not mark the answered question consumed (a later resume may re-ask): %s", firstLine(err.Error()))
		}
		rc.answeredQuestionID = ""
	}

	// A RÜCKFRAGE IS A BLOCKER. When the run — at a level that may ask — ends its message with the
	// question block, it stops here: the work it already finished is committed and published (nothing
	// is lost, K-1), the question is recorded on the Blocked surface and delivered to the user, and
	// the repository blocks on an open question until the answer resumes THIS run. It does NOT guess
	// and it does NOT shrink the task. An autonomous run may not ask, so its block (were it to write
	// one anyway) is ignored and the run proceeds.
	if q, rec, asks := parseAgentQuestion(out.ResultText); asks && rc.Run.Autonomy.MayAsk() {
		return rc.raiseQuestion(ctx, wb, q, rec, out.ResultText)
	}

	// The agent usually commits its own work; whatever is loose is committed now — judging by
	// the working tree alone would silently discard finished work (ported lesson).
	if dirty, derr := wb.HasUncommitted(ctx); derr != nil {
		return fmt.Errorf("inspect working tree: %w", derr)
	} else if dirty {
		if _, cerr := wb.CommitAll(ctx, "mercury-run: "+rc.Run.Title); cerr != nil {
			return fmt.Errorf("commit agent work: %w", cerr)
		}
	}

	// Constitution reference (REQ-002.2/5): the CLAUDE.md block rides along in this regular
	// delivery; when a run finds nothing else, the refresh alone is a full delivery.
	if err := refreshConstitution(ctx, rc, wb); err != nil {
		rc.logf("CLAUDE.md constitution refresh failed (delivery proceeds): %s", firstLine(err.Error()))
	}

	head, err = wb.Head(ctx)
	if err != nil {
		return fmt.Errorf("read workbench head: %w", err)
	}
	rc.head = head
	n, err := wb.CommitsAhead(ctx, rc.deliveryBase)
	if err != nil {
		return fmt.Errorf("count delivery commits: %w", err)
	}
	rc.deliveryCommits = n
	if n > 0 {
		rc.deliveryID = runs.NewDeliveryID()
	}

	// Publish after commit (K-1): the workbench is pushed the moment commits exist — an abort
	// after this line has already secured the work.
	if err := wb.Publish(ctx); err != nil {
		return fmt.Errorf("publish workbench: %w", err)
	}

	// The agent worked this repository through against the run's axioms and left rc.head behind, so
	// THAT is the examined stand the next run measures from. Recorded only here, on the path where
	// an examination actually happened: the rest path creates nothing and examines nothing, and a
	// failed agent examined nothing it could vouch for.
	if err := deps.RecordAxiomScope(rc.Repo, rc.Run, rc.head, deps.Now()); err != nil {
		rc.logf("examined stand not recorded (the next run examines this repository in full): %s", firstLine(err.Error()))
	}

	rc.report = out.ResultText
	switch {
	case n > 0:
		rc.logf("%d commit(s) on %s@%s — published", n, rc.branchName(), short(rc.head))
	case rc.devAdvanced():
		rc.logf("no own contribution — %s advanced only by folding the default branch (%s@%s)",
			rc.branchName(), rc.branchName(), short(rc.head))
	default:
		rc.logf("no new changes — %s@%s unchanged", rc.branchName(), short(rc.head))
	}
	if out.ResultText != "" {
		rc.logf("%s", clip(out.ResultText))
	}
	return nil
}

// implementRestPath establishes the span of already-implemented, undelivered work without
// creating anything new (REQ-020.3).
func implementRestPath(ctx context.Context, rc *RepoCtx, wb WorkbenchOps) error {
	if d := rc.Finding.OpenDelivery; d != nil {
		rc.deliveryID, rc.deliveryBranch = d.ID, d.Branch
		rc.deliveryBase, rc.head = d.FromCommit, d.ToCommit
		rc.deliveryCommits = 1
		rc.logf("already implemented — nothing new created; adopting recorded delivery %s (branch %s)", d.ID, d.Branch)
	} else {
		base, err := wb.MergeBaseDefault(ctx)
		if err != nil {
			return fmt.Errorf("establish undelivered span: %w", err)
		}
		n, err := wb.CommitsAhead(ctx, base)
		if err != nil {
			return fmt.Errorf("count undelivered commits: %w", err)
		}
		rc.deliveryBase, rc.deliveryCommits = base, n
		rc.logf("already implemented — nothing new created; %d undelivered commit(s) on %s@%s", n, rc.branchName(), short(rc.head))
	}
	for _, ev := range rc.Finding.Evidence {
		rc.logf("evidence: %s", ev)
	}
	return nil
}

// ensureRepoCreated creates a missing target repository WITH its protection in the same pass
// (REQ-006.2/033.6). An existing repo satisfies the creation; the protection is ensured either
// way, and a protection failure fails the creation.
func ensureRepoCreated(ctx context.Context, rc *RepoCtx) error {
	gh := rc.Deps.GitHub()
	_, probeErr := gh.DefaultBranch(ctx, rc.Repo)
	if probeErr != nil {
		if faultclass.Classify(probeErr) == faultclass.Transient {
			return fmt.Errorf("probe repository %s: %w", rc.Repo, probeErr)
		}
		err := faultclass.Retry(ctx, &model.Backoff{}, transientMaxAttempts, func() error {
			return gh.CreateRepo(ctx, rc.Repo, true)
		})
		if err != nil && !isSatisfied(err) {
			return fmt.Errorf("create repository %s: %w", rc.Repo, err)
		}
		rc.logf("repository %s created", rc.Repo)
	}
	if err := rc.Deps.Deliver().EnsureProtection(ctx, rc.Repo); err != nil {
		return fmt.Errorf("repository creation failed: protection could not be set: %w", err)
	}
	rc.logf("branch protection ensured from the first moment (PR-only, no history rewrite, merge method \"merge\")")
	return nil
}

// refreshConstitution replaces the CLAUDE.md constitution block with the reference text — the
// ONE renderer is mercury.ReplaceConstitutionBlock (B6 renders, B3 calls). A repo without a
// CLAUDE.md gets one (the shared constitution belongs into every repo).
func refreshConstitution(ctx context.Context, rc *RepoCtx, wb WorkbenchOps) error {
	cur, _, err := wb.ReadFile(ctx, "CLAUDE.md")
	if err != nil {
		return fmt.Errorf("read CLAUDE.md: %w", err)
	}
	next, changed := replaceConstitution(cur)
	if !changed {
		return nil
	}
	if err := wb.WriteFile(ctx, "CLAUDE.md", []byte(next)); err != nil {
		return fmt.Errorf("write CLAUDE.md: %w", err)
	}
	if _, err := wb.CommitAll(ctx, "devlab: refresh the CLAUDE.md constitution reference"); err != nil {
		return fmt.Errorf("commit CLAUDE.md refresh: %w", err)
	}
	rc.logf("CLAUDE.md constitution reference refreshed (rides along in this delivery)")
	return nil
}

// ── deliver-dev ──────────────────────────────────────────────────────────────────────────
//
// Effect: artifact built as user, install-only as root, honest running gate (F10) — all
// inside the deploy package. A missing delivery path is a NAMED failure ("delivery not yet
// set up"), never "no service" and never green (K-4). A deploy error gets exactly ONE honest
// attempt (REQ-032.1) — repetition cannot fix a broken build or a missing setup.

func deliverDevApplies(ctx context.Context, rc *RepoCtx) (bool, string) {
	if rc.Finding.State == model.TaskDelivered {
		return false, deliveredEvidence(rc)
	}
	det, err := rc.detect(ctx)
	if err == nil {
		// A skip must rest on ATTESTED repo evidence (REQ-031.3): a detection that carries
		// none returns "" here — and the motor REFUSES the skip (recorded as failed).
		switch det.Kind {
		case "library":
			return false, attested("library repository (no service to run)", det.Evidence)
		case "excluded":
			return false, attested("excluded from delivery by declaration", det.Evidence)
		case "template":
			return false, attested("service template (never delivered itself)", det.Evidence)
		}
	}
	if rc.prepared && rc.deliveryCommits == 0 && !rc.devAdvanced() {
		return false, fmt.Sprintf("workbench unchanged (%s@%s already delivered previously)", rc.branchName(), short(rc.head))
	}
	return true, ""
}

func deliverDevRun(ctx context.Context, rc *RepoCtx) error {
	det, err := rc.detect(ctx)
	if err != nil {
		return fmt.Errorf("detect repository kind: %w", err)
	}
	if det.Kind == "nonconforming" {
		// REQ-028.4: a service that cannot be led onto the generic path is a violation of the
		// "code structure" axiom — reported, never given a special script.
		rc.Sink.Notice(NoticeEvent{
			Kind:     structureViolationNotice,
			Repo:     rc.Repo,
			Text:     "code-structure violation: the service does not fit the generic delivery path (" + det.Evidence + ")",
			NextStep: "bring the repository onto the holistic service template",
		})
		return fmt.Errorf("code-structure violation: %s", det.Evidence)
	}

	// THE WRITE HALF: if the user approved renewing the root wrapper scripts, install the approved
	// standard-branch versions through the root tool BEFORE the delivery re-checks its guard. Once the
	// installed wrappers match the merged code again, the delivery below proceeds on the same attempt.
	rc.applyApprovedWrapperRenewal(ctx)

	out, err := rc.Deps.Deploy().DeliverDev(ctx, rc.Repo)
	if err != nil {
		if isSatisfied(err) {
			rc.logf("already delivered and running: %s", firstLine(err.Error()))
			return nil
		}
		// THE FREED HANDLE: the self delivery refused because the root wrapper scripts drifted. The
		// chain never writes those scripts — they are the runner's own sudo allowlist, and writing them
		// would be a self-escalation. Instead the stage asks the user to renew them (with the exact
		// difference) and blocks until they do; the guard that raised this refusal re-measures the
		// actual scripts on resume, so nothing installs until they genuinely match — the approval alone
		// never writes them and never bypasses the content check.
		if errors.Is(err, ErrWrappersStale) {
			return rc.raiseWrapperQuestion(ctx, out.WrapperDrift)
		}
		// Both halves are named even when the stage fails: a UI half that could not be built in
		// (unconfigured/failed) still leaves the program installed — the stage fails BENANNT, but the
		// program's own result is not swallowed by the interface's failure.
		if out.Installed && (out.UI == "failed" || out.UI == "unconfigured") {
			rc.logf("program: installed on port %d (%s@%s); interface: %s — the stage fails on the interface half",
				out.Port, rc.branchName(), short(rc.head), out.UI)
		}
		return err // named failure, exactly one attempt — e.g. "delivery not yet set up" (K-4)
	}
	// The self delivery succeeded: if a wrapper-renewal question led here, the user renewed the
	// scripts and the guard just confirmed the match — the question has done its job and leaves the
	// Blocked surface.
	rc.consumeWrapperQuestion(ctx)
	rc.deliveredCommit = rc.head
	rc.logf("program: installed and running on port %d — delivered state %s@%s", out.Port, rc.branchName(), short(rc.head))
	if out.Detail != "" {
		rc.logf("%s", clip(out.Detail))
	}
	// The delivery reports BOTH halves a service needs: program (above) and dashboard UI (here). A
	// service without a ui says so ausdrücklich — the half is not silently omitted.
	switch out.UI {
	case "", "none":
		rc.logf("interface: none — this service ships no dashboard face")
	case "built":
		rc.logf("interface: built and delivered to the serve root the browser reads — the new dashboard is what users now fetch")
	case "foreign-blocked":
		rc.logf("interface: verified and wired in, but the shared dashboard build is blocked by another service: %s", clip(out.UIDetail))
	default:
		rc.logf("interface: %s", clip(out.UI))
	}
	if out.Self {
		// B-2/B-3: the self repo's restart is a HANDOVER — the motor only requests it; sched
		// gates new starts and the wrapper-side unit restarts once the slots are free.
		by := model.Actor{Autonomous: true, OnBehalfOf: rc.Doc.Requested.Created.User}
		if err := rc.Deps.RequestRestart(by); err != nil {
			return fmt.Errorf("restart handover request failed: %w", err)
		}
		rc.logf("self repository delivered — handover restart requested (running executions drain first)")
	}
	return nil
}

func (rc *RepoCtx) detect(ctx context.Context) (Detection, error) {
	if rc.detection == nil && rc.detectErr == nil {
		det, err := rc.Deps.Deploy().Detect(ctx, rc.Repo)
		if err != nil {
			rc.detectErr = err
		} else {
			rc.detection = &det
		}
	}
	if rc.detectErr != nil {
		return Detection{}, rc.detectErr
	}
	return *rc.detection, nil
}

// ── publish ──────────────────────────────────────────────────────────────────────────────
//
// Effect: git push of the workbench. Proof: remote ref == local ref. Push is idempotent; a
// rejected push folds in and retries inside the bench (K-1) — a transient network fault backs
// off with growing intervals until honestly blocked.

func publishApplies(ctx context.Context, rc *RepoCtx) (bool, string) {
	if rc.Finding.State == model.TaskDelivered {
		return false, deliveredEvidence(rc)
	}
	return true, ""
}

func publishRun(ctx context.Context, rc *RepoCtx) error {
	wb := rc.Deps.Workbench(rc.Repo)
	err := faultclass.Retry(ctx, &model.Backoff{}, transientMaxAttempts, func() error {
		return wb.Publish(ctx)
	})
	if err != nil {
		return fmt.Errorf("publish workbench: %w", err)
	}
	rc.logf("workbench %s@%s published", rc.branchName(), short(rc.head))
	return nil
}

// ── pull-request ─────────────────────────────────────────────────────────────────────────
//
// Effect: delivery branch (fix/…|feature/… from the run's description, deterministic collision
// suffix), push, ledger intent, PR — through deliver.OpenOrAdoptPR, the ONE PR path (K-6):
// an existing open PR with the same work is adopted, never duplicated (REQ-019.5).

func pullRequestApplies(ctx context.Context, rc *RepoCtx) (bool, string) {
	if rc.Finding.State == model.TaskDelivered {
		return false, deliveredEvidence(rc)
	}
	if rc.deliveryCommits == 0 {
		if rc.devAdvanced() {
			return false, fmt.Sprintf("no own contribution: %s advanced only by folding the default branch (%s@%s)",
				rc.branchName(), rc.branchName(), short(rc.head))
		}
		return false, fmt.Sprintf("no delivery: no commits beyond %s@%s", rc.branchName(), short(rc.head))
	}
	return true, ""
}

func pullRequestRun(ctx context.Context, rc *RepoCtx) error {
	wb := rc.Deps.Workbench(rc.Repo)
	if rc.deliveryID == "" {
		rc.deliveryID = runs.NewDeliveryID()
	}
	if rc.deliveryBranch == "" {
		// The delivery branch IS the task's own branch. There is no snapshot to cut any more:
		// the agent already committed onto exactly this branch, so what is pushed here is the
		// task's work and nothing else. The old snapshot existed only because every task shared
		// one branch, and a copy of it was the only way to isolate a delivery from it.
		rc.deliveryBranch = runs.TaskBranch(rc.Target.Create, rc.Run.Title, rc.Run.ID)
	}
	if rc.head == "" {
		return errors.New("pull-request without an established head")
	}
	if err := faultclass.Retry(ctx, &model.Backoff{}, transientMaxAttempts, func() error {
		return wb.PushBranch(ctx, rc.deliveryBranch)
	}); err != nil {
		return fmt.Errorf("push delivery branch %s: %w", rc.deliveryBranch, err)
	}

	// The base is the top of the stack EXCLUDING this delivery's own branch — the head that is
	// about to be pushed and opened. Without that exclusion an open delivery of this very run
	// (freshly written to the ledger, or left open by an earlier attempt) would be chosen as the
	// base, and GitHub rejects head==base with a 422 "No commits between X and X".
	var base string
	if err := faultclass.Retry(ctx, &model.Backoff{}, transientMaxAttempts, func() error {
		var berr error
		base, berr = rc.Deps.Deliver().NextPRBase(ctx, rc.Repo, rc.deliveryBranch)
		return berr
	}); err != nil {
		return fmt.Errorf("resolve PR base: %w", err)
	}

	in := DeliverPRIn{
		Repo:        rc.Repo,
		Head:        rc.deliveryBranch,
		Base:        base,
		Title:       "Mercury run: " + rc.Run.Title,
		Body:        prBody(rc),
		DeliveryID:  rc.deliveryID,
		ExecutionID: rc.Doc.ID,
		FromCommit:  rc.deliveryBase,
		ToCommit:    rc.head,
	}
	var pr model.PRRef
	var adopted bool
	if err := faultclass.Retry(ctx, &model.Backoff{}, transientMaxAttempts, func() error {
		var perr error
		pr, adopted, perr = rc.Deps.Deliver().OpenOrAdoptPR(ctx, in)
		return perr
	}); err != nil {
		return fmt.Errorf("open pull request: %w", err)
	}
	rc.link = pr.URL
	if adopted {
		rc.logf("adopted the existing open PR #%d (%s) — no duplicate", pr.Number, pr.URL)
	} else {
		rc.logf("PR #%d (%s), base %s, span %s..%s", pr.Number, pr.URL, base, short(rc.deliveryBase), short(rc.head))
	}
	return nil
}

func prBody(rc *RepoCtx) string {
	var b strings.Builder
	b.WriteString("Delivery of the Mercury execution ")
	b.WriteString(rc.Doc.ID)
	b.WriteString(" (" + string(rc.Run.Kind) + " \"" + rc.Run.Title + "\").\n\n")
	b.WriteString("Span " + short(rc.deliveryBase) + ".." + short(rc.head) + " on " + rc.branchName() + ".\n")

	// What the CHAIN itself did, stated by the chain. Without this line the reader is left with
	// the agent's account alone — and the agent writes it at the END OF IMPLEMENT, from a sandbox
	// that has neither sudo nor push rights. Its honest "built, but not live — I may not deploy"
	// then stands unqualified next to a deployment that DID happen two stages later, and the pull
	// request reads as a failure that never occurred. Measured 2026-07-31 on presentr #7: the
	// service was installed and running at 18:59 while its own pull request said "NOT live".
	b.WriteString("\n**Dev delivery:** ")
	if rc.deliveredCommit != "" {
		b.WriteString("installed and running at " + short(rc.deliveredCommit) +
			" — performed by this pipeline AFTER the report below was written.\n")
	} else {
		b.WriteString("not performed in this run.\n")
	}

	if rc.report != "" {
		b.WriteString("\n---\n\n**The agent's own report**, written at the end of the implement stage — " +
			"before delivery, deployment and this pull request existed. Where it says the agent could not " +
			"deploy, push or open a pull request, that describes the agent's sandbox, not the outcome: the " +
			"line above states the outcome.\n\n")
		b.WriteString(clip(rc.report) + "\n")
	}
	return b.String()
}

// attested joins a skip label with its attested evidence — and yields "" (which the motor
// REFUSES) when there is no evidence to attest (REQ-031.3).
func attested(label, evidence string) string {
	if strings.TrimSpace(evidence) == "" {
		return ""
	}
	return label + ": " + evidence
}

func deliveredEvidence(rc *RepoCtx) string {
	if len(rc.Finding.Evidence) > 0 {
		return "already delivered: " + rc.Finding.Evidence[0]
	}
	return "already delivered (attested by preflight)"
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	if sha == "" {
		return "?"
	}
	return sha
}

const logClip = 16000

// clip bounds a log/report to a recent window (keep the tail — that is where the action is).
func clip(s string) string {
	r := []rune(s)
	if len(r) > logClip {
		return "…\n" + string(r[len(r)-logClip:])
	}
	return s
}

// limitMessage extracts the most telling text of a usage-limit stop for the pause reason.
func limitMessage(out streamOutcome, werr error) string {
	if werr != nil {
		return werr.Error()
	}
	if out.ResultText != "" {
		return out.ResultText
	}
	return "usage limit reached"
}

// lostConversation says whether the agent refused because the conversation this run wanted to
// continue does not exist (any more). It is deliberately narrow: only the agent's own wording about
// a session/conversation counts, so a genuine implementation failure is never mistaken for it and
// silently retried. Measured against the wording the CLI produced on 2026-07-30:
// "Error: --resume requires a valid session ID or session title when used with --print".
func lostConversation(werr error, rawFinal []byte) bool {
	hay := strings.ToLower(strings.TrimSpace(string(rawFinal)))
	if werr != nil {
		hay += " " + strings.ToLower(werr.Error())
	}
	if !strings.Contains(hay, "session") && !strings.Contains(hay, "conversation") {
		return false
	}
	for _, m := range []string{
		"--resume requires a valid session",
		"no conversation found",
		"no session found",
		"session not found",
		"could not find session",
		"invalid session id",
	} {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}
