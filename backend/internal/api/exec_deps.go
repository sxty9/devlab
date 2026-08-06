// The COMPOSITION the daemon needs, not request handling (ARCHITEKTUR §3.8/§3.14): the production
// executor.Deps and preflight.Sources over this server's pools. It sits here because api.New owns
// every passive pool — a second store handle over the same file would be a second mutex over one
// truth, so the composition reaches the pools through their one owner instead of re-opening them.
//
// The whole file is adapter work: it resolves a run target (a repo id) to the runner's working tree
// and GitHub full name, shims the workbench onto the motor's WorkbenchOps, turns the blocking agent
// primitive into a stream, and forwards the two scheduler hooks (B-3). No decision lives here — the
// stages decide, the pools store, the scheduler admits.
package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/atlas"
	"devlab/backend/internal/deliver"
	"devlab/backend/internal/deploy"
	"devlab/backend/internal/discover"
	"devlab/backend/internal/executor"
	"devlab/backend/internal/github"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/telemetry"
	"devlab/backend/internal/workbench"
	"devlab/backend/internal/workspace"
)

// ChainHooks are the two callbacks the motor reaches BACK through (B-3): the handover restart
// request and the collective usage-limit pause. Both are implemented by sched and injected here, so
// the executor never imports the scheduler.
type ChainHooks struct {
	RequestRestart     func(by model.Actor) error
	PauseAllUsageLimit func(msg string, notBefore time.Time) error
}

// SchedulerHooks adapts the scheduler's own signatures onto ChainHooks — sched.RequestRestart
// answers with the restart STATE (the marker it wrote), the motor only needs to know whether the
// request went through.
func SchedulerHooks(sc *sched.Scheduler) ChainHooks {
	return ChainHooks{
		RequestRestart: func(by model.Actor) error {
			_, err := sc.RequestRestart(by)
			return err
		},
		PauseAllUsageLimit: sc.PauseAllUsageLimit,
	}
}

// ChainDeps is the production executor.Deps (and preflight.Sources) for ONE execution: it holds the
// working trees it prepared, so the stages of one repo share one bench and one workspace lock.
// Close releases them; a ChainDeps is never reused across executions.
type ChainDeps struct {
	s     *Server
	hooks ChainHooks
	// run is the execution's own run definition. It is what lets the media pool be addressed
	// (the pool is keyed by run id) without a second lookup path; the observation-only form
	// (the admission gate, the startup reconciliation) leaves it zero.
	run runs.Run

	user  string // the runner's OS account (workspace owner)
	token string // the runner's linked GitHub token
	// initErr names why this composition cannot act at all (no runner account, no token). Every
	// repo operation then fails with that named reason — never silently skips (K-4).
	initErr error

	mu      sync.Mutex
	benches map[string]*repoBench
	full    map[string]string

	// The three seams below default to production and are overridden ONLY by the end-to-end
	// production test, which drives the whole path against a REAL local repository. In production
	// they are zero/false, so the behaviour is exactly the inline code they replaced.
	//
	// execBypass runs the runner's git ops directly (PerUser=false) instead of through the pinned
	// sudo wrapper — a hermetic test has no wrapper, but it does have a real git and a real checkout,
	// so the fetch and the DETACHED checkout of the merged state run for real.
	execBypass bool
	// prodBuild / prodSend are the build-and-ship half of the production step. Everything BEFORE
	// them — resolve the merged default branch, fetch it, check it out detached, detect the service —
	// runs against the real repository. The build (root-only, via the wrapper) and the send to the
	// foreign host are what a test substitutes, because a hermetic test has neither the wrapper nor a
	// remote target. nil means the production function.
	prodBuild func(ctx context.Context, ex workspace.Executor, wt string) (string, error)
	prodSend  func(ctx context.Context, cfg deploy.ProdConfig, repo, artifact string) error
	// prodEmit lays the first-time SETUP product (unit/route/rights) into the artifact before the send,
	// AS THE RUNNER through the pinned wrapper. Like prodBuild, a hermetic test substitutes it (no
	// wrapper, no runner). nil means the production function.
	prodEmit func(ctx context.Context, ex workspace.Executor, wt, repo string, det deploy.Detection) error
}

// runnerExec builds the Executor that acts AS the runner account. It is the ONE place the runner's
// executor is constructed, so the per-user mode — production: run AS the workspace owner through the
// pinned devlab-exec wrapper — lives in a single spot instead of five identical literals. execBypass
// (set only by the end-to-end production test) drops to direct execution.
func (d *ChainDeps) runnerExec() workspace.Executor {
	return workspace.Executor{User: d.user, PerUser: !d.execBypass}
}

// buildProd runs the production artifact build, honouring the test seam.
func (d *ChainDeps) buildProd(ctx context.Context, ex workspace.Executor, wt string) (string, error) {
	if d.prodBuild != nil {
		return d.prodBuild(ctx, ex, wt)
	}
	return deploy.Build(ctx, ex, wt)
}

// sendProd ships the prebuilt artifact to the production host, honouring the test seam.
func (d *ChainDeps) sendProd(ctx context.Context, cfg deploy.ProdConfig, repo, artifact string) error {
	if d.prodSend != nil {
		return d.prodSend(ctx, cfg, repo, artifact)
	}
	return deploy.SendProd(ctx, cfg, repo, artifact)
}

// emitProdSetup lays the first-time SETUP product into the artifact (before the send), honouring the
// test seam. The production path resolves the port the delivered unit will bind (declared, else an
// atlas proposal) and runs the emit AS THE RUNNER through the pinned wrapper.
func (d *ChainDeps) emitProdSetup(ctx context.Context, ex workspace.Executor, wt, repo string, det deploy.Detection) error {
	if d.prodEmit != nil {
		return d.prodEmit(ctx, ex, wt, repo, det)
	}
	port, err := deploy.ProdSetupPort(ctx, det)
	if err != nil {
		return fmt.Errorf("resolve the production port for %s: %w", repo, err)
	}
	if out, err := ex.EmitSetup(ctx, wt, repo, port); err != nil {
		return fmt.Errorf("emit the setup product for %s: %w\n%s", repo, err, out)
	}
	return nil
}

// repoBench is one repo's prepared working tree, held for the execution's lifetime.
type repoBench struct {
	bench  *workbench.Bench
	wt     string
	unlock func()
	err    error
}

// ChainDeps assembles the execution dependencies. It never returns nil: a missing prerequisite
// becomes the named error every stage then reports.
func (s *Server) ChainDeps(hooks ChainHooks) *ChainDeps {
	d := &ChainDeps{s: s, hooks: hooks, benches: map[string]*repoBench{}, full: map[string]string{}}
	d.user = runsUser()
	if d.user == "" {
		d.initErr = errors.New("no runner account configured (DEVLAB_RUNS_USER) — the chain has no workspace to work in")
		return d
	}
	token, err := s.runnerToken()
	if err != nil {
		d.initErr = fmt.Errorf("the chain needs the linked runner account: %w", err)
		return d
	}
	d.token = token
	return d
}

// WithRun binds the execution's run definition, mirroring the workbench's WithToken wiring style.
// Returns the same composition for convenience.
func (d *ChainDeps) WithRun(run runs.Run) *ChainDeps {
	d.run = run
	return d
}

// Close releases every workspace lock this composition took.
func (d *ChainDeps) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, rb := range d.benches {
		if rb.unlock != nil {
			rb.unlock()
			rb.unlock = nil
		}
	}
	d.benches = map[string]*repoBench{}
}

