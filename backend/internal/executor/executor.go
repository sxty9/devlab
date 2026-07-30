// Package executor is the chain motor (S8): it walks the ONE stage list over each target repo,
// records honest stage states, streams the transcript, counts consumption live, enforces the
// time budget honestly, and honors continuations. ONE Deps seam (B 1.3b) instead of nine
// function fields; sched is never imported — the restart hook and the limit pause arrive as
// injected functions (B-3).
//
// Motor rules (REQ-027/030/031/032, K-3/K-4/K-5):
//   - every stage ENDS in exactly one of the four terminal states; skips are never success;
//   - not-applicable requires attested repo evidence — a skip without evidence is REFUSED and
//     recorded as failed (REQ-031.3);
//   - after a failed stage the following stages are not-executed, with the failed stage named;
//   - a repo blocked by an exhausted transient backoff carries its Backoff (reason, time,
//     attempts) and waits for explicit resumption — it never holds the other repos up;
//   - "implemented without deliver-dev" is a failed repo AND raises a notice (K-4/REQ-031);
//   - the time budget ends as a NAMED overrun (value + achieved), never a technical error and
//     never a silent abort (REQ-010.4, F6); the transcript survives (F11);
//   - a detected usage limit pauses ALL executions through the injected hook (REQ-016);
//   - there is no operating mode and no cost cap (REQ-027, REQ-017/W1).
package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/telemetry"
)

// ── The ONE test seam (B 1.3b) ───────────────────────────────────────────────────────────

// PrepareInfo mirrors the workbench's preparation report across the seam.
type PrepareInfo struct {
	Created       bool
	FoldedRemote  bool
	FoldedDefault bool
	Conflicted    bool
	ConflictFiles []string
	Head          string
}

// WorkbenchOps is the slice of the workbench (S9) plus the neutral git primitives the motor
// composes. The adapter in cmd/devlabd implements it over workbench.Bench + workspace.Executor;
// fixtures substitute the whole interface. No operation here ever resets committed work (K-1).
type WorkbenchOps interface {
	Prepare(ctx context.Context) (PrepareInfo, error)
	CleanUntracked(ctx context.Context) error
	Head(ctx context.Context) (string, error)
	// CommitsAhead counts commits on the workbench since the given commit.
	CommitsAhead(ctx context.Context, since string) (int, error)
	HasUncommitted(ctx context.Context) (bool, error)
	// CommitAll stages everything loose and commits it, returning the new head.
	CommitAll(ctx context.Context, message string) (string, error)
	// Publish pushes the workbench (rejected ⇒ fold-in + retry inside, REQ-023.3).
	Publish(ctx context.Context) error
	// ReadFile reads a repo-relative file; exists=false when it is absent.
	ReadFile(ctx context.Context, rel string) (content string, exists bool, err error)
	WriteFile(ctx context.Context, rel string, data []byte) error
	// BranchAt snapshots a branch at a commit WITHOUT checking it out.
	BranchAt(ctx context.Context, name, at string) error
	// PushBranch pushes one named branch to origin.
	PushBranch(ctx context.Context, name string) error
	// MergeBaseDefault returns the merge base of the workbench and the default branch — the
	// span start of undelivered work on the rest path.
	MergeBaseDefault(ctx context.Context) (string, error)
}

// AgentStream is a running agent invocation: its stream-json stdout plus its completion.
type AgentStream interface {
	Output() io.Reader
	Wait() error
	Kill() error
}

// GitHubOps is the typed-error GitHub slice the motor needs (classification input for
// faultclass). The runner token lives behind the adapter — never in the motor.
type GitHubOps interface {
	DefaultBranch(ctx context.Context, repo string) (string, error)
	CreateRepo(ctx context.Context, repo string, private bool) error
}

// DeliverOps is the delivery slice (the ONE PR path + protection on repo creation).
type DeliverOps interface {
	// NextPRBase returns the stacked base of the next PR: the last open delivery's branch,
	// else the default branch (REQ-024).
	NextPRBase(ctx context.Context, repo string) (string, error)
	OpenOrAdoptPR(ctx context.Context, in DeliverPRIn) (model.PRRef, bool, error)
	// EnsureProtection sets the full branch protection; failing to set it fails the repo
	// creation (REQ-033.6).
	EnsureProtection(ctx context.Context, repo string) error
}

