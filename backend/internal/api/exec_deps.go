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
	"strings"
	"sync"
	"time"

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
	ex := workspace.Executor{User: d.user, PerUser: true}
	return startAgentStream(ctx, ex, wt, chainAgentArgs(repo, prompt, t, sess)), nil
}

// chainAgentArgs is the agent invocation of ONE stage, as a value — the seam that lets the
// invocation be verified without a workspace and without root.
func chainAgentArgs(repo, prompt string, t runs.ResolvedTuning, sess executor.AgentSession) []string {
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
	return append(args, "--append-system-prompt", chainPreamble(repo, t.Effort))
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
func chainPreamble(repo, effort string) string {
	s := "You are the autonomous DevLab runner. You work in the checked-out workspace of the " +
		"repository \"" + repo + "\" on its long-lived working branch " + workbench.Branch + ". " +
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
	ex := workspace.Executor{User: d.user, PerUser: true}
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

// Deploy is the delivery-to-host machinery (S11).
func (d *ChainDeps) Deploy() executor.DeployOps { return chainDeploy{d: d} }

// Preflight observes one repo for one run — the same derivation the admission gate uses.
func (d *ChainDeps) Preflight(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error) {
	return preflight.Derive(ctx, d, repo, run)
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
func (d *ChainDeps) WorkbenchState(ctx context.Context, repo string) (bool, string, error) {
	b, ok, err := d.observeBench(ctx, repo)
	if err != nil || !ok {
		return false, "", err
	}
	return b.AheadOfDefault(ctx)
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

// PriorImplementAt reports whether an earlier execution of this run already ran the implement stage
// at repo. It is the run-scoped counterpart to the repo-global "workbench ahead": mercury-dev is
// shared, so it cannot say whose commits it carries — the execution archive can.
//
// executed AND failed both count: the agent commits its own work, so an implement that ended in a
// failure can still have left commits on the workbench. not-executed and not-applicable never ran.
func (d *ChainDeps) PriorImplementAt(runID, repo string) (bool, error) {
	if d.s.results == nil {
		return false, errors.New("the execution archive is not available")
	}
	prior, err := d.s.results.ForRun(runID)
	if err != nil {
		return false, err
	}
	for _, res := range prior {
		// stage-vocabulary: an archived pre-rebuild document carries the RETIRED stage names
		// verbatim, and its execution never wrote into today's delivery ledger. It can therefore
		// neither be compared here nor attest that commits on today's workbench are this run's —
		// it is skipped by its PROVENANCE, not by a name comparison, so nothing is guessed. The
		// skip errs towards "not implemented", which makes the agent run; the opposite error is
		// the silent skip this whole rule exists against.
		if res.Legacy {
			continue
		}
		for _, rp := range res.Repos {
			if !sameRepo(rp.Repo, repo) {
				continue
			}
			for _, st := range rp.Stages {
				if st.Stage == model.StageImplement && (st.State == model.StepExecuted || st.State == model.StepFailed) {
					return true, nil
				}
			}
		}
	}
	return false, nil
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
	ex := workspace.Executor{User: d.user, PerUser: true}
	b, err := workbench.New(&ex, wt)
	if err != nil {
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
func (o benchOps) Prepare(ctx context.Context) (executor.PrepareInfo, error) {
	b, _, err := o.d.bench(ctx, o.repo)
	if err != nil {
		return executor.PrepareInfo{}, err
	}
	res, err := b.Prepare(ctx)
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

func (c chainDeliver) NextPRBase(ctx context.Context, repo string) (string, error) {
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
	def, err := github.DefaultBranch(ctx, c.d.token, full)
	if err != nil {
		return "", err
	}
	return deliver.NextPRBase(open, def), nil
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
	ex := workspace.Executor{User: c.d.user, PerUser: true}
	artifact, err := deploy.Build(ctx, ex, wt)
	if err != nil {
		return executor.DeployOutcome{}, err
	}
	self := repoShort(repo) == selfRepo()
	if self {
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
	}, err
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