// fullName resolves a run target (a repo id) to its GitHub owner/repo. A repo that does not exist
// yet (a target to be created) resolves to the instance owner's namespace, so the creating stage can
// name it — resolution never invents an owner beyond the configured one.
func (d *ChainDeps) fullName(ctx context.Context, repo string) (string, error) {
	if strings.Contains(repo, "/") {
		return repo, nil
	}
	d.mu.Lock()
	if f, ok := d.full[repo]; ok {
		d.mu.Unlock()
		return f, nil
	}
	d.mu.Unlock()
	if d.initErr != nil {
		return "", d.initErr
	}
	full, ok := d.s.resolveRunnerRepo(ctx, d.token, repo)
	if !ok {
		owner, err := discover.Owner()
		if err != nil {
			// The NAMED error, wrapped: a caller — and a test — tells "no namespace configured"
			// apart from "this repository does not exist" without matching on message text.
			return "", fmt.Errorf("repository %q is unknown and no instance owner is configured: %w", repo, err)
		}
		full = owner + "/" + repo
	}
	d.mu.Lock()
	d.full[repo] = full
	d.mu.Unlock()
	return full, nil
}

// bench prepares (once) and returns the runner's workbench for one repo, holding the workspace lock
// for the rest of the execution. A failure is cached: a repo whose workspace cannot be opened fails
// the same way for every stage instead of retrying the clone per call.
func (d *ChainDeps) bench(ctx context.Context, repo string) (*workbench.Bench, string, error) {
	d.mu.Lock()
	if rb, ok := d.benches[repo]; ok {
		d.mu.Unlock()
		return rb.bench, rb.wt, rb.err
	}
	d.mu.Unlock()

	rb := &repoBench{}
	if d.initErr != nil {
		rb.err = d.initErr
	} else if full, err := d.fullName(ctx, repo); err != nil {
		rb.err = err
	} else {
		b, wt, unlock, berr := d.s.runnerBench(ctx, d.user, d.token, repo, full)
		if berr != nil {
			rb.err = fmt.Errorf("prepare the runner workspace of %s: %w", repo, berr)
		} else {
			rb.bench, rb.wt, rb.unlock = b.WithToken(d.token), wt, unlock
		}
	}

	d.mu.Lock()
	if existing, ok := d.benches[repo]; ok { // a concurrent caller won the race
		d.mu.Unlock()
		if rb.unlock != nil {
			rb.unlock()
		}
		return existing.bench, existing.wt, existing.err
	}
	d.benches[repo] = rb
	d.mu.Unlock()
	return rb.bench, rb.wt, rb.err
}

// ── executor.Deps ────────────────────────────────────────────────────────────────────────

// Workbench hands the motor the working-state operations of one repo. The seam has no error
// channel, so an unopenable workspace answers through every method with its named reason — the
// stage then fails honestly instead of skipping.
func (d *ChainDeps) Workbench(repo string) executor.WorkbenchOps {
	return benchOps{d: d, repo: repo}
}

// Agent starts the agent on the repo's workbench in stream-json form and hands back the live
// stream. --verbose is what makes the CLI emit every event as it happens in -p mode, which is what
// keeps the transcript and the live token counters honest (F7/F11).
func (d *ChainDeps) Agent(ctx context.Context, repo, prompt string, t runs.ResolvedTuning, sess executor.AgentSession) (executor.AgentStream, error) {
	_, wt, err := d.bench(ctx, repo)
	if err != nil {
		return nil, err
	}
	ex := d.runnerExec()
	// Name the branch the working tree is ACTUALLY on — each run works on its own order branch, so a
	// preamble that always said "mercury-dev" told the agent a falsehood about where its commits land.
	// The workbench constant is only the fallback when the measurement yields nothing (bare/detached).
	branch := ex.CurrentBranch(ctx, wt)
	if branch == "" || branch == "HEAD" {
		branch = workbench.LegacyShared
	}
	return startAgentStream(ctx, ex, wt, chainAgentArgs(repo, branch, prompt, t, sess)), nil
}

// chainAgentArgs is the agent invocation of ONE stage, as a value — the seam that lets the
// invocation be verified without a workspace and without root.
func chainAgentArgs(repo, branch, prompt string, t runs.ResolvedTuning, sess executor.AgentSession) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if effort := chainEffort(t.Effort); effort != "" {
		args = append(args, "--effort", effort)
	}
	// The conversation is NAMED when it is opened, not only when it is continued. The CLI takes a
	// UUID under --session-id and answers --resume only for a name it has already seen; handing it
	// the execution id on resume alone made every continuation die with
	// "--resume requires a valid session ID or session title". The name is derived — same execution
	// and same repository always yield the same UUID, so no id has to be stored anywhere.
	if sess.Key != "" {
		if sess.Resume {
			args = append(args, "--resume", sessionUUID(sess.Key, repo))
		} else {
			args = append(args, "--session-id", sessionUUID(sess.Key, repo))
		}
	}
	return append(args, "--append-system-prompt", chainPreamble(repo, branch, t.Effort))
}

// chainEffort maps the run's effort onto the CLI ladder. "ultracode" is DevLab's own maximal tier:
// the CLI's own top level plus the directive in the preamble — one name, no second flag.
func chainEffort(effort string) string {
	if effort == "ultracode" {
		return "max"
	}
	return effort
}

// chainPreamble tells the agent what it is: the autonomous DevLab runner working on the workbench
// of one repository. The constitution itself is NEVER composed here — it rides in the prompt
// snapshot (REQ-002.1).
func chainPreamble(repo, branch, effort string) string {
	if branch == "" {
		branch = workbench.LegacyShared
	}
	s := "You are the autonomous DevLab runner. You work in the checked-out workspace of the " +
		"repository \"" + repo + "\" on its working branch " + branch + ". " +
		"Implement what the task asks for, commit your work with clear messages, and state plainly " +
		"what you did and what you did not do."
	if effort == "ultracode" {
		s += " Take the most thorough path available: verify your change by building and testing it " +
			"before you finish."
	}
	return s
}

// StageAttachments materializes a run's media into the runner workspace (B-6) and hands back the
// prompt manifest plus the mandatory cleanup.
func (d *ChainDeps) StageAttachments(ctx context.Context, repo string, atts []runs.AttachmentRef) (string, func() error, error) {
	noop := func() error { return nil }
	if len(atts) == 0 {
		return "", noop, nil
	}
	_, wt, err := d.bench(ctx, repo)
	if err != nil {
		return "", noop, err
	}
	if d.s.attachments == nil {
		return "", noop, errors.New("the media pool is not available")
	}
	if d.run.ID == "" {
		return "", noop, errors.New("the media pool is keyed by run — this composition carries no run")
	}
	files := make([]workspace.AttachmentFile, 0, len(atts))
	for _, a := range atts {
		data, gerr := d.s.attachments.Get(d.run.ID, a.ID)
		if gerr != nil {
			return "", noop, fmt.Errorf("read attachment %s: %w", a.Filename, gerr)
		}
		files = append(files, workspace.AttachmentFile{Filename: a.Filename, Data: data, Note: a.MIME})
	}
	ex := d.runnerExec()
	manifest, _, cleanup, serr := ex.StageAttachments(ctx, wt, files)
	if serr != nil {
		return "", noop, serr
	}
	return manifest, cleanup, nil
}

// GitHub is the typed-error GitHub slice the motor classifies against.
func (d *ChainDeps) GitHub() executor.GitHubOps { return chainGitHub{d: d} }

// Deliver is the ONE pull-request path, reached through package deliver.
func (d *ChainDeps) Deliver() executor.DeliverOps { return chainDeliver{d: d} }