// DeliverPRIn mirrors deliver's PR input across the seam (kept local so the seam stays
// mockable without importing deliver in tests). FromCommit..ToCommit is the delivery span the
// ledger intent records.
type DeliverPRIn struct {
	Repo, Head, Base, Title, Body string
	DeliveryID                    string
	ExecutionID                   string
	FromCommit, ToCommit          string
}

// Detection is the deploy detection across the seam (kinds: "service" | "library" |
// "excluded" | "nonconforming" | "template"), always with its evidence.
type Detection struct {
	Kind     string
	Evidence string
}

// DeployOutcome reports one dev delivery honestly (F10): installed AND running, the held
// port, and whether this was the SELF repo (the motor then requests the handover restart,
// B-2/B-3).
type DeployOutcome struct {
	Installed bool
	Running   bool
	Self      bool
	Port      int
	Detail    string
}

// DeployOps is the deploy slice (S11): detection and the one dev delivery (build as user,
// install-only as root, honest running gate — all inside the deploy package).
type DeployOps interface {
	Detect(ctx context.Context, repo string) (Detection, error)
	DeliverDev(ctx context.Context, repo string) (DeployOutcome, error)
}

// Deps is the ONE test seam — implemented by the domain packages, injected in cmd/devlabd.
type Deps interface {
	Workbench(repo string) WorkbenchOps
	Agent(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, resumeID string) (AgentStream, error)
	// StageAttachments materializes a run's media into the runner workspace (B-6) and returns
	// the prompt manifest plus the mandatory cleanup (idempotent; runs before any commit).
	StageAttachments(ctx context.Context, repo string, atts []runs.AttachmentRef) (manifest string, cleanup func() error, err error)
	GitHub() GitHubOps
	Deliver() DeliverOps
	Deploy() DeployOps
	Preflight(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error)
	// AxiomScope names the stand THIS repository was last examined against, per axiom of the run —
	// the section that makes an execution incremental instead of re-reading whole repositories
	// every night. "" when there is nothing to say (a ToDo carries no axioms). It is a per-REPO
	// fact, so it can never live in the run's shared prompt snapshot; the wording is composed by
	// the ONE renderer outside the motor, and an unreadable record is NAMED there, never presented
	// as "never examined".
	AxiomScope(ctx context.Context, repo string, run runs.Run) string
	// RecordAxiomScope notes the stand this repository was examined against, for every axiom of the
	// run. A failure costs the NEXT run its incrementality, never its correctness (it re-examines in
	// full), so the motor reports it and carries on.
	RecordAxiomScope(repo string, run runs.Run, commit string, at time.Time) error
	// RequestRestart is the B-3 seam — implemented by sched, injected; called by the SELF
	// repo's deliver-dev stage after a successful install.
	RequestRestart(by model.Actor) error
	// PauseAllUsageLimit is the collective usage-limit pause hook (REQ-016) — implemented by
	// sched, injected. A zero notBefore means the reset instant is unknown (sched applies its
	// configured backoff).
	PauseAllUsageLimit(msg string, notBefore time.Time) error
	RecordAiUsage(u telemetry.UsageSample)
	Publish(t live.Topic)
	Now() time.Time
}

// NoticeEvent is one notice the motor raises (K-4 delivery alarm, code-structure violation).
// The composed Sink maps it onto the notice pool; the motor never touches a store.
type NoticeEvent struct {
	Kind     string
	Repo     string
	Text     string
	NextStep string
}

// Sink receives the motor's progress — implemented by sched (document progress) and by the
// result recorder (live result); composed in cmd/devlabd. Transcript lines arrive as
// self-contained JSON objects ({"at":…,"text":…}) ready for the jsonl journal.
type Sink interface {
	StageUpdate(repo string, sv model.StageView)
	Transcript(repo string, line []byte)
	// Usage carries the EXECUTION-cumulative consumption — climbing live while the agent
	// works (F7), settled to the authoritative final figures per invocation.
	Usage(u model.UsageView)
	RepoDone(rp model.RepoPipeline)
	Continuation(c model.ContinuationView)
	Notice(n NoticeEvent)
}

