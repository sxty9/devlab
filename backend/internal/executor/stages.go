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

	prep, err := wb.Prepare(ctx)
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
	head, err := wb.Head(ctx)
	if err != nil {
		return fmt.Errorf("read workbench head: %w", err)
	}
	// The delivery base is the head AFTER the fold, so the span holds exactly the agent's own
	// commits — the range a counter-booking later reverses.
	rc.deliveryBase, rc.head = head, head
	// Measure the branch actually worked on so every log line names the truth, not a constant.
	if b := wb.CurrentBranch(ctx); b != "" {
		rc.branch = b
	}

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
	prompt := AssemblePrompt(rc.Run.PromptSnapshot, rc.Finding, deps.AxiomScope(ctx, rc.Repo, rc.Run), manifest)
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

	out, err := rc.Deps.Deploy().DeliverDev(ctx, rc.Repo)
	if err != nil {
		if isSatisfied(err) {
			rc.logf("already delivered and running: %s", firstLine(err.Error()))
			return nil
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
		rc.logf("interface: built and wired into the shared dashboard")
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
		rc.deliveryBranch = runs.BranchName(runs.BranchKindFor(rc.Target.Create), rc.Run.Title, runs.NewBranchToken())
	}
	if rc.head == "" {
		return errors.New("pull-request without an established head")
	}
	if err := wb.BranchAt(ctx, rc.deliveryBranch, rc.head); err != nil {
		return fmt.Errorf("snapshot delivery branch %s: %w", rc.deliveryBranch, err)
	}
	if err := faultclass.Retry(ctx, &model.Backoff{}, transientMaxAttempts, func() error {
		return wb.PushBranch(ctx, rc.deliveryBranch)
	}); err != nil {
		return fmt.Errorf("push delivery branch %s: %w", rc.deliveryBranch, err)
	}

	var base string
	if err := faultclass.Retry(ctx, &model.Backoff{}, transientMaxAttempts, func() error {
		var berr error
		base, berr = rc.Deps.Deliver().NextPRBase(ctx, rc.Repo)
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
	if rc.report != "" {
		b.WriteString("\n" + clip(rc.report) + "\n")
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