// deliveringBranchFor names the branch a run delivers for one repo — the run's own task branch, cut
// per (this target's new-service flag, run title, run id). It is the tree the wrapper gate and the
// working-source renewal measure against, read from its committed ref (never the shared working tree,
// which sits on the standard branch on a resume).
func deliveringBranchFor(run runs.Run, repo string) string {
	create := false
	for _, t := range run.Targets {
		if sameRepo(t.Repo, repo) {
			create = t.Create
			break
		}
	}
	return runs.TaskBranch(create, run.Title, run.ID)
}

// deliveringBranch is deliveringBranchFor bound to THIS execution's run.
func (d *ChainDeps) deliveringBranch(repo string) string { return deliveringBranchFor(d.run, repo) }

// Deploy is the delivery-to-host machinery (S11).
func (d *ChainDeps) Deploy() executor.DeployOps { return chainDeploy{d: d} }

// Questions is the blocking-question slice (the Blocked surface).
func (d *ChainDeps) Questions() executor.QuestionOps { return chainQuestions{d: d} }

// Preflight observes one repo for one run — the same derivation the admission gate uses.
func (d *ChainDeps) Preflight(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error) {
	return preflight.Derive(ctx, d, repo, run)
}

// StackPosition measures where this run's task branch sits relative to the current stack tip
// (deliver.NextPRBase): the tip's commit, the fork commit they share, how far behind the branch is,
// and the deliveries that landed in between (title + branch). It reads the local checkout and the
// ledger, never mutating anything — the rebase itself is the implement stage's act. nil (no error)
// when the runner has no checkout, the branch carries no work, or the tip cannot be resolved: a
// branch with nothing on it is never behind, so there is simply nothing to record.
func (d *ChainDeps) StackPosition(ctx context.Context, repo string, run runs.Run) (*preflight.Understack, error) {
	taskBranch := deliveringBranchFor(run, repo)

	b, ok, err := d.observeBench(ctx, repo)
	if err != nil || !ok {
		return nil, err // no checkout → nothing to measure (err nil when the repo is simply absent)
	}
	bb, err := b.On(taskBranch)
	if err != nil {
		return nil, err
	}
	tipBranch, err := d.Deliver().NextPRBase(ctx, repo, taskBranch)
	if err != nil {
		return nil, err
	}
	tip, fork, behind, exists, err := bb.StackPosition(ctx, tipBranch)
	if err != nil || !exists {
		return nil, err
	}
	u := &preflight.Understack{TipBranch: tipBranch, TipCommit: tip, ForkCommit: fork, Behind: behind}
	if behind > 0 {
		u.Intervening = d.interveningDeliveries(ctx, bb, repo, taskBranch, tip, fork)
	}
	return u, nil
}

// interveningDeliveries names the deliveries that landed between a branch's fork point and the
// current tip: every ledger delivery of the repo (other than the task's own branch) whose merged
// commit is reachable from the tip but NOT from the fork. Read-only reachability probes over the
// checkout; the title is joined through the delivery's execution. Deduped by branch, best-effort —
// a delivery whose reachability cannot be decided is simply left out, never guessed in.
func (d *ChainDeps) interveningDeliveries(ctx context.Context, b *workbench.Bench, repo, taskBranch, tip, fork string) []preflight.DeliverySpan {
	if d.s.deliveries == nil {
		return nil
	}
	all, err := d.s.deliveries.All()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []preflight.DeliverySpan
	for _, del := range all {
		if !sameRepo(del.Repo, repo) || del.Branch == "" || del.Branch == taskBranch || del.ToCommit == "" || seen[del.Branch] {
			continue
		}
		inTip, err := b.Reaches(ctx, del.ToCommit, tip)
		if err != nil || !inTip {
			continue
		}
		afterFork, err := b.Reaches(ctx, del.ToCommit, fork)
		if err != nil || afterFork {
			continue // reachable from the fork ⇒ already present when the branch forked, not "in between"
		}
		seen[del.Branch] = true
		out = append(out, preflight.DeliverySpan{Title: d.deliveryTitle(del), Branch: del.Branch})
	}
	return out
}

// deliveryTitle resolves the human title of a delivery through its execution — the archived result
// carries it directly, else the run behind the execution does. It falls back to the delivery's
// branch (which encodes the task's description) so the section is never nameless.
func (d *ChainDeps) deliveryTitle(del runs.Delivery) string {
	if d.s.results != nil && del.ExecutionID != "" {
		if res, ok, err := d.s.results.Get(del.ExecutionID); err == nil && ok && res.RunTitle != "" {
			return res.RunTitle
		}
	}
	if d.s.runs != nil {
		if runID := d.runIDOf(del.ExecutionID); runID != "" {
			if r, ok, err := d.s.runs.Get(runID); err == nil && ok && r.Title != "" {
				return r.Title
			}
		}
	}
	if del.Branch != "" {
		return del.Branch
	}
	return "a delivery"
}

// RequestRestart is the B-3 seam into sched (handover restart after a self-repo install).
func (d *ChainDeps) RequestRestart(by model.Actor) error {
	if d.hooks.RequestRestart == nil {
		return errors.New("no restart coordinator wired — the handover cannot be requested")
	}
	return d.hooks.RequestRestart(by)
}

// PauseAllUsageLimit is the collective usage-limit pause hook (REQ-016).
func (d *ChainDeps) PauseAllUsageLimit(msg string, notBefore time.Time) error {
	if d.hooks.PauseAllUsageLimit == nil {
		return errors.New("no pause coordinator wired")
	}
	return d.hooks.PauseAllUsageLimit(msg, notBefore)
}

// RecordAiUsage reports consumption into the ONE usage pool (cross-cutting 5).
func (d *ChainDeps) RecordAiUsage(u telemetry.UsageSample) { d.s.recordAiUsage(u) }

// Publish ticks the live stream after a write, exactly as a request handler does.
func (d *ChainDeps) Publish(t live.Topic) { d.s.publish(t) }

// Now is the motor's clock.
func (d *ChainDeps) Now() time.Time { return time.Now().UTC() }

// ── preflight.Sources ────────────────────────────────────────────────────────────────────
//
// Observation is deliberately LOCK-FREE: the admission gate runs on a caller's request, so it must
// never wait out a running execution that holds the repo's workspace. It reads refs, never the
// working tree, and it prepares nothing.

// WorkbenchState reports whether the workbench carries work the default branch does not. A repo the
// runner has never cloned is not an error — there simply is no workbench yet.
func (d *ChainDeps) WorkbenchState(ctx context.Context, repo, branch string) (bool, string, error) {
	b, ok, err := d.observeBench(ctx, repo)
	if err != nil || !ok {
		return false, "", err
	}
	on, err := b.On(branch)
	if err != nil {
		return false, "", err
	}
	return on.AheadOfDefault(ctx)
}

// ContainedInDefault reports whether a commit arrived in the default branch. Without a local
// observation point this is UNKNOWN — an error, so the caller defers by name instead of guessing
// (REQ-039.2).
func (d *ChainDeps) ContainedInDefault(ctx context.Context, repo, commit string) (bool, error) {
	b, ok, err := d.observeBench(ctx, repo)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("no local checkout of %s — arrival in the default branch cannot be confirmed", repo)
	}
	return b.ContainedInDefault(ctx, commit)
}

// OpenPRByHead returns the open pull request on a head branch, nil when there is none.
func (d *ChainDeps) OpenPRByHead(ctx context.Context, repo, head string) (*model.PRRef, error) {
	if d.initErr != nil {
		return nil, d.initErr
	}
	full, err := d.fullName(ctx, repo)
	if err != nil {
		return nil, err
	}
	pr, found := github.FindOpenPullRequest(ctx, d.token, full, head)
	if !found {
		return nil, nil
	}
	return &model.PRRef{Number: pr.Number, URL: pr.HTMLURL, HeadBranch: head}, nil
}