// Request is one execution request: the run definition, its persisted state document and the
// resolved tuning (budget included).
type Request struct {
	Run    runs.Run
	Doc    execstate.Doc
	Budget runs.ResolvedTuning
}

// ── Named outcomes ───────────────────────────────────────────────────────────────────────

// ErrPausedUsageLimit reports that the execution paused on the subscription's usage limit:
// the collective pause hook has been invoked and the continuation persisted. Not a failure.
var ErrPausedUsageLimit = errors.New("paused: usage limit reached")

// BudgetExceededError is the honest time-budget overrun (REQ-010.4): the budget VALUE and
// what was achieved, never a bare technical error.
type BudgetExceededError struct {
	Budget   time.Duration
	Achieved string
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("time budget exceeded (%s): %s", e.Budget, e.Achieved)
}

// ReposFailedError summarizes an execution whose repos did not all succeed — the named,
// non-technical execution outcome.
type ReposFailedError struct {
	Failed, Blocked, Succeeded, Total int
}

func (e *ReposFailedError) Error() string {
	msg := fmt.Sprintf("%d of %d repositories failed", e.Failed, e.Total)
	if e.Blocked > 0 {
		msg += fmt.Sprintf(" (%d blocked, awaiting explicit resumption)", e.Blocked)
	}
	return msg
}

// usageLimitError is the internal signal an agent invocation hit the subscription window.
type usageLimitError struct {
	msg      string
	resetAt  time.Time
	hasReset bool
}

func (e *usageLimitError) Error() string { return e.msg }

// ── The per-repo context ─────────────────────────────────────────────────────────────────

// RepoCtx is the motor's per-repo context handed to the stage functions.
type RepoCtx struct {
	Repo    string
	Run     runs.Run
	Doc     execstate.Doc
	Tuning  runs.ResolvedTuning
	Finding preflight.Finding
	Target  runs.Target
	Deps    Deps
	Sink    Sink

	// parent is the un-budgeted context — bookkeeping after a budget kill (publish, counting
	// what was achieved) still needs a live context.
	parent context.Context
	// resumeID is the opaque continuation token handed to the agent on resume ("" fresh).
	resumeID string

	prepared        bool
	prep            PrepareInfo
	head            string // current workbench head
	deliveryBase    string // head AFTER the fold — the delivery span start
	deliveryCommits int
	deliveryID      string
	deliveryBranch  string
	deliveredCommit string // named delivered state (REQ-022.3)
	detection       *Detection
	detectErr       error
	report          string
	usage           *usageTotal

	logBuf  []string
	failLog string // F11: transcript tail shown as the failed stage's log
	link    string
}

func (rc *RepoCtx) logf(format string, args ...any) {
	rc.logBuf = append(rc.logBuf, fmt.Sprintf(format, args...))
}

func (rc *RepoCtx) takeLog() string {
	s := strings.Join(rc.logBuf, "\n")
	rc.logBuf = nil
	return s
}

// workAdvanced reports whether this run left work at the repo: agent commits (a delivery) or
// a moved workbench (created / default folded in).
func (rc *RepoCtx) workAdvanced() bool {
	return rc.deliveryCommits > 0 || rc.devAdvanced()
}

// devAdvanced reports whether the workbench itself moved to a new state to serve.
func (rc *RepoCtx) devAdvanced() bool {
	return rc.prepared && (rc.prep.Created || rc.prep.FoldedDefault)
}

// usageTotal accumulates the execution-cumulative consumption (REQ-017 — informative only,
// there is NO cap).
type usageTotal struct {
	in, out int64
	cost    float64
}

func (u *usageTotal) view() model.UsageView {
	return model.UsageView{InputTokens: u.in, OutputTokens: u.out, CostUSD: u.cost}
}

// ── The motor ────────────────────────────────────────────────────────────────────────────

// deliveryAlarmNotice labels the K-4 alarm notices ("implemented without dev delivery").
const deliveryAlarmNotice = "delivery-alarm"

// structureViolationNotice labels the REQ-028.4 code-structure violation notices.
const structureViolationNotice = "code-structure-violation"