// RunDeliveries returns the ledger deliveries this run produced at one repo, oldest first. The join
// run→execution→delivery runs over the execution documents, and the repo is matched in both forms
// the ledger may carry (the full name it stores, the id a target names).
func (d *ChainDeps) RunDeliveries(runID, repo string) ([]runs.Delivery, error) {
	if d.s.deliveries == nil {
		return nil, errors.New("the delivery ledger is not available")
	}
	all, err := d.s.deliveries.All()
	if err != nil {
		return nil, err
	}
	out := []runs.Delivery{}
	for _, del := range all {
		if !sameRepo(del.Repo, repo) {
			continue
		}
		if d.runIDOf(del.ExecutionID) != runID {
			continue
		}
		out = append(out, del)
	}
	return out, nil
}

// runIDOf resolves the run behind an execution id through the state documents (and the result
// archive, so an execution whose document was pruned still joins).
func (d *ChainDeps) runIDOf(execID string) string {
	if execID == "" {
		return ""
	}
	if d.s.docs != nil {
		if doc, ok, err := d.s.docs.Get(execID); err == nil && ok {
			return doc.RunID
		}
	}
	if d.s.results != nil {
		if res, ok, err := d.s.results.Get(execID); err == nil && ok {
			return res.RunID
		}
	}
	return ""
}

// sameRepo compares a ledger repo against a target repo, tolerating the two forms in play: the
// GitHub full name and the bare repo id.
func sameRepo(ledger, target string) bool {
	return ledger == target || repoShort(ledger) == repoShort(target)
}

// observeBench opens a READ-ONLY bench on an already-cloned workspace. ok=false means the runner
// has no checkout of this repo — a fact, not a failure.
func (d *ChainDeps) observeBench(ctx context.Context, repo string) (*workbench.Bench, bool, error) {
	if d.initErr != nil {
		return nil, false, d.initErr
	}
	if d.s.workspaces == nil {
		return nil, false, errors.New("no workspace manager")
	}
	// An execution of this repo may already hold a prepared bench — use it rather than opening a
	// second view on the same tree.
	d.mu.Lock()
	rb, held := d.benches[repo]
	d.mu.Unlock()
	if held && rb.err == nil {
		return rb.bench, true, nil
	}
	if !d.s.workspaces.Exists(d.user, repo) {
		return nil, false, nil
	}
	wt, err := d.s.workspaces.Path(d.user, repo)
	if err != nil {
		return nil, false, err
	}
	ex := d.runnerExec()
	b, err := workbench.New(&ex, wt)
	if err != nil {
		return nil, false, err
	}
	// STEP 1 of the branch-per-task rebuild: the bench now CARRIES the branch it works on instead
	// of reaching for one shared name. The observation path still names the retired shared branch,
	// because that is where today's undelivered work lies; the per-task name is wired in with the
	// stage that creates it.
	if b, err = b.On(workbench.LegacyShared); err != nil {
		return nil, false, err
	}
	b = b.WithToken(d.token)
	// Best-effort refresh of the remote refs: observation wants the CURRENT default branch, but an
	// unreachable remote must not turn the observation into a failure — the refs on disk still
	// answer, and a stale answer errs toward "work present", never toward "already delivered".
	_ = ex.Fetch(ctx, wt, d.token)
	return b, true, nil
}

// ── the workbench shim ───────────────────────────────────────────────────────────────────

type benchOps struct {
	d    *ChainDeps
	repo string
}

// prepare maps the workbench's own report onto the motor's PrepareInfo (the seam keeps its own
// shape so the motor stays mockable without importing the workbench).
func (o benchOps) Prepare(ctx context.Context, branch, base string) (executor.PrepareInfo, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return executor.PrepareInfo{}, err
	}
	res, err := b.Prepare(ctx, branch, base)
	info := executor.PrepareInfo{
		Created:       res.Created,
		FoldedRemote:  res.FoldedRemote,
		FoldedDefault: res.FoldedDefault,
		Conflicted:    res.Conflicted,
		ConflictFiles: res.ConflictFiles,
		Head:          res.Head,
	}
	return info, err
}

func (o benchOps) CleanUntracked(ctx context.Context) error {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return err
	}
	return b.CleanUntracked(ctx)
}

func (o benchOps) Head(ctx context.Context) (string, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return "", err
	}
	return b.Head(ctx)
}

func (o benchOps) CurrentBranch(ctx context.Context) string {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return ""
	}
	return b.CurrentBranch(ctx)
}

func (o benchOps) CommitsAhead(ctx context.Context, since string) (int, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return 0, err
	}
	return b.CommitsAhead(ctx, since)
}

func (o benchOps) HasUncommitted(ctx context.Context) (bool, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return false, err
	}
	return b.HasUncommitted(ctx)
}

func (o benchOps) CommitAll(ctx context.Context, message string) (string, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return "", err
	}
	login, id := o.d.runnerIdentity(ctx)
	return b.CommitAll(ctx, message, login, id)
}

func (o benchOps) Publish(ctx context.Context) error {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return err
	}
	return b.Publish(ctx)
}

func (o benchOps) ReadFile(ctx context.Context, rel string) (string, bool, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return "", false, err
	}
	return b.ReadFile(rel)
}

func (o benchOps) WriteFile(ctx context.Context, rel string, data []byte) error {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return err
	}
	return b.WriteFile(rel, data)
}

func (o benchOps) BranchAt(ctx context.Context, name, at string) error {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return err
	}
	return b.BranchAt(ctx, name, at)
}

func (o benchOps) PushBranch(ctx context.Context, name string) error {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return err
	}
	return b.PushBranch(ctx, name)
}

func (o benchOps) MergeBaseDefault(ctx context.Context) (string, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return "", err
	}
	return b.MergeBaseDefault(ctx)
}

func (o benchOps) CatchUpOnto(ctx context.Context, tipBranch string) (executor.CatchUpInfo, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return executor.CatchUpInfo{}, err
	}
	rep, err := b.CatchUpOnto(ctx, tipBranch)
	return executor.CatchUpInfo{
		Rebased:       rep.Rebased,
		Conflicted:    rep.Conflicted,
		ConflictFiles: rep.ConflictFiles,
		OldHead:       rep.OldHead,
		NewHead:       rep.NewHead,
	}, err
}

// runnerIdentity resolves the commit identity of the runner's linked account (best-effort — the
// workbench falls back to the neutral identity).
func (d *ChainDeps) runnerIdentity(ctx context.Context) (string, int64) {
	if d.token == "" {
		return "", 0
	}
	v, err := github.GetViewer(ctx, d.token)
	if err != nil {
		return "", 0
	}
	return v.Login, v.ID
}

// ── the GitHub shim ──────────────────────────────────────────────────────────────────────

type chainGitHub struct{ d *ChainDeps }

func (g chainGitHub) DefaultBranch(ctx context.Context, repo string) (string, error) {
	if g.d.initErr != nil {
		return "", g.d.initErr
	}
	full, err := g.d.fullName(ctx, repo)
	if err != nil {
		return "", err
	}
	return github.DefaultBranch(ctx, g.d.token, full)
}

// CreateRepo creates the repository AND protects it in the same pass — deliver.CreateProtectedRepo
// is the ONE way the system creates a repo (REQ-006.2/REQ-033.6), so no second creation path
// exists. An already-existing repo is Satisfied, which the motor reads as success.
func (g chainGitHub) CreateRepo(ctx context.Context, repo string, _ bool) error {
	if g.d.initErr != nil {
		return g.d.initErr
	}
	if _, err := deliver.CreateProtectedRepo(ctx, deliverOps(g.d.s, g.d.token), repoShort(repo)); err != nil {
		return err
	}
	// The freshly created repo widens the runner's visible set — drop the cached view so the
	// following stages resolve its full name instead of the pre-creation guess.
	discover.InvalidateUser(runsTokenUser())
	return nil
}

// ── the delivery shim ────────────────────────────────────────────────────────────────────

type chainDeliver struct{ d *ChainDeps }

func (c chainDeliver) NextPRBase(ctx context.Context, repo, head string) (string, error) {
	if c.d.s.deliveries == nil {
		return "", errors.New("the delivery ledger is not available")
	}
	full, err := c.d.fullName(ctx, repo)
	if err != nil {
		return "", err
	}
	open, err := c.d.s.deliveries.Open(full)
	if err != nil {
		return "", err
	}
	// The stack top comes from the LEDGER, which is local. GitHub is only needed for the name of
	// the default branch, and only when no delivery OTHER THAN head is open — so the common case
	// asks GitHub nothing at all. This matters because the base is now resolved before every task
	// starts: a throttled GitHub would otherwise stop the chain from beginning any work, which it
	// did on 2026-07-31 ("resolve the base of this task's branch: github: 403"). head is excluded
	// by the pure function, so this delivery is never chosen as its own base.
	if base := deliver.NextPRBase(open, head, ""); base != "" {
		return base, nil
	}
	def, err := github.DefaultBranch(ctx, c.d.token, full)
	if err != nil {
		// The local clone knows its default branch too — the SAME truth, read from the checkout
		// instead of over the network. This is not a guess: an unresolvable name still fails.
		if b, ok, berr := c.d.observeBench(ctx, repo); berr == nil && ok {
			if local, lerr := b.DefaultBranchName(); lerr == nil && local != "" {
				return local, nil
			}
		}
		return "", err
	}
	return deliver.NextPRBase(open, head, def), nil
}

// OpenOrAdoptPR writes the ledger INTENT and then rides deliver.OpenOrAdoptPR — the one PR path
// (K-6). Intent before effect: the delivery record exists before the pull request, so a crash
// between the two leaves a reproducible span rather than an orphaned PR.
func (c chainDeliver) OpenOrAdoptPR(ctx context.Context, in executor.DeliverPRIn) (model.PRRef, bool, error) {
	if c.d.s.deliveries == nil {
		return model.PRRef{}, false, errors.New("the delivery ledger is not available")
	}
	full, err := c.d.fullName(ctx, in.Repo)
	if err != nil {
		return model.PRRef{}, false, err
	}
	if in.DeliveryID != "" {
		if _, ok, err := c.d.s.deliveries.ByID(in.DeliveryID); err != nil {
			return model.PRRef{}, false, err
		} else if !ok {
			if err := c.d.s.deliveries.Put(runs.Delivery{
				ID: in.DeliveryID, ExecutionID: in.ExecutionID,
				Repo: full, Branch: in.Head,
				FromCommit: in.FromCommit, ToCommit: in.ToCommit,
				CreatedAt: time.Now().UTC(),
			}); err != nil {
				return model.PRRef{}, false, err
			}
		}
	}
	ref, adopted, err := deliver.OpenOrAdoptPR(ctx, deliverOps(c.d.s, c.d.token), c.d.s.deliveries, deliver.PRIn{
		Repo: full, Head: in.Head, Base: in.Base, Title: in.Title, Body: in.Body, DeliveryID: in.DeliveryID,
	})
	if err == nil {
		c.d.s.publish(live.TopicDeliveries)
		if c.d.s.runPRs != nil && ref.Number > 0 {
			now := time.Now().UTC()
			_ = c.d.s.runPRs.Add(runs.PendingPR{
				Repo: full, Number: ref.Number, URL: ref.URL, DeliveryID: in.DeliveryID,
				CreatedAt: now, MergeBy: now.Add(c.d.s.automergeWindow()),
			})
		}
	}
	return ref, adopted, err
}

// FailedTip resolves the repo's full name (the ledger's key) and reports the failed delivery at the
// tip, or nil when the tip is sound — the ledger side of "a failed order becomes the tip". The
// stack top is read from the LOCAL ledger, so the check the implement stage runs before every order
// makes no network call.
func (c chainDeliver) FailedTip(ctx context.Context, repo string) (*runs.Delivery, error) {
	if c.d.s.deliveries == nil {
		return nil, errors.New("the delivery ledger is not available")
	}
	full, err := c.d.fullName(ctx, repo)
	if err != nil {
		return nil, err
	}
	return deliver.FailedTip(c.d.s.deliveries, full)
}

// RecordFailedDelivery leaves the durable "Lieferung gescheitert" mark and ticks the deliveries
// topic so the ledger surface shows the failed tip without a reload.
func (c chainDeliver) RecordFailedDelivery(ctx context.Context, in executor.DeliverFailedIn) error {
	if c.d.s.deliveries == nil {
		return errors.New("the delivery ledger is not available")
	}
	full, err := c.d.fullName(ctx, in.Repo)
	if err != nil {
		return err
	}
	if err := deliver.RecordFailedDelivery(c.d.s.deliveries, deliver.FailedDeliveryIn{
		DeliveryID: in.DeliveryID, ExecutionID: in.ExecutionID,
		Repo: full, Branch: in.Branch,
		FromCommit: in.FromCommit, ToCommit: in.ToCommit, Reason: in.Reason,
	}); err != nil {
		return err
	}
	c.d.s.publish(live.TopicDeliveries)
	return nil
}

func (c chainDeliver) EnsureProtection(ctx context.Context, repo string) error {
	full, err := c.d.fullName(ctx, repo)
	if err != nil {
		return err
	}
	ops := deliverOps(c.d.s, c.d.token)
	if err := ops.ProtectDefaultBranch(ctx, full, deliver.OriginStatusContext); err != nil {
		return err
	}
	return nil
}

// ── the question shim ────────────────────────────────────────────────────────────────────

type chainQuestions struct{ d *ChainDeps }

// OpenForRepo resolves the repo's full name (the pool stores questions under the target as the run
// named it, but a full name may reach it too) and reports the open question holding it, if any.
func (c chainQuestions) OpenForRepo(ctx context.Context, repo, exceptRunID string) (*runs.Question, error) {
	if c.d.s.runQuestions == nil {
		return nil, nil
	}
	return c.d.s.runQuestions.OpenForRepo(repo, exceptRunID)
}

func (c chainQuestions) AnsweredForRun(ctx context.Context, runID, repo string) (*runs.Question, error) {
	if c.d.s.runQuestions == nil {
		return nil, nil
	}
	return c.d.s.runQuestions.AnsweredForRun(runID, repo)
}

func (c chainQuestions) WithdrawForRun(ctx context.Context, runID, repo, qKind string) (int, error) {
	if c.d.s.runQuestions == nil {
		return 0, nil
	}
	n, err := c.d.s.runQuestions.WithdrawForRun(runID, repo, qKind)
	if err == nil && n > 0 {
		c.d.s.publish(live.TopicQuestions)
	}
	return n, err
}

// Raise records the question and ticks the Blocked surface. Outward delivery to the user rides the
// question pool's on-new hook (StartQuestionDelivery), which records a disturbance notice — the SAME
// path the sister order built for disturbances, never a second one.
func (c chainQuestions) Raise(ctx context.Context, q runs.Question) (runs.Question, error) {
	if c.d.s.runQuestions == nil {
		return runs.Question{}, errors.New("the question pool is not available")
	}
	saved, err := c.d.s.runQuestions.Raise(q)
	if err != nil {
		return runs.Question{}, err
	}
	c.d.s.publish(live.TopicQuestions)
	return saved, nil
}