// Execute walks the chain over every target repo of the execution. Domain outcomes are fully
// visible through the Sink; the returned error is the NAMED execution-level outcome:
//   - nil — every repo succeeded (or was honestly not-applicable throughout);
//   - ErrPausedUsageLimit — paused collectively on the subscription window (hook invoked,
//     continuation persisted); resume re-enters at the same spot;
//   - ctx.Err() — the execution was canceled/interrupted; the continuation is persisted and
//     the current stage recorded honestly;
//   - *BudgetExceededError — the time budget ended the execution, named with value+achieved;
//   - *ReposFailedError — completed, but repos failed and/or are blocked.
func Execute(ctx context.Context, deps Deps, req Request, sink Sink) error {
	if deps == nil || sink == nil {
		return errors.New("executor: nil deps or sink")
	}
	bctx := ctx
	if req.Budget.Budget > 0 {
		var cancel context.CancelFunc
		bctx, cancel = context.WithTimeout(ctx, req.Budget.Budget)
		defer cancel()
	}

	total := &usageTotal{}
	var failed, blocked, succeeded, run int
	budgetSpent := false

	for _, row := range req.Doc.Repos {
		if row.State == execstate.RepoDone {
			continue // completed repositories stay completed (REQ-015.2/019.2)
		}
		run++
		if budgetSpent || bctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			budgetSpent = true
			markBudgetSkipped(sink, row.Repo, req.Budget.Budget)
			failed++
			continue
		}

		rc := &RepoCtx{
			Repo: row.Repo, Run: req.Run, Doc: req.Doc, Tuning: req.Budget,
			Deps: deps, Sink: sink, parent: ctx, usage: total,
			Target: targetFor(req.Run, row.Repo),
		}
		if c := req.Doc.Continuation; c != nil && c.Repo == row.Repo {
			rc.resumeID = req.Doc.ID
		}

		rp, err := runRepo(bctx, rc)
		if err != nil {
			var ul *usageLimitError
			if errors.As(err, &ul) {
				// Usage-limit pause (REQ-016): ALL executions pause together through the
				// injected hook; this one records its continuation and waits.
				notBefore := time.Time{}
				if ul.hasReset {
					notBefore = ul.resetAt
				}
				_ = deps.PauseAllUsageLimit(ul.msg, notBefore)
				return ErrPausedUsageLimit
			}
			if ctx.Err() != nil {
				return ctx.Err() // interrupted/canceled — continuation already emitted
			}
			// Budget overrun: the current repo is already honestly recorded; remaining repos
			// are marked below on the next loop turns.
			var be *BudgetExceededError
			if errors.As(err, &be) {
				budgetSpent = true
				failed++
				continue
			}
			return err
		}
		switch {
		case rp.Block != nil:
			blocked++
		case rp.Succeeded:
			succeeded++
		default:
			failed++
		}
	}

	sink.Usage(total.view())
	if budgetSpent {
		return &BudgetExceededError{
			Budget:   req.Budget.Budget,
			Achieved: fmt.Sprintf("%d of %d repositories completed; the transcript and all committed work are preserved", succeeded, run),
		}
	}
	if failed > 0 || blocked > 0 {
		return &ReposFailedError{Failed: failed, Blocked: blocked, Succeeded: succeeded, Total: run}
	}
	return nil
}

// targetFor finds the run's target entry for repo (zero value when untracked — e.g. an auto
// run whose targets are discovered, never created).
func targetFor(r runs.Run, repo string) runs.Target {
	for _, t := range r.Targets {
		if t.Repo == repo {
			return t
		}
	}
	return runs.Target{Repo: repo}
}

// markBudgetSkipped records an untouched repo honestly once the budget is spent: every stage
// not-executed with the named overrun — never silently missing, never green (K-4).
func markBudgetSkipped(sink Sink, repo string, budget time.Duration) {
	reason := fmt.Sprintf("not executed: time budget exceeded (%s) before this repository was reached", budget)
	views := make([]model.StageView, 0, len(model.ChainStages()))
	now := time.Now().UTC()
	for _, st := range model.ChainStages() {
		sv := model.StageView{Stage: st, State: model.StepNotExecuted, Reason: reason, EndedAt: &now}
		sink.StageUpdate(repo, sv)
		views = append(views, sv)
	}
	rp := model.RepoPipeline{Repo: repo, Stages: views}
	rp.Done, rp.Succeeded = model.PipelineSucceeded(views)
	sink.RepoDone(rp)
}