func (c chainQuestions) Resolve(ctx context.Context, id string) error {
	if c.d.s.runQuestions == nil {
		return nil
	}
	if err := c.d.s.runQuestions.Resolve(id); err != nil {
		return err
	}
	c.d.s.publish(live.TopicQuestions)
	return nil
}

// ── the deploy shim ──────────────────────────────────────────────────────────────────────

type chainDeploy struct{ d *ChainDeps }

// selfRepo names the repository this daemon itself is built from — the one whose delivery ends in a
// handover restart instead of a unit restart (B-2). The default matches the install wrapper's.
func selfRepo() string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_SELF_REPO")); v != "" {
		return v
	}
	return "devlab"
}

// dashboardRepo names the central dashboard's repository (short name). The dashboard is a landscape
// member built differently from the Go services and reachable without a service route of its own, so
// the edge routes never name it — the roster adds it explicitly. Its identity is instance
// configuration, never a literal (Keine Instanz-Spezifika): DEVLAB_HOLISTIC_DASHBOARD_REPO names it
// outright, else it is the directory name of the configured holistic checkout (DEVLAB_HOLISTIC_REPO),
// which IS that repository's working copy — so the dashboard's desired production state is that
// repository's standard-branch tip, determined the same derived way as any service, never from the
// delivery history. "" when neither is configured: the dashboard is then not asserted onto the roster
// (a configuration gap named in the report, not a false claim that production is complete without it).
func dashboardRepo() string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_HOLISTIC_DASHBOARD_REPO")); v != "" {
		return v
	}
	if p := strings.TrimSpace(os.Getenv("DEVLAB_HOLISTIC_REPO")); p != "" {
		return filepath.Base(filepath.Clean(p))
	}
	return ""
}

// chainLandscape derives the production landscape roster — the SOURCE OF TRUTH for what production
// must carry — the way Atlas derives the port ledger: from the edge routes the host actually serves
// (a service is a landscape member because the edge routes it), never stored, never the delivery
// history. To the routed services it adds the two load-bearing members that carry no edge route of
// their own: devlab itself (which holds Mercury and the chain — without it on production, production
// cannot keep itself supplied) and the central dashboard (built differently from the Go services).
// Every id is mapped to its full owner/repo name so the reconciliation, the ledger and the send all
// speak the one value form.
type chainLandscape struct{}

func (chainLandscape) Services(_ context.Context) ([]string, error) {
	owner, err := discover.Owner()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(short string) {
		short = strings.TrimSpace(short)
		if short == "" {
			return
		}
		full := owner + "/" + short
		if seen[full] {
			return
		}
		seen[full] = true
		out = append(out, full)
	}
	for _, id := range atlas.RoutedServiceIDs() {
		add(id)
	}
	add(selfRepo())
	add(dashboardRepo())
	sort.Strings(out)
	return out, nil
}

func (c chainDeploy) Detect(ctx context.Context, repo string) (executor.Detection, error) {
	_, wt, err := c.d.bench(ctx, repo)
	if err != nil {
		return executor.Detection{}, err
	}
	det, err := deploy.Detect(wt)
	if err != nil {
		return executor.Detection{}, err
	}
	return executor.Detection{Kind: string(det.Kind), Evidence: det.Evidence}, nil
}

// DeliverDev builds the artifact AS THE RUNNER, installs it through the pinned root wrapper, and
// proves the result honestly (F10). The self repo takes the handover path: install now, restart
// later, outside this process (K-2).
func (c chainDeploy) DeliverDev(ctx context.Context, repo string) (executor.DeployOutcome, error) {
	_, wt, err := c.d.bench(ctx, repo)
	if err != nil {
		return executor.DeployOutcome{}, err
	}
	det, err := deploy.Detect(wt)
	if err != nil {
		return executor.DeployOutcome{}, err
	}
	ex := c.d.runnerExec()
	artifact, err := deploy.Build(ctx, ex, wt)
	if err != nil {
		return executor.DeployOutcome{}, err
	}
	self := repoShort(repo) == selfRepo()
	if self {
		// The chain ships only the daemon binary; the root wrappers under sbin are replaced solely
		// by a human with sudo (E §7.4). So BEFORE installing a new binary, prove the installed
		// wrappers still match the branch BEING DELIVERED — otherwise a change touching
		// deploy/devlab-install or deploy/devlab-exec would go live half-shipped and report green
		// (measured 31.07./01.08.2026, and the false negative on 2026-08-04 where the shared working
		// tree sat on the standard branch and hid the run's own change). The gate measures the
		// delivering branch's committed ref, not the working tree, so a run's change is seen.
		// A drift is a NAMED failure that installs nothing (no half state, K-4); it names each stale
		// wrapper and the one line a human runs to make them current, then resumes the execution.
		if err := deploy.GuardWrappersCurrent(wt, c.d.deliveringBranch(repo)); err != nil {
			// The refusal is real — nothing is installed. Instead of failing silently every night, the
			// deliver-dev stage turns it into a wrapper-renewal question the user must approve. We name
			// the refusal with the motor's sentinel and carry the exact difference; the guard above STILL
			// gates the install, so a resume proceeds only once the scripts genuinely match again — the
			// approval never writes them and never bypasses this content check.
			return executor.DeployOutcome{Self: true, WrapperDrift: err.Error()},
				fmt.Errorf("%w: %s", executor.ErrWrappersStale, err.Error())
		}
		if err := deploy.SelfInstallAndHandover(ctx, deploy.SudoInstaller{}, repoShort(repo), artifact); err != nil {
			return executor.DeployOutcome{Self: true}, err
		}
		return executor.DeployOutcome{
			Installed: true, Running: true, Self: true,
			Detail: "artifact installed; the restart is handed over to the root wrapper and fires once the slots are free",
		}, nil
	}
	out, err := deploy.DeliverDev(ctx, deploy.SudoInstaller{}, deploy.LivePorts{}, deploy.DefaultGate(), det, repoShort(repo), artifact)
	return executor.DeployOutcome{
		Installed: out.Installed, Running: out.Running, Port: out.Port, Detail: out.Detail,
		UI: string(out.UI), UIDetail: out.UIDetail,
	}, err
}

// DeployProd is the production step — the LAST step of the chain (WHAT-1), reached only after a
// delivery has MERGED. It ships the MERGED state (the default branch, never the dev branch — prod is
// fed exclusively from the default branch) and proves the service running on the target through the
// SAME honest gate the dev delivery uses (WHAT-2), executed server-side by the receiver. A repository
// that is no service is a PROVEN not-applicable outcome; a missing production configuration is a
// deficiency reported as a failure, never a silent skip ("Kein stummes Ausbleiben").
func (c chainDeploy) DeployProd(ctx context.Context, repo string) (deliver.ProdOutcome, error) {
	bench, wt, err := c.d.bench(ctx, repo)
	if err != nil {
		return deliver.ProdOutcome{}, err
	}
	full, err := c.d.fullName(ctx, repo)
	if err != nil {
		return deliver.ProdOutcome{}, err
	}

	// The merged state lives on the default branch: fetch and check its tip out (detached) so the
	// artifact is built from exactly what merged, never from the dev branch's newer, unmerged layers.
	def, err := github.DefaultBranch(ctx, c.d.token, full)
	if err != nil || def == "" {
		return deliver.ProdOutcome{}, fmt.Errorf("deploy: resolve default branch of %s: %v", repo, err)
	}
	ex := c.d.runnerExec()
	if err := ex.Fetch(ctx, wt, c.d.token); err != nil {
		return deliver.ProdOutcome{}, fmt.Errorf("deploy: fetch %s: %w", repo, err)
	}
	// The merged state is named by a REMOTE-TRACKING ref (origin/<default>), not a local branch, and
	// the intent is a DETACHED build from exactly what merged — so it is checked out with the detached
	// primitive. Plain Checkout (git switch -- origin/main) is the wrong tool here: it accepts only a
	// local branch and refuses the remote-tracking ref outright.
	if err := ex.CheckoutDetached(ctx, wt, "origin/"+def); err != nil {
		return deliver.ProdOutcome{}, fmt.Errorf("deploy: check out the merged state (origin/%s) of %s: %w", def, repo, err)
	}
	_ = bench // the checkout above pins the worktree to the merged tip for the build below
	// The exact standard-branch commit this send builds from and ships — read from the tree that was
	// just checked out, so the production-state record names precisely what production will carry.
	shipped, _ := ex.RevParse(ctx, wt, "HEAD")

	// A repository that is no service proves production inapplicable — a not-applicable outcome, never
	// a failure. A structure violation is a failure (there is a service shape but it does not reduce).
	det, err := deploy.Detect(wt)
	if err != nil {
		return deliver.ProdOutcome{}, err
	}
	// The central dashboard is a landscape member built differently from the Go services (a frontend,
	// no cmd/<id>d daemon), so Detect classifies it a library. Left there it would settle as "not a
	// service" and vanish silently — but a landscape member absent from production is a Mangel, not a
	// proven not-applicable property (das Fehlen einer Einrichtung ist keine solche Eigenschaft). Its
	// production build-and-serve — build the holistic bundle on the dev host, ship it over THIS send,
	// the receiver installs it into the production serve root — is the receiver's half, the same
	// division that already governs the Go-service receiver (a separate root artifact on the prod host,
	// outside this repo). Until that half is armed the dashboard is surfaced as a NAMED, retrying
	// deficiency, never a silent skip ("Kein stummes Ausbleiben").
	if repoShort(repo) == dashboardRepo() && det.Kind != deploy.KindService {
		return deliver.ProdOutcome{Commit: shipped, Detail: "the central dashboard belongs on production, but its " +
				"production build-and-serve path is not yet wired into the chain (it is built differently from the Go " +
				"services); this is a deficiency reported until the dashboard receiver half is armed, never a silent skip"},
			fmt.Errorf("deploy: the central dashboard %s has no production delivery path yet — determine its desired "+
				"state from the holistic checkout and ship its built bundle over this send once the receiver installs it", repo)
	}
	switch det.Kind {
	case deploy.KindLibrary, deploy.KindExcluded, deploy.KindTemplate:
		return deliver.ProdOutcome{NotApplicable: true, Evidence: det.Evidence, Commit: shipped,
			Detail: "not a service — nothing to run in production"}, nil
	case deploy.KindNonconforming:
		return deliver.ProdOutcome{Detail: det.Evidence},
			fmt.Errorf("%w: %s", deploy.ErrNonconforming, det.Evidence)
	}

	// The production target comes EXCLUSIVELY from server-side configuration (never a request). A
	// missing target is a deficiency: it is reported as a failure that keeps retrying, not a skip.
	cfg, cfgErr := prodConfigFromEnv(c.d.s.paths.ProdKnownHosts())
	if cfgErr != nil {
		return deliver.ProdOutcome{Detail: cfgErr.Error()}, cfgErr
	}

	artifact, err := c.d.buildProd(ctx, ex, wt)
	if err != nil {
		return deliver.ProdOutcome{}, fmt.Errorf("deploy: build the production artifact of %s: %w", repo, err)
	}
	// The first-time SETUP product (unit/route/rights) is laid INTO the artifact so the receiver can
	// set the service up on a bare host without any manual per-service step. It travels beside the
	// program; the send below ships it, the receiver installs it only when the unit is still missing.
	if err := c.d.emitProdSetup(ctx, ex, wt, repoShort(repo), det); err != nil {
		return deliver.ProdOutcome{Detail: err.Error()}, fmt.Errorf("deploy: prepare production setup of %s: %w", repo, err)
	}
	// The send expects the SHORT repo id (its name grammar rejects a slash); the ledger carries the
	// full "owner/repo", so it is reduced here — the same value-form seam that the workbench needs.
	if err := c.d.sendProd(ctx, cfg, repoShort(repo), artifact); err != nil {
		// A CHANGED production host key is not a plain failure: it is carried up as its own signal so the
		// production pass can hold the send and ask for a deliberate approval, instead of masking it as a
		// generic "connection failed" retried in silence (task part 2).
		var hk *deploy.HostKeyChangedError
		if errors.As(err, &hk) {
			return deliver.ProdOutcome{HostKeyChanged: true, HostKeyTarget: hk.Target, Detail: err.Error()}, err
		}
		return deliver.ProdOutcome{Detail: err.Error()}, err
	}
	// The receiver installed the prebuilt artifact AND proved the service running on the target, so a
	// clean send is the honest running proof (F10), executed on the second machine. Commit names the
	// standard-branch state now running in production, for the production-state record.
	return deliver.ProdOutcome{Running: true, Commit: shipped,
		Detail: "shipped to production and proven running on the target"}, nil
}

// DefaultBranchTip resolves the current commit at the tip of a repository's standard (default)
// branch — the state production must carry — with ONE GitHub read and no worktree. It is the measure
// the production reconciliation compares production's recorded commit against. A missing token or an
// unresolved default branch yields "" (no error) so the reconciliation leaves the repository for the
// next pass rather than guessing at the standard branch.
func (c chainDeploy) DefaultBranchTip(ctx context.Context, repo string) (string, error) {
	full, err := c.d.fullName(ctx, repo)
	if err != nil {
		return "", err
	}
	def, err := github.DefaultBranch(ctx, c.d.token, full)
	if err != nil || def == "" {
		return "", err
	}
	return github.BranchTip(ctx, c.d.token, full, def)
}

// prodConfigFromEnv assembles the production send from server-side configuration only (E §7.4): the
// rsync staging target, the receiver the forced-command install fires on, and the deploy key BOTH
// calls authenticate with. All three are required — arming production means naming where it goes AND
// how it signs in; a half-configured target is a deficiency, not a partial delivery. The key is
// NAMED here exactly like the target and receiver (one place, one key, no second path beside it);
// its file content stays out of the process — only the path is handled. The known-hosts location is
// the durable state-root file the service user can write, because that user has no home ssh
// directory. Instance values live ONLY here in the runtime environment (Keine Instanz-Spezifika).
func prodConfigFromEnv(knownHosts string) (deploy.ProdConfig, error) {
	target := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_PROD_TARGET"))
	recv := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_PROD_RECV"))
	key := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_PROD_KEY"))
	if target == "" || recv == "" || key == "" {
		return deploy.ProdConfig{}, errors.New("production delivery is not configured — set DEVLAB_RUNS_PROD_TARGET " +
			"(the rsync staging target), DEVLAB_RUNS_PROD_RECV (the deploy-key receiver host), and DEVLAB_RUNS_PROD_KEY " +
			"(the deploy private key both calls sign in with); until then the merged work reaches the default branch " +
			"but not production")
	}
	id := deploy.ProdIdentity{KeyFile: key, KnownHostsFile: knownHosts}
	return deploy.ProdConfig{RsyncTarget: target, Identity: id, Trigger: deploy.SSHTrigger(recv, id)}, nil
}