// runRepo walks the chain over one repo. The returned error is only ever a MOTOR-level signal
// (usage limit, interrupt, budget); every stage outcome is recorded in the pipeline.
//
// Resume ("continue at the same spot", REQ-018.6/019.2) walks the WHOLE chain again — every
// stage is idempotent (ARCHITEKTUR §6.3), so nothing is duplicated: preflight re-derives the
// truth, implement either resumes the agent session (continuation at implement — the resumeID
// is passed through) or takes the rest path over already-committed work, deliver/publish/PR
// re-probe before re-effecting (adoption instead of duplication, K-6). Completed repositories
// never re-enter (their document row is done).
func runRepo(ctx context.Context, rc *RepoCtx) (model.RepoPipeline, error) {
	stages := Chain()

	views := make([]model.StageView, 0, len(stages))
	var block *model.Backoff
	var budgetErr *BudgetExceededError
	failedStage := model.Stage("")

	for _, spec := range stages {
		if failedStage != "" {
			now := rc.Deps.Now().UTC()
			sv := model.StageView{
				Stage:   spec.Name,
				State:   model.StepNotExecuted,
				Reason:  fmt.Sprintf("not executed: stage %s failed", failedStage),
				EndedAt: &now,
			}
			rc.Sink.StageUpdate(rc.Repo, sv)
			views = append(views, sv)
			continue
		}

		started := rc.Deps.Now().UTC()
		rc.Sink.StageUpdate(rc.Repo, model.StageView{Stage: spec.Name, State: model.StepRunning, StartedAt: &started})

		sv := model.StageView{Stage: spec.Name, StartedAt: &started}
		ok, evidence := spec.Applies(ctx, rc)
		if !ok {
			if strings.TrimSpace(evidence) == "" {
				// REQ-031.3: a skip WITHOUT attested repo evidence is refused — recorded as a
				// failure, never as silent not-applicable and never as success.
				sv.State = model.StepFailed
				sv.Reason = "refused: stage skip without attested repo evidence"
				failedStage = spec.Name
			} else {
				sv.State = model.StepNotApplicable
				sv.Reason = evidence
				sv.Evidence = evidence
			}
		} else {
			err := spec.Run(ctx, rc)
			switch {
			case err == nil:
				sv.State = model.StepExecuted
				sv.Log = rc.takeLog()
			case isUsageLimit(err):
				// The stage stays transient (running) — the execution pauses and resumes here.
				rc.Sink.Continuation(model.ContinuationView{Repo: rc.Repo, Stage: spec.Name})
				return model.RepoPipeline{}, err
			case rc.parent.Err() != nil:
				// Interrupted/canceled from outside: record honestly, persist the spot.
				now := rc.Deps.Now().UTC()
				sv.State = model.StepFailed
				sv.Reason = "interrupted: " + firstLine(rc.parent.Err().Error())
				sv.Log = rc.takeFailLogOr()
				sv.EndedAt = &now
				rc.Sink.StageUpdate(rc.Repo, sv)
				rc.Sink.Continuation(model.ContinuationView{Repo: rc.Repo, Stage: spec.Name})
				return model.RepoPipeline{}, rc.parent.Err()
			case ctx.Err() != nil && rc.parent.Err() == nil:
				// The time budget expired mid-stage: a NAMED overrun with value + achieved
				// (REQ-010.4); transcript and committed work are preserved (F11/K-1). The
				// remaining stages are marked not-executed by the loop.
				sv.State = model.StepFailed
				sv.Reason = rc.budgetOverrunReason()
				sv.Log = rc.takeFailLogOr()
				failedStage = spec.Name
				budgetErr = &BudgetExceededError{Budget: rc.Tuning.Budget, Achieved: rc.budgetAchieved()}
			default:
				if bl := asBlocked(err); bl != nil {
					// Transient attempts exhausted ⇒ honestly blocked (K-5): reason, time,
					// attempts — awaiting explicit resumption; other repos continue.
					sv.State = model.StepFailed
					sv.Reason = bl.Error()
					sv.Log = rc.takeFailLogOr()
					block = &bl.Backoff
					failedStage = spec.Name
				} else if isSatisfied(err) {
					// A goal already reached counts as success (REQ-032.2).
					sv.State = model.StepExecuted
					sv.Log = joinNonEmpty(rc.takeLog(), "already in the desired state: "+firstLine(err.Error()))
				} else {
					sv.State = model.StepFailed
					sv.Reason = firstLine(err.Error())
					sv.Log = rc.takeFailLogOr()
					failedStage = spec.Name
				}
			}
		}
		finishStage(rc, &sv, &views)
	}

	rp := rc.finishRepo(views, block)
	if budgetErr != nil {
		return rp, budgetErr
	}
	return rp, nil
}