// StackTipWrapperDrift reports which root wrappers' installed copies differ from the STACK TIP
// (deliver.NextPRBase — another open delivery's branch, else the standard branch), never main alone.
// Only the self repo ships the root wrappers, so a foreign repo has none.
func (c chainDeploy) StackTipWrapperDrift(ctx context.Context, repo string) ([]runs.WrapperGrant, error) {
	if repoShort(repo) != selfRepo() {
		return nil, nil
	}
	_, wt, err := c.d.bench(ctx, repo)
	if err != nil {
		return nil, err
	}
	tip, err := c.d.Deliver().NextPRBase(ctx, repo, c.d.deliveringBranch(repo))
	if err != nil {
		return nil, fmt.Errorf("resolve the stack tip for the wrapper drift probe: %w", err)
	}
	return wrapperGrants(deploy.StackTipWrapperDrift(wt, tip))
}

// WorkingWrapperDrift reports which root wrappers' installed copies differ from THIS run's own
// delivering branch — the content the run itself changed but has not yet merged. It is the second
// renewal source (beside StackTipWrapperDrift): when the installed scripts already match the stack tip,
// this is what lets a run that changed a root script ask for approval instead of halting for good. It
// reads the delivering branch's committed ref (the same tree the gate measures), so a run's change is
// seen even when the shared working tree sits on the standard branch. Only the self repo owns these
// root wrappers, so a foreign repo has none.
func (c chainDeploy) WorkingWrapperDrift(ctx context.Context, repo string) ([]runs.WrapperGrant, error) {
	if repoShort(repo) != selfRepo() {
		return nil, nil
	}
	_, wt, err := c.d.bench(ctx, repo)
	if err != nil {
		return nil, err
	}
	return wrapperGrants(deploy.DeliveringBranchWrapperDrift(wt, c.d.deliveringBranch(repo)))
}

// wrapperGrants projects deploy drifts onto the (name, checksum, summary) grants a renewal question
// carries — one shared projection for both drift sources (Keine Redundanz).
func wrapperGrants(drifts []deploy.WrapperDrift, err error) ([]runs.WrapperGrant, error) {
	if err != nil {
		return nil, err
	}
	grants := make([]runs.WrapperGrant, 0, len(drifts))
	for _, d := range drifts {
		grants = append(grants, runs.WrapperGrant{Name: d.Name, SHA: d.WantSHA, Summary: d.Summary})
	}
	return grants, nil
}

// RenewApprovedWrappers is the daemon side of the WRITE half: for each wrapper the user approved, it
// re-reads the content from the SAME source the approval was built from (the stack tip or this run's
// delivering branch, per q.WrapperSource), stages a run-unwritable grant, and calls the root tool
// (`sudo devlab-install --renew-wrapper`). It skips a wrapper already at the approved checksum
// (idempotent) and refuses a non-self repo — only the self repo owns these root wrappers.
func (c chainDeploy) RenewApprovedWrappers(ctx context.Context, repo string, q runs.Question) error {
	if repoShort(repo) != selfRepo() {
		return fmt.Errorf("refusing wrapper renewal for non-self repository %q", repo)
	}
	_, wt, err := c.d.bench(ctx, repo)
	if err != nil {
		return err
	}
	grantDir := c.d.s.paths.WrapperGrants()
	renewer := deploy.SudoWrapperRenewer{}
	// Bind the re-read to the SAME source the approval named. WrapperSourceWorking re-reads this run's
	// own delivering branch; anything else (WrapperSourceStackTip, empty, or the retired "merged") the
	// stack tip. Either way the daemon re-reads the bytes and RenewApprovedWrapper refuses any that no
	// longer hash to the approved checksum, so a source that changed after the approval installs nothing.
	var src deploy.WrapperContentAt
	if q.WrapperSource == runs.WrapperSourceWorking {
		src = deploy.DeliveringBranchContent(wt, c.d.deliveringBranch(repo))
	} else {
		tip, terr := c.d.Deliver().NextPRBase(ctx, repo, c.d.deliveringBranch(repo))
		if terr != nil {
			return fmt.Errorf("resolve the stack tip for the approved wrapper renewal: %w", terr)
		}
		src = deploy.StackTipContent(wt, tip)
	}
	by := q.AnsweredBy.User
	if by == "" {
		by = q.AnsweredBy.OnBehalfOf
	}
	at := ""
	if q.AnsweredAt != nil {
		at = q.AnsweredAt.UTC().Format(time.RFC3339)
	}
	for _, g := range q.Wrappers {
		if deploy.InstalledWrapperMatches(g.Name, g.SHA) {
			continue // already renewed to this exact content — do not spend the single-use approval again
		}
		if err := deploy.RenewApprovedWrapper(ctx, renewer, src, grantDir, g.Name, g.SHA, q.ID, by, at); err != nil {
			return err
		}
	}
	return nil
}

// ── the agent stream ─────────────────────────────────────────────────────────────────────

// agentStream turns the blocking agent primitive (which streams stdout through a callback) into the
// motor's read-to-EOF-then-Wait shape. The pipe closes when the process ends, so the compaction
// loop terminates on its own; Wait then reports the invocation's outcome exactly once.
type agentStream struct {
	pr     *io.PipeReader
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	err    error
}

// streamFunc is the streaming primitive behind the adapter: it pushes stdout to onStdout as the
// invocation produces it and returns the invocation's outcome. Keeping it a seam is what makes the
// adapter testable without a claude binary and without sudo.
type streamFunc func(ctx context.Context, onStdout func(line []byte)) error

// startAgentStream runs the agent in the working tree through the ported per-user primitive.
func startAgentStream(ctx context.Context, ex workspace.Executor, wt string, args []string) *agentStream {
	return startAgentStreamWith(ctx, func(runCtx context.Context, onStdout func([]byte)) error {
		_, err := ex.AgentStream(runCtx, wt, onStdout, args...)
		return err
	})
}

func startAgentStreamWith(ctx context.Context, run streamFunc) *agentStream {
	runCtx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	a := &agentStream{pr: pr, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(a.done)
		a.err = run(runCtx, func(line []byte) {
			// The callback hands over ONE line WITHOUT its terminator (workspace.runAgentCmd trims it),
			// while the compaction on the other end splits the stream on '\n'. The terminator therefore
			// has to be put back HERE. Without it every event of an invocation is glued into a single
			// unparsable blob: no transcript line is ever emitted and the token counters stay at zero,
			// while the agent works perfectly normally and the stage still succeeds. That is exactly
			// how it stayed unnoticed — nothing fails, the numbers are just silently absent.
			buf := make([]byte, 0, len(line)+1)
			buf = append(buf, line...)
			buf = append(buf, '\n')
			// A write error means the reader is gone; the invocation is then stopped by the context.
			if _, werr := pw.Write(buf); werr != nil {
				cancel()
			}
		})
		_ = pw.Close() // EOF for the compaction loop
	}()
	return a
}

func (a *agentStream) Output() io.Reader { return a.pr }

// Wait blocks until the invocation finished and reports its outcome. Safe to call once the output
// reached EOF (the motor's order) and idempotent.
func (a *agentStream) Wait() error {
	a.once.Do(func() { <-a.done })
	return a.err
}

// Kill cancels the invocation; the underlying primitive signals the whole process group.
func (a *agentStream) Kill() error {
	a.cancel()
	return nil
}

// sessionUUID is the DERIVED name of one repository's agent conversation inside one execution.
// Derived, not drawn: the same execution and the same repository always yield the same name, so a
// resume finds the conversation again without any id having to be stored, and two repositories of
// one execution never share a conversation. The shape is a RFC-4122 version-5 UUID because that is
// the only shape the agent accepts as a session name.
func sessionUUID(key, repo string) string {
	sum := sha256.Sum256([]byte("devlab/agent-session\x00" + key + "\x00" + repo))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x50 // version 5
	b[8] = (b[8] & 0x3f) | 0x80 // RFC-4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