func finishStage(rc *RepoCtx, sv *model.StageView, views *[]model.StageView) {
	if sv.EndedAt == nil {
		now := rc.Deps.Now().UTC()
		sv.EndedAt = &now
	}
	if sv.Link == "" {
		sv.Link = rc.link
		rc.link = ""
	}
	rc.Sink.StageUpdate(rc.Repo, *sv)
	*views = append(*views, *sv)
}

// finishRepo assembles the pipeline, raises the K-4 alarm when implemented work was left
// undelivered, and reports the repo done.
//
// stage-vocabulary: `views` are the stages this run just walked, named by the chain itself. The
// executor reads no stored execution document, so an archived one — which carries the retired
// stage names — cannot reach this comparison.
func (rc *RepoCtx) finishRepo(views []model.StageView, block *model.Backoff) model.RepoPipeline {
	rp := model.RepoPipeline{Repo: rc.Repo, Stages: views, TaskState: rc.Finding.State, Block: block}
	rp.Done, rp.Succeeded = model.PipelineSucceeded(views)

	if rc.workAdvanced() {
		for _, sv := range views {
			if sv.Stage != model.StageDeliverDev {
				continue
			}
			if sv.State == model.StepFailed || sv.State == model.StepNotExecuted {
				// K-4/REQ-031: implemented without dev delivery ⇒ the repo already failed;
				// the user is additionally informed of their own accord.
				rc.Sink.Notice(NoticeEvent{
					Kind:     deliveryAlarmNotice,
					Repo:     rc.Repo,
					Text:     fmt.Sprintf("implemented without dev delivery: %s", nonEmpty(sv.Reason, "dev delivery did not run")),
					NextStep: "fix the delivery path and resume the execution",
				})
			}
		}
	}
	rc.Sink.RepoDone(rp)
	return rp
}

func (rc *RepoCtx) budgetOverrunReason() string {
	return (&BudgetExceededError{Budget: rc.Tuning.Budget, Achieved: rc.budgetAchieved()}).Error()
}

// budgetAchieved names what the overrun kept — counted on the live parent context, since the
// budget context is already dead.
func (rc *RepoCtx) budgetAchieved() string {
	if !rc.prepared {
		return "no changes had been made yet"
	}
	n, err := rc.Deps.Workbench(rc.Repo).CommitsAhead(rc.parent, rc.deliveryBase)
	if err != nil || n == 0 {
		return "the transcript is preserved; no commits had been made yet"
	}
	return fmt.Sprintf("the transcript is preserved and %d commit(s) on %s are already published", n, workbenchBranch)
}

// takeFailLogOr returns the transcript tail when one was captured (F11 — a failed/killed agent
// step keeps its transcript as the result), else the accumulated stage log.
func (rc *RepoCtx) takeFailLogOr() string {
	if rc.failLog != "" {
		s := rc.failLog
		rc.failLog = ""
		return s
	}
	return rc.takeLog()
}

// workbenchBranch names the workbench in messages. The constant itself lives in package
// workbench (S9); the literal here stays aligned by the vocabulary test.
const workbenchBranch = "mercury-dev"

func isUsageLimit(err error) bool {
	var ul *usageLimitError
	return errors.As(err, &ul)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	const max = 400
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func joinNonEmpty(parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}
