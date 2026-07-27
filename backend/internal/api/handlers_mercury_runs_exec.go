package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/discover"
	"devlab/backend/internal/github"
	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workspace"
)

// Phase 2 — the autonomous executor. On schedule (or "Jetzt ausführen") a run's stored prompt (axioms
// + Laufregeln, composed earlier in a cookie-bearing request) drives Claude across every Holistic
// repo. This code is INERT until provisioned: StartScheduler only arms it when DEVLAB_RUNS_MODE and
// DEVLAB_RUNS_USER are set, and never under dev-bypass. The agent NEVER gets host privileges — the one
// privileged step, deploy, is a separate root wrapper (devlab-deploy) restricted to a per-repo
// allowlist. The runner must be a DEDICATED UNPRIVILEGED user (never one with passwordless sudo).
//
// Safety ladder via DEVLAB_RUNS_MODE:
//   off    — nothing runs (default)
//   report — clone + Claude in --permission-mode plan (read-only), store the plan; no branch/push/deploy
//   pr     — implement (bypassPermissions) → commit → push a run branch → open a PR (human merges)
//   full   — pr + deploy the committed workspace via the devlab-deploy allowlist wrapper

const (
	deployWrapper = "/usr/local/sbin/devlab-deploy"
	// deployScriptDir mirrors the wrapper's vetted per-repo allowlist. A repo WITHOUT a script there has
	// no deploy target at all — see hasDeployTarget.
	deployScriptDir     = "/etc/devlab/deploy.d"
	defaultAgentTimeout = 3 * time.Hour // default per-repo time budget; a from-scratch build needs the room
	runnerPreamble      = "You are the autonomous Holistic runner, executing unattended on the server. Work " +
		"strictly against the axioms and Laufregeln in this prompt. There is no human to ask — for " +
		"unresolved operational gaps follow the Laufregeln (log a non-blocking skip, do not stop). Make " +
		"focused, correct, well-tested changes and summarise precisely what you did."

	defaultLimitBackoff = 15 * time.Minute // fallback wait when the CLI doesn't tell us when the window resets
	defaultMaxResumes   = 24               // give up after this many usage-limit suspensions on one execution

	// strandedWindow bounds resume of an unfinished, unsuspended result (a crash mid-sweep, or a
	// deliberate cost/duration carry-over). It MUST exceed the longest schedule period, because the
	// scheduler advances NextFireAt before executing, so the next automatic fire that resumes the husk
	// is ~24h (daily) or ~7d (weekly) later — a shorter window would reject it and refire from scratch,
	// duplicating every PR. 10 days covers weekly with margin while still abandoning truly ancient husks.
	strandedWindow = 10 * 24 * time.Hour
	// defaultMaxRunDuration caps an entire sweep's wall-clock. Belt-and-braces beside the per-repo
	// runAgentTimeout: even 19 repos each just under 60m would otherwise run ~19h. 0 via env = off.
	defaultMaxRunDuration = 4 * time.Hour
)

// runAgentTimeout is the SERVICE-WIDE DEFAULT time budget for one repo's agent pass — the value a
// run/todo that made no explicit choice follows. Three hours covers an ordinary change and a from-scratch
// build both; a too-tight cap kills the pass mid-implement, leaving an empty repo, no PR and no usable
// output. It is the default an un-chosen run resolves to at RUN TIME (never copied into the run — see
// budgetFor), so moving it here moves every un-chosen run at once. Configurable via DEVLAB_RUNS_AGENT_TIMEOUT
// (a Go duration; an explicit "0" removes the cap, leaving only the whole-sweep duration as the bound).
// The default belongs at the central config surface per the config axiom; until one exists it is a
// DEVLAB_RUNS_* env knob like its siblings.
func runAgentTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_AGENT_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return defaultAgentTimeout
}

// errBudgetExceeded is the cause a per-repo budget context carries when its deadline fires, so a killed
// agent pass is told apart from the whole-sweep duration cap (context.DeadlineExceeded on the parent) and
// a deliberate abort (runs.ErrRunAborted) — and reported honestly as an overrun instead of a raw error.
var errBudgetExceeded = errors.New("time budget exceeded")

// budgetFor is the time-budget SIBLING of tuningFor: it resolves a run's (possibly empty) TimeBudget into
// the concrete per-repo agent cap. Empty FOLLOWS the live service default (runAgentTimeout), so an
// un-chosen run tracks the default even after it changes — the referenced-not-copied rule. An explicit
// no-cap ("off"/"none"/0) removes the cap (0 = only the whole-sweep duration bounds the run — a valid,
// deliberate choice). A positive duration caps at that value. Anything else (a hand-edited runs.json, a
// writer that skipped validateTuning) falls back to the default rather than running unbounded — defense in
// depth at the execution boundary, mirroring agentTuning.resolve.
func budgetFor(run runs.Run) time.Duration {
	v := strings.TrimSpace(run.TimeBudget)
	if v == "" {
		return runAgentTimeout()
	}
	if isNoBudget(v) {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	return runAgentTimeout()
}

// isNoBudget recognizes the explicit "no time budget" choice: the word sentinels and any zero duration.
func isNoBudget(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "none", "0":
		return true
	}
	if d, err := time.ParseDuration(v); err == nil && d == 0 {
		return true
	}
	return false
}

// humanizeDuration renders a resolved budget for display and for the honest overrun message: a positive
// duration compactly ("3h", "1h30m", "45m") and the explicit no-cap as "off". It is the single formatter
// behind Result.TimeBudget and budgetAwareError, so the stamped label and the overrun text never diverge.
func humanizeDuration(d time.Duration) string {
	if d <= 0 {
		return "off"
	}
	d = d.Round(time.Minute)
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// budgetAwareError renders the honest reason an agent pass ended. When actx was cancelled because THIS
// repo's time budget elapsed (cause errBudgetExceeded — told apart from the whole-sweep duration cap and a
// deliberate abort, whose causes differ), it names the overrun with the budget that applied — "Zeitbudget
// überschritten (3h)" — rather than the raw kill error; otherwise the underlying error stands. This is the
// REPO-level reason (rr.Error); the partial transcript — "what was reached until then" — is preserved
// separately on the step itself (runAgentLive routes a budget overrun to agentStep.failBudget), so the two
// are shown side by side: the honest banner and the partial work.
func budgetAwareError(actx context.Context, budget time.Duration, err error) string {
	if errors.Is(context.Cause(actx), errBudgetExceeded) {
		return "Zeitbudget überschritten (" + humanizeDuration(budget) + ")"
	}
	return err.Error()
}

// maxRunDuration caps the wall-clock of one whole sweep. DEVLAB_RUNS_MAX_DURATION overrides;
// an explicit "0" disables the cap.
func maxRunDuration() time.Duration {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_MAX_DURATION")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return defaultMaxRunDuration
}

// limitBackoff / maxResumes read the usage-limit resume knobs from the environment.
func limitBackoff() time.Duration {
	if d := os.Getenv("DEVLAB_RUNS_LIMIT_BACKOFF"); d != "" {
		if pd, err := time.ParseDuration(d); err == nil && pd > 0 {
			return pd
		}
	}
	return defaultLimitBackoff
}

func maxResumes() int {
	if v := os.Getenv("DEVLAB_RUNS_LIMIT_MAXRESUMES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return defaultMaxResumes
}

func resumeEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("DEVLAB_RUNS_LIMIT_RESUME")), "off")
}

// maxConcurrentRuns is the seed for the concurrency cap (how many runs execute at once). DEVLAB_RUNS_MAX_
// CONCURRENT overrides; a non-positive/absent value leaves the scheduler's conservative default. This is
// only the STARTING value — the cap is configurable live at runtime (see the settings store), env just
// seeds it.
func maxConcurrentRuns() int {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_MAX_CONCURRENT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return 0 // 0 → NewScheduler applies its default
}

// StartScheduler arms the run scheduler iff configured. Absent config → it logs and stays dormant
// (the management layer keeps working). Wired from main() with a cancelable context.
func (s *Server) StartScheduler(ctx context.Context) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("DEVLAB_RUNS_MODE")))
	user := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_USER"))
	if mode == "" || mode == "off" || user == "" {
		log.Printf("devlabd: runs scheduler OFF (DEVLAB_RUNS_MODE=%q, DEVLAB_RUNS_USER=%q)", mode, user)
		return
	}
	if mode != "report" && mode != "pr" && mode != "full" {
		log.Printf("devlabd: runs scheduler OFF — invalid DEVLAB_RUNS_MODE %q (want report|pr|full)", mode)
		return
	}
	if s.v.DevBypass() {
		log.Printf("devlabd: runs scheduler OFF under dev-bypass")
		return
	}
	if s.runs == nil || s.runResults == nil || s.runPRs == nil || s.deliveries == nil || s.links == nil {
		log.Printf("devlabd: runs scheduler OFF — stores unavailable")
		return
	}
	autoMerge := 30 * 24 * time.Hour
	if d := os.Getenv("DEVLAB_RUNS_AUTOMERGE"); d != "" {
		// Guard > 0: a stray "0" (or negative) would set MergeBy ≈ now, so Maintain would auto-merge
		// EVERY open run PR on the very next tick — unreviewed AI code onto default branches instantly.
		if pd, err := time.ParseDuration(d); err == nil && pd > 0 {
			autoMerge = pd
		} else {
			log.Printf("devlabd: ignoring DEVLAB_RUNS_AUTOMERGE=%q (must be a positive duration), keeping %s", d, autoMerge)
		}
	}
	tick := 30 * time.Second
	if d := os.Getenv("DEVLAB_RUNS_TICK"); d != "" {
		if pd, err := time.ParseDuration(d); err == nil {
			tick = pd
		}
	}
	// The OS identity that executes and the GitHub identity that pushes are separate concerns: the
	// runner should be a powerless Linux account while the token belongs to a real linked account.
	// runnerTokenUser resolves DEVLAB_RUNS_TOKEN_USER, falling back to DEVLAB_RUNS_USER — shared with
	// the background rollout so both agree on which account pushes.
	tokenUser := runnerTokenUser()
	x := &runExecutor{s: s, mode: mode, user: user, tokenUser: tokenUser, autoMergeAfter: autoMerge}
	s.runExec = x // also drives rollback/reset (POST /deliveries/{id}/rollback, /repos/reset)
	// Seed the concurrency cap: a configured setting wins (req 13 — set in the UI, survives a restart);
	// otherwise the env value seeds it; otherwise the scheduler's conservative default.
	maxConc := maxConcurrentRuns()
	if s.runSettings != nil {
		if rs, err := s.runSettings.Get(); err == nil && rs.MaxConcurrent > 0 {
			maxConc = rs.MaxConcurrent
		}
	}
	s.scheduler = runs.NewScheduler(s.runs, x, tick, maxConc)
	log.Printf("devlabd: runs scheduler ENABLED — mode=%s user=%s tokenUser=%s automerge=%s tick=%s maxConcurrent=%d",
		mode, user, tokenUser, autoMerge, tick, maxConc)
	go s.scheduler.Run(ctx)
}

// runExecutor implements runs.Executor: the per-repo pipeline + auto-merge maintenance.
type runExecutor struct {
	s    *Server
	mode string // report | pr | full
	// user is the OS identity: whose Linux account runs git and the Claude CLI (via devlab-exec),
	// and whose workspace root holds the clones.
	user string
	// tokenUser is the link-store key: whose GitHub token authorizes clone/push/merge and whose
	// account the runner commits as. DELIBERATELY SEPARATE from user — the runner should be a
	// powerless OS account (devlab-runs) that has no business owning a GitHub identity, while the
	// token belongs to a real linked account (the owner). DEVLAB_RUNS_TOKEN_USER sets it; it
	// defaults to user.
	//
	// WARNING: user is the WORKSPACE owner, and executeRepo runs CleanWorktree (git reset --hard HEAD +
	// clean -fdx) on /var/lib/devlab/workspaces/<user>/<repo> before every run. The DevLab IDE keys
	// its OWN workspace by the logged-in username under the same path. So DEVLAB_RUNS_USER must be a
	// DEDICATED account (devlab-runs) that no human ever uses interactively — otherwise a nightly run
	// silently wipes that human's uncommitted edits. Point DEVLAB_RUNS_TOKEN_USER at the owner for the
	// token; never point DEVLAB_RUNS_USER at a human account.
	tokenUser      string
	autoMergeAfter time.Duration

	// IO seams — nil in production (the real GitHub client and deploy pipeline are used). Kept as
	// fields so the Maintain orchestration (throttled merge-detection → prod-deploy → untrack) can be
	// exercised in tests without a real GitHub token, real network, or a real root deploy wrapper.
	tokenFn      func(user string) (string, error)
	getPRFn      func(ctx context.Context, token, fullName string, number int) (github.PullRequest, error)
	mergePRFn    func(ctx context.Context, token, fullName string, number int) error
	prodDeployFn func(ctx context.Context, token string, p runs.PendingPR) (string, error)
	// deployTargetFn stubs the deploy allowlist lookup, whose real answer depends on root-owned files in
	// /etc that a test can neither create nor rely on.
	deployTargetFn func(repoName string) bool
	// deleteBranchFn stubs the GitHub ref-delete so the merged-delivery-branch prune can be exercised
	// without a real token or network.
	deleteBranchFn func(ctx context.Context, token, fullName, branch string) error

	// Rollback/reset seams — the git counter-booking runs against a real per-user workspace (sudo), and
	// the PR ops hit GitHub; both are stubbed in tests so the rollback DECISION logic (conflict → ToDo,
	// merged → reversing PR, open → close PR) is exercised deterministically.
	counterBookFn func(ctx context.Context, token string, d runs.Delivery, reversalBranch string) (counterBookResult, error)
	createPRFn    func(ctx context.Context, token, fullName, head, base, title, body string) (github.PullRequest, error)
	closePRFn     func(ctx context.Context, token, fullName string, number int) error
	commentPRFn   func(ctx context.Context, token, fullName string, number int, body string) error
	resetRepoFn   func(ctx context.Context, token string, repo model.Repo) (string, error)
}

// token / fetchPR / mergePR / runProdDeploy dispatch to the injected seam when present, else the real
// implementation. This keeps production wiring untouched (StartScheduler sets no seams) while letting
// tests substitute deterministic fakes.
func (x *runExecutor) token() (string, error) {
	if x.tokenFn != nil {
		return x.tokenFn(x.tokenUser)
	}
	return x.s.links.Token(x.tokenUser)
}

func (x *runExecutor) fetchPR(ctx context.Context, token, fullName string, number int) (github.PullRequest, error) {
	if x.getPRFn != nil {
		return x.getPRFn(ctx, token, fullName, number)
	}
	return github.GetPullRequest(ctx, token, fullName, number)
}

func (x *runExecutor) mergePR(ctx context.Context, token, fullName string, number int) error {
	if x.mergePRFn != nil {
		return x.mergePRFn(ctx, token, fullName, number)
	}
	return github.MergePullRequest(ctx, token, fullName, number)
}

func (x *runExecutor) runProdDeploy(ctx context.Context, token string, p runs.PendingPR) (string, error) {
	if x.prodDeployFn != nil {
		return x.prodDeployFn(ctx, token, p)
	}
	return x.prodDeployMerged(ctx, token, p)
}

func (x *runExecutor) deployable(repoName string) bool {
	if x.deployTargetFn != nil {
		return x.deployTargetFn(repoName)
	}
	return hasDeployTarget(repoName)
}

// markDelivery mirrors a PR outcome onto the delivery ledger (merged/closed), so LatestOpen — the
// stacked-PR base — never points at an already-merged predecessor. A no-op when the ledger is absent
// (older setups / tests) or the PR predates it.
func (x *runExecutor) markDelivery(repo string, number int, status runs.DeliveryStatus) {
	if x.s == nil || x.s.deliveries == nil {
		return
	}
	_, _, _ = x.s.deliveries.SetStatusByPR(repo, number, status)
}

func (x *runExecutor) Execute(ctx context.Context, run runs.Run, report func(resultID string)) (runs.ResultRef, error) {
	// Resume an execution suspended on the usage limit (same ResultID, skip the repos already done), or
	// start a fresh one. A resume that can't find its open result silently starts fresh.
	res, resuming := x.resumeOrNew(run)
	res.Suspended, res.ResumeAt = false, nil // recomputed below if we hit the limit again
	res.Live = nil                           // drop any stale in-flight repo left by a crashed prior attempt
	saver := &liveSaver{do: func() { _ = x.s.runResults.Save(res) }}
	save := saver.force
	// Publish the live result id (so /active can point the UI at this execution) and materialize the
	// result file up front — so a run is followable, and survives a page reload, from its first moment.
	report(res.ResultID)
	save()

	fail := func(msg string) (runs.ResultRef, error) {
		res.FinishedAt = time.Now().UTC()
		res.Repos = append(res.Repos, runs.RepoResult{Repo: "-", OK: false, Error: msg})
		save()
		ref := res.Ref()
		ref.OK = false
		return ref, fmt.Errorf("%s", msg)
	}
	// carryOver stops WITHOUT finalising (FinishedAt stays zero) so the next fire resumes this same
	// result and skips the done repos — for transient infrastructure failures (no network) that must not
	// be recorded as a permanent, "run complete" failure. It is the infra sibling of fail().
	carryOver := func(reason string) (runs.ResultRef, error) {
		res.OK = false
		save()
		log.Printf("devlabd: run %s carried over before completing (%s) — next fire resumes it", run.ID, reason)
		return res.Ref(), nil
	}

	token, err := x.s.links.Token(x.tokenUser)
	if err != nil {
		return fail("Runner-Token nicht verfügbar (DEVLAB_RUNS_TOKEN_USER — ersatzweise DEVLAB_RUNS_USER — " +
			"muss ein verknüpftes GitHub-Konto sein): " + err.Error())
	}

	// Self-healing preflight: if GitHub is unreachable, WAIT for connectivity to return before touching
	// any repo (a short DNS/link blip must not turn into a whole failed night). Only after it stays down
	// past the wait budget do we carry the sweep over to the next fire — never finalise it as failed.
	if err := ensureGitHubReachable(ctx, token); err != nil {
		if isInfraError(err) || ctx.Err() != nil {
			return carryOver("GitHub nicht erreichbar (Netz/DNS): " + err.Error())
		}
		return fail("GitHub-Vorabprüfung fehlgeschlagen: " + err.Error())
	}

	// An auto run sweeps every Holistic repo; a ToDo hits its one-or-more targets — each an existing repo
	// or one it creates first. Both feed the very same per-repo pipeline below.
	var repos []model.Repo
	newRepoName := map[string]string{} // repo id → its to-be-created name, so the prompt says "scaffold it"
	var resolveFails []runs.RepoResult // targets that could not be resolved (unknown repo, create failed)
	if run.IsTodo() {
		repos, newRepoName, resolveFails = x.resolveTodoTargets(ctx, run, token)
	} else {
		repos, err = discover.ReposForUser(ctx, x.tokenUser, token)
		if err != nil {
			if isInfraError(err) {
				return carryOver("Repo-Discovery (Netz/DNS): " + err.Error())
			}
			return fail("Holistic-Repos konnten nicht ermittelt werden: " + err.Error())
		}
	}
	ghLogin, ghID := x.runnerIdentity()

	// A ToDo can carry media (images, documents) the agent must take into account. Read them from the
	// passive pool once here; each target's workspace gets them materialized (and cleaned) around its
	// agent call, and the prompt announces them.
	var todoAtts []loadedAttachment
	if run.IsTodo() {
		todoAtts = x.loadAttachments(run)
	}

	// Cap the whole sweep's wall-clock (belt-and-braces beside the per-repo timeout). A resume inherits a
	// fresh duration budget. There is deliberately NO spend ceiling: the only limits that bind are the
	// subscription usage limit (which pauses the run and resumes it when the window resets) and this
	// duration bound. A dollar cap measured a cost that does not exist — the flat-rate subscription bills
	// nothing per run — so it only left paid quota unused; spend is still MEASURED (res.CostUSD below),
	// just never used to abort.
	if d := maxRunDuration(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	done := res.DoneRepos()          // repos already handled in a previous attempt (resume skips them)
	overallOK := res.OK || !resuming // seed from the partial result's running state
	if resuming {
		overallOK = true
		for _, rr := range res.Repos {
			if !rr.OK {
				overallOK = false
			}
		}
	}

	// A ToDo target that could not be resolved (an unknown existing repo, or a new one that failed to
	// create) is recorded as a failed repo result but does NOT sink the whole run — the targets that DID
	// resolve still run. Skip any already recorded in a prior attempt so a resume never double-counts.
	for _, rf := range resolveFails {
		if done[rf.Repo] {
			continue
		}
		res.Repos = append(res.Repos, rf)
		done[rf.Repo] = true
		overallOK = false
		save()
	}

	carriedOver := false // true = stop early but DON'T finalise; the next scheduled fire continues this
	//                      same result (via the stranded-resume path), skipping the repos already done.

	// Per-repo coordination (req 14): an auto run occupies ONE slot and claims each repo through the
	// scheduler just before working it, releasing it after — so a repo momentarily busy (held by another
	// run) is put to the BACK of the worklist and retried, not skipped, and ToDos in other repos run
	// alongside. A ToDo already holds its target claims (reserved at admission), so it does not re-claim.
	perRepoClaim := !run.IsTodo() && x.s != nil && x.s.scheduler != nil
	worklist := make([]model.Repo, 0, len(repos))
	for _, repo := range repos {
		if !done[repo.ID] { // skip repos completed in an earlier attempt of this same execution
			worklist = append(worklist, repo)
		}
	}
	busyStreak := 0 // consecutive repos found busy this cycle → all remaining busy ⇒ wait, don't spin

	for len(worklist) > 0 {
		repo := worklist[0]
		worklist = worklist[1:]

		if ctx.Err() != nil {
			// A DEFER (freeing a slot) re-arms the run as an immediately-due deferred suspension — it keeps
			// its progress and resumes at the next free slot. Checked first: it is a stand-down, not a stop.
			if deferredNow(ctx) {
				return x.suspendDeferred(&res, save)
			}
			// Distinguish WHY the context ended. Only a DELIBERATE kill-switch abort (Cancel attaches
			// ErrRunAborted as the cause) finalises the run — "abort" means stop, not resume. Our own
			// sweep-duration cap (DeadlineExceeded) and a process shutdown (a plain cancel with no abort
			// cause) instead CARRY OVER: leave the result unfinished so the next fire resumes it and
			// skips the done repos, rather than refiring from scratch and duplicating every PR.
			if errors.Is(context.Cause(ctx), runs.ErrRunAborted) {
				res.Repos = append(res.Repos, runs.RepoResult{Repo: repo.ID, OK: false, Error: "abgebrochen"})
				overallOK = false
				continue
			}
			log.Printf("devlabd: run %s interrupted (%v) after %d repos — carrying the rest to the next run", run.ID, ctx.Err(), len(res.Repos))
			carriedOver = true
			break
		}

		var releaseRepo func()
		if perRepoClaim {
			key := runs.RepoClaimKey(repo.ID)
			if !x.s.scheduler.TryClaimRepo(run.ID, key) {
				worklist = append(worklist, repo) // busy — put it at the back and come back (req 14)
				busyStreak++
				if busyStreak >= len(worklist) {
					// A whole cycle found every remaining repo busy — wait briefly (bounded by the sweep
					// duration cap via ctx) rather than spinning, then retry.
					busyStreak = 0
					select {
					case <-ctx.Done():
					case <-time.After(2 * time.Second):
					}
				}
				continue
			}
			busyStreak = 0
			releaseRepo = func() { x.s.scheduler.ReleaseRepo(run.ID, key) }
		}

		rr, lim := x.executeRepo(ctx, run, repo, newRepoName[repo.ID] != "", x.promptFor(run, repo.ID, newRepoName[repo.ID], todoAtts), token, ghLogin, ghID, todoAtts, &res, saver)
		if releaseRepo != nil {
			releaseRepo() // free the repo the instant this one settles, so another run can take it
		}
		res.Live = nil // this repo has settled (about to be recorded, carried over, or retried on a limit)
		if lim.limited && resumeEnabled() {
			// The subscription window is exhausted. Do NOT record this repo (it retries on resume) and
			// do NOT hammer the rest — suspend the whole execution until the window resets.
			return x.suspend(run, &res, lim, save)
		}
		if lim.infra {
			// Infrastructure failure (DNS/network) — NOT this repo's fault, and a sign the rest will fail
			// too. Do NOT record the repo (it retries) and stop the sweep: carry over so the next fire
			// resumes from here once connectivity is back, rather than burning the night on clone failures
			// and finalising the run as "done, all failed". This is the self-healing deferral.
			log.Printf("devlabd: run %s hit an infrastructure failure on %s (%s) — carrying the rest to the next run",
				run.ID, repo.ID, lim.infraErr)
			carriedOver = true
			break
		}
		// If the context ended DURING this repo (duration cap or shutdown, not a deliberate abort) before
		// it produced any durable result, do NOT record the half-done repo — carry over so it retries
		// cleanly on resume instead of being skipped as a permanent failure. Drop ONLY a repo that
		// neither succeeded nor left a PR (`!rr.OK && rr.PRUrl == ""`):
		//   • a successful repo (rr.OK — report-mode analyze with no PR, or pr/full with a PR) must be
		//     recorded even if the deadline fires an instant later, else resume re-does it (a report
		//     wastefully, a pr/full repo into a DUPLICATE PR);
		//   • a failed repo that already opened a PR (full-mode deploy-fail leaves rr.PRUrl set) must be
		//     recorded so resume skips it — dropping it would re-open a duplicate PR.
		if deferredNow(ctx) {
			// Deferred mid-repo: record the repo only if it durably completed (opened a PR / succeeded), so
			// resume skips it; otherwise drop it (retries clean on resume). Then re-arm as deferred.
			if rr.OK || rr.PRUrl != "" {
				rr.Running = false
				res.Repos = append(res.Repos, rr)
				res.InputTokens += rr.InputTokens
				res.OutputTokens += rr.OutputTokens
				res.CostUSD += rr.CostUSD
				res.NumTurns += rr.NumTurns
			}
			return x.suspendDeferred(&res, save)
		}
		if !rr.OK && rr.PRUrl == "" && ctx.Err() != nil && !errors.Is(context.Cause(ctx), runs.ErrRunAborted) {
			log.Printf("devlabd: run %s interrupted mid-repo %s (%v) — carrying over", run.ID, repo.ID, ctx.Err())
			carriedOver = true
			break
		}
		rr.Running = false // a recorded repo is complete, never in-flight
		// Note the stand this repo was examined against, per axiom of the run — the next run then only
		// has to look at the commits after it (never examined ⇒ full repository).
		if rr.OK && !run.IsTodo() && rr.Base != "" {
			x.s.axiomChecks.Record(repo.ID, run.AxiomIDs, rr.Base, time.Now())
		}
		res.Repos = append(res.Repos, rr)
		res.InputTokens += rr.InputTokens
		res.OutputTokens += rr.OutputTokens
		res.CostUSD += rr.CostUSD
		res.NumTurns += rr.NumTurns
		if !rr.OK {
			overallOK = false
		}
		save() // persist after every repo so a resume (or a crash) never loses completed work
	}

	// Carry-over: we broke out early (cost cap, duration cap, or shutdown) with not-done repos still
	// remaining. Leave the result UNFINISHED (FinishedAt stays zero) so the next scheduled fire resumes
	// it via the stranded-resume path and skips the done repos, instead of refiring from scratch and
	// duplicating PRs. No tight loop: NextFireAt is (re-)anchored to the next schedule slot (~24h/~7d
	// out), and each carry-over Save refreshes the result's UpdatedAt so the stranded window never ages
	// out a run that is still being worked. Progress isn't required — a zero-progress carry-over (e.g.
	// shutdown before the first repo) simply resumes from the same point, still without duplicating.
	if carriedOver {
		res.OK = overallOK
		save()
		return res.Ref(), nil
	}

	res.FinishedAt = time.Now().UTC()
	res.OK = overallOK
	save()
	return res.Ref(), nil
}

// freshResult mints a brand-new execution stamped with the run's current identity, mode and tuning —
// snapshotted so the History stays legible even after the run (or its axioms) later change.
func (x *runExecutor) freshResult(run runs.Run) runs.Result {
	start := time.Now()
	model, _, _ := tuningFor(run).resolve()
	return runs.Result{RunID: run.ID, ResultID: runs.NewResultID(start), RunName: run.Name,
		Type: runs.NormalizeType(run.Type), Mode: x.mode, Model: model, Effort: run.Effort,
		TimeBudget: humanizeDuration(budgetFor(run)), StartedAt: start.UTC(),
		PromptHash: run.PromptHash, Prompt: run.Prompt}
}

// classifyResume is the SINGLE, read-only decision of whether the next fire of `run` continues an
// interrupted execution or starts fresh — and why. It performs NO side effects (it neither reaps nor
// mints), so the exact same logic can PREVIEW the decision for whoever triggers the run (PlanResume) and
// ENACT it in the execution path (resumeOrNew) without a second, divergable code path — one source of
// truth for "fortgesetzt oder neu begonnen?". Three interruption kinds are continued (skipping the repos
// already done, so nothing is re-implemented into a duplicate PR): a usage-limit suspension (run.Suspended
// points at the open result), a crash/restart mid-run (a stranded, unsuspended husk on disk), and an
// ORPHANED suspension whose run lost its pointer (FindResumable recovers it — the freeze-bug fix).
//
// The standing conditions are applied reliably, not weakened: a husk from a DIFFERENT safety-ladder mode
// is NOT continued (a report husk marks repos "done" after mere analysis; resuming it in pr mode would
// skip implementing them), and a husk not worked within the stranded window has aged out and is not found
// at all. candidate is the husk the plan refers to — the one to resume, or the mode-mismatched one the
// caller must reap before starting fresh; found reports whether any husk was considered.
func (x *runExecutor) classifyResume(run runs.Run) (plan runs.ResumePlan, candidate runs.Result, found bool) {
	decide := func(existing runs.Result) (runs.ResumePlan, runs.Result, bool) {
		if existing.Mode != "" && existing.Mode != x.mode {
			return runs.ResumePlan{Action: runs.ResumeFresh,
				Reason: fmt.Sprintf("the interrupted execution ran in %s mode; starting fresh in %s mode", existing.Mode, x.mode),
			}, existing, true
		}
		return runs.ResumePlan{Action: runs.ResumeContinue, ResultID: existing.ResultID, ReposDone: len(existing.Repos),
			Reason: fmt.Sprintf("continuing the interrupted execution — %s already done", reposPhrase(len(existing.Repos))),
		}, existing, true
	}
	// A live usage-limit suspension names its open result directly; prefer it, but only while it is still
	// unfinished (a finished result is never resumed — a defensive guard so the pointer can't resurrect one).
	if run.Suspended != nil && run.Suspended.ResultID != "" {
		if existing, ok, _ := x.s.runResults.Get(run.ID, run.Suspended.ResultID); ok && existing.FinishedAt.IsZero() {
			return decide(existing)
		}
	}
	// Otherwise a crash/carry-over husk (or an orphaned suspension) on disk, bounded by the stranded window.
	if existing, ok := x.s.runResults.FindResumable(run.ID, time.Now().Add(-strandedWindow)); ok {
		return decide(existing)
	}
	return runs.ResumePlan{Action: runs.ResumeFresh, Reason: "no interrupted execution to continue — starting a new one"}, runs.Result{}, false
}

// resumeOrNew enacts classifyResume's decision: continue the interrupted execution (same id, done repos
// skipped) or mint a fresh one. A husk from a different mode is reaped — finalised as failed so it stops
// lingering as unfinished — before the fresh start.
func (x *runExecutor) resumeOrNew(run runs.Run) (runs.Result, bool) {
	plan, candidate, found := x.classifyResume(run)
	if plan.Action == runs.ResumeContinue {
		// A resumed attempt is driven by the run's CURRENT tuning (executeRepo reads tuningFor(run) each
		// fire), so re-stamp the engine label to match — else a legacy husk shows no model, and one whose
		// tuning was edited while suspended keeps a stale label. Label-only; it steers no resume decision.
		m, _, _ := tuningFor(run).resolve()
		candidate.Model, candidate.Effort = m, run.Effort
		candidate.TimeBudget = humanizeDuration(budgetFor(run))
		return candidate, true
	}
	if found {
		x.reap(candidate, plan.Reason) // mode-mismatched husk — close it out so it stops showing as unfinished
	}
	return x.freshResult(run), false
}

// PlanResume answers "fortgesetzt oder neu begonnen?" synchronously for whoever triggers a run, and
// enacts an EXPLICIT fresh start. With fresh=false it is the read-only classifier — the executor
// re-derives the same decision, deterministically, when it actually runs. With fresh=true it DISCARDS any
// resumable execution: the husk is reaped (visibly marked as discarded, not left lingering), so the run
// starts new instead of continuing it, and the returned plan names the discarded execution and any open
// pull request it left — surfacing it rather than silently placing a second one beside it.
func (x *runExecutor) PlanResume(run runs.Run, fresh bool) runs.ResumePlan {
	plan, candidate, _ := x.classifyResume(run)
	if !fresh || plan.Action != runs.ResumeContinue {
		return plan // nothing to discard: report the classifier's decision as-is
	}
	x.reap(candidate, "discarded on an explicit fresh start")
	out := runs.ResumePlan{Action: runs.ResumeFresh, Discarded: candidate.ResultID,
		Reason: fmt.Sprintf("discarded the interrupted execution (%s done) and started fresh, as requested", reposPhrase(len(candidate.Repos)))}
	if pr := candidate.Ref().PRUrl; pr != "" {
		out.DiscardedPRUrl = pr
		out.Reason += "; it left an open pull request: " + pr
	}
	return out
}

// reposPhrase renders a repo count with correct singular/plural, for the human-readable resume reason.
func reposPhrase(n int) string {
	if n == 1 {
		return "1 repository"
	}
	return fmt.Sprintf("%d repositories", n)
}

// reap finalises an unresumable husk (mode-mismatched, or otherwise not to be continued) as a failed,
// FINISHED result so it stops showing as "suspended"/unfinished forever and cannot be picked up again.
// The work it already did remains in its stored report; only its open state is closed out.
func (x *runExecutor) reap(res runs.Result, reason string) {
	res.Suspended = false
	res.ResumeAt = nil
	res.FinishedAt = time.Now().UTC()
	res.OK = false
	res.Repos = append(res.Repos, runs.RepoResult{Repo: "-", OK: false, Error: "closed (not continued): " + reason})
	_ = x.s.runResults.Save(res)
	log.Printf("devlabd: reaped husk %s/%s — %s", res.RunID, res.ResultID, reason)
}

// suspend persists the partial execution as paused and returns a suspended ResultRef — UNLESS the resume
// budget is exhausted, in which case it finalizes the execution as failed and returns a normal ref (so
// the scheduler clears the suspension and stops retrying).
// deferredNow reports whether the context was cancelled by a Defer (freeing a slot) rather than an abort
// or a shutdown.
func deferredNow(ctx context.Context) bool {
	return ctx.Err() != nil && errors.Is(context.Cause(ctx), runs.ErrRunDeferred)
}

// suspendDeferred re-arms the execution as an IMMEDIATELY-DUE deferred suspension (Reason=deferred,
// ResumeAt=now): it keeps all progress and reclaims the next free slot, resuming the SAME execution and
// skipping the repos already done. It reuses the ONE suspension mechanism — no second pause concept — and
// never consumes a usage-limit attempt (the scheduler only counts attempts for a real usage-limit pause).
func (x *runExecutor) suspendDeferred(res *runs.Result, save func()) (runs.ResultRef, error) {
	now := time.Now().UTC()
	res.Suspended = true
	res.ResumeAt = &now
	save()
	ref := res.Ref() // copies Suspended + ResumeAt from res
	ref.Reason = runs.ReasonDeferred
	ref.OK = false
	log.Printf("devlabd: run %s deferred to free a slot — resumes at the next free slot", res.RunID)
	return ref, nil
}

func (x *runExecutor) suspend(run runs.Run, res *runs.Result, lim repoSignal, save func()) (runs.ResultRef, error) {
	attempts := 0
	if run.Suspended != nil {
		attempts = run.Suspended.Attempts
	}
	resumeAt := time.Now().Add(limitBackoff())
	if lim.hasReset && lim.resetAt.After(time.Now()) {
		resumeAt = lim.resetAt.Add(1 * time.Minute) // a small cushion past the reported reset
	}
	if attempts+1 > maxResumes() {
		res.Suspended, res.ResumeAt = false, nil
		res.FinishedAt = time.Now().UTC()
		res.OK = false
		res.Repos = append(res.Repos, runs.RepoResult{Repo: "-", OK: false,
			Error: fmt.Sprintf("Abo-Limit: nach %d automatischen Fortsetzungen aufgegeben", attempts)})
		save()
		ref := res.Ref()
		ref.OK = false
		return ref, nil
	}
	res.Suspended = true
	res.ResumeAt = &resumeAt
	save()
	log.Printf("devlabd: run %s suspended on usage limit — resuming at %s (attempt %d)", run.ID, resumeAt.Format(time.RFC3339), attempts+1)
	ref := res.Ref()
	ref.OK = false
	return ref, nil
}

// repoSignal reports why a repo step stopped in a way that must NOT be recorded as a terminal repo
// result — the repo retries when the execution resumes/carries over, rather than being marked done:
//   - limited: the Claude usage window is exhausted → suspend the whole execution until it resets.
//   - infra:   an infrastructure failure (DNS/network) hit clone or refresh, i.e. NOT the repo's own
//     fault → carry the sweep over to the next fire (the network may be back by then), never burn
//     through 19 clone failures and finalise the run as done.
//
// A zero repoSignal means the repo produced an ordinary result (success or a genuine work failure).
type repoSignal struct {
	limited  bool
	resetAt  time.Time
	hasReset bool
	infra    bool
	infraErr string
}

// adoptPriorDelivery detects duplicate work before it is redone (req 5): if a PRIOR attempt of THIS SAME
// execution already opened a pull request for this repo — recorded in the delivery ledger — but crashed
// before its repo-result was saved (so the resume's done-set missed it and would implement it again), the
// existing pull request is ADOPTED instead of a second being stacked beside it. Keying on the exact
// (runId, resultId) keeps it precise: a legitimate delivery from a different execution (an automatic run's
// next night) has a different resultId and is never mistaken for a duplicate. ok=false is the normal path
// (nothing to adopt). Pure w.r.t. the workspace — it reads only the ledger, so it needs no clone.
func (x *runExecutor) adoptPriorDelivery(run runs.Run, res *runs.Result, repoID, repoFull string) (runs.RepoResult, bool) {
	if x.s == nil || x.s.deliveries == nil {
		return runs.RepoResult{}, false
	}
	prior, ok, _ := x.s.deliveries.OpenForExecution(run.ID, res.ResultID, repoFull)
	if !ok {
		return runs.RepoResult{}, false
	}
	rr := runs.RepoResult{
		Repo:      repoID,
		DevBranch: prior.DevBranch, DevCommit: prior.ToCommit,
		PRUrl: prior.PRUrl, PRBase: prior.BaseBranch, DeliveryID: prior.ID,
		Steps: []runs.Step{{Name: "pr", Status: runs.StepOK, At: time.Now().UTC(),
			Log: clip("an open pull request from this same execution already delivers this repository — adopted instead of opening a second: " + prior.PRUrl)}},
	}
	rr.OK = runs.StepsSucceeded(rr.Steps) // the repo result follows the chain (req 5)
	return rr, true
}

func (x *runExecutor) executeRepo(ctx context.Context, run runs.Run, repo model.Repo, isNewRepo bool, prompt, token, ghLogin string, ghID int64, atts []loadedAttachment, res *runs.Result, saver *liveSaver) (runs.RepoResult, repoSignal) {
	rr := runs.RepoResult{Repo: repo.ID, Running: true}
	res.Live = &rr // publish this repo as the in-flight one; the caller clears Live once it settles
	saver.force()
	// step records a COMPLETED stage and re-saves, so the fast git stages (push/pr/deploy) also surface
	// live as they land. The long agent stages instead use runAgentLive (a running step that streams).
	step := func(name, logtxt string, status runs.StepStatus) {
		rr.Steps = append(rr.Steps, runs.Step{Name: name, Status: status, Log: clip(logtxt), At: time.Now().UTC()})
		saver.force()
	}

	// Duplicate-work guard (req 5), BEFORE the expensive clone/implement: a prior attempt of THIS SAME
	// execution may have already opened a pull request for this repo and then crashed before its repo-result
	// was recorded — so the resume's done-set missed it and arrived here to redo the work. Adopt that pull
	// request from the ledger instead of implementing again and stacking a second one beside it.
	if adopted, ok := x.adoptPriorDelivery(run, res, repo.ID, repo.FullName); ok {
		rr = adopted
		res.Live = &rr
		saver.force()
		log.Printf("devlabd: run %s repo %s — adopted the open PR %s from this same execution (duplicate avoided)", run.ID, repo.ID, adopted.PRUrl)
		return rr, repoSignal{}
	}

	// NOTE: a repo with an open Mercury PR is NEVER skipped, and its not-yet-merged work is never redone.
	// The run builds on the persistent dev branch (mercury-dev), which already CARRIES every prior
	// delivery — so the agent sees not-yet-merged work as present without any PR-folding step (see the dev
	// branch setup below).

	// Clone (first run only; Ensure is a no-op once cloned). Retry a few times on a connectivity blip
	// before giving up, and tell a network failure apart from a broken repo: a network failure carries
	// the whole sweep over (infra), a repo failure is this repo's own terminal error.
	if err := retryInfra(ctx, 3, 10*time.Second, func() error {
		_, e := x.s.workspaces.Ensure(ctx, x.user, repo.ID, repo.FullName, token, true)
		return e
	}); err != nil {
		if isInfraError(err) {
			return rr, repoSignal{infra: true, infraErr: "clone " + repo.ID + ": " + err.Error()}
		}
		rr.Error = "clone: " + err.Error()
		return rr, repoSignal{}
	}
	unlock, err := x.s.workspaces.Lock(x.user, repo.ID)
	if err != nil {
		rr.Error = "lock: " + err.Error()
		return rr, repoSignal{}
	}
	defer unlock()
	var branch string
	if err := retryInfra(ctx, 3, 10*time.Second, func() error {
		b, e := github.DefaultBranch(ctx, token, repo.FullName)
		branch = b
		return e
	}); err != nil || branch == "" {
		if isInfraError(err) {
			return rr, repoSignal{infra: true, infraErr: "default branch " + repo.ID + ": " + err.Error()}
		}
		rr.Error = "default branch: " + errString(err)
		return rr, repoSignal{}
	}
	wt, err := x.s.workspaces.Path(x.user, repo.ID)
	if err != nil {
		rr.Error = "workspace: " + err.Error()
		return rr, repoSignal{}
	}
	ex := workspace.Executor{User: x.user, PerUser: true}

	devBranch := devBranchName()
	rr.DevBranch = devBranch // the delivered state is always nameable (req 2), even when this run adds nothing

	// GROW, don't reconstruct. Sit on the persistent per-repo dev branch (mercury-dev) — the accumulated
	// state of every previous run — instead of hard-resetting to the default branch. This is the SINGLE
	// way a state comes to be: prior not-yet-merged work is already present because the dev branch carries
	// it, so the old "fold the open PRs in before building" special case is gone (removed with
	// openMercuryPRHeads). EnsureDevBranch fetches too, so a connectivity failure carries the sweep over.
	var devPrep workspace.DevBranchPrep
	if err := retryInfra(ctx, 3, 10*time.Second, func() error {
		p, e := ex.EnsureDevBranch(ctx, wt, token, branch, devBranch)
		devPrep = p
		return e
	}); err != nil {
		if isInfraError(err) {
			return rr, repoSignal{infra: true, infraErr: "dev-Branch vorbereiten " + repo.ID + ": " + err.Error()}
		}
		rr.Error = "dev-Branch vorbereiten: " + err.Error()
		return rr, repoSignal{}
	}
	// Recovered work (req 5): an interrupted prior run left committed-but-unpublished commits in this
	// workspace. They are NOT silently absorbed — the step names them, and marking devPrep.Recovered on the
	// dev-advance decision below guarantees the dev push publishes them even if this run adds nothing else.
	if devPrep.Recovered > 0 {
		log.Printf("devlabd: run %s repo %s — recovered %d committed-but-unpublished commit(s) an interrupted run left on %s; retained and republishing", run.ID, repo.ID, devPrep.Recovered, devBranch)
		step("dev-branch", fmt.Sprintf("%d festgeschriebene(r) Commit(s) eines unterbrochenen Laufs im Arbeitsstand gefunden, übernommen und mit ausgeliefert", devPrep.Recovered), runs.StepOK)
	}
	// A pushed dev state that will not fold cleanly is non-fatal (req 2): the local state is kept as-is and
	// the run continues on it, simply without the very newest pushed commits until a later run resolves it.
	if devPrep.RemoteConflict {
		log.Printf("devlabd: run %s repo %s — pushed %s did not fold cleanly into the local dev state; kept local, continuing", run.ID, repo.ID, devBranch)
		step("dev-branch", "veröffentlichter dev-Stand ("+devBranch+") nicht konfliktfrei einfaltbar — auf dem lokalen Stand fortgeführt", runs.StepNotApplicable)
	}
	// Hygiene without loss of history: clear an aborted run's half-changes (uncommitted edits + untracked
	// files); the branch pointer — and the accumulated history — is untouched (req 4).
	if err := ex.CleanWorktree(ctx, wt); err != nil {
		rr.Error = "Arbeitsbaum säubern: " + err.Error()
		return rr, repoSignal{}
	}
	startTip, err := ex.RevParse(ctx, wt, "HEAD")
	if err != nil {
		rr.Error = "dev-Tip ermitteln: " + err.Error()
		return rr, repoSignal{}
	}
	// Fold the default branch INTO the dev state (never reset the state onto it — req 1). A conflict is
	// non-fatal: the state simply keeps going without the very newest default until a run resolves it.
	if err := ex.FoldInBranch(ctx, wt, branch); err != nil {
		if errors.Is(err, workspace.ErrMergeConflict) {
			step("fold", "Standard-Branch "+branch+" nicht konfliktfrei einfaltbar — dev-Stand ohne die neuesten "+branch+"-Änderungen fortgeführt", runs.StepNotApplicable)
		} else {
			rr.Error = "Standard-Branch einfalten: " + err.Error()
			return rr, repoSignal{}
		}
	}
	// The delivery base is the dev tip AFTER the fold, so a delivery's range is exactly the agent's own
	// linear commits (no fold merge) — the range a counter-booking later reverses.
	deliveryBase, err := ex.RevParse(ctx, wt, "HEAD")
	if err != nil {
		rr.Error = "Lieferungs-Basis ermitteln: " + err.Error()
		return rr, repoSignal{}
	}

	// The stand the agent actually examines: the refreshed remote tip. Recorded per axiom once this repo
	// succeeds, so the next run can scope itself to the commits after it.
	if head, herr := ex.RevParse(ctx, wt, "origin/"+branch); herr == nil {
		rr.Base = head
	}

	// Per-repo TIME BUDGET: the run's own cap (budgetFor), else the service default; 0 = no cap, leaving
	// only the whole-sweep duration as the bound. The context carries errBudgetExceeded as its cause so an
	// overrun is reported honestly — as a spent budget with the value that applied — not as a raw kill.
	actx, cancel := context.WithCancel(ctx)
	budget := budgetFor(run)
	if budget > 0 {
		cancel()
		actx, cancel = context.WithTimeoutCause(ctx, budget, errBudgetExceeded)
	}
	defer cancel()

	// REPORT: read-only plan against the dev state; no push, no deploy, no delivery.
	if x.mode == "report" {
		lim, err := x.runAgentLive(actx, ex, wt, prompt, "plan", "analyze", tuningFor(run), atts, &rr, saver)
		if lim.limited {
			return rr, lim
		}
		if err != nil {
			rr.Error = "analyze: " + budgetAwareError(actx, budget, err)
			return rr, repoSignal{}
		}
		// tokens were already settled onto rr inside runAgentLive (climbing live during the analyze step)
		if tip, e := ex.RevParse(ctx, wt, "HEAD"); e == nil {
			rr.DevCommit = tip
		}
		rr.OK = runs.StepsSucceeded(rr.Steps) // the repo result follows the chain (req 5)
		return rr, repoSignal{}
	}

	// Publish the dev state as it GROWS (req 3): while the agent implements — routinely committing on its
	// own, sometimes for a long time — a background checkpoint pushes mercury-dev whenever it advances, so
	// an interruption costs at most the work since the last checkpoint, never the whole run. It is stopped
	// explicitly before the deploy/publish stages (so it can never race the authoritative atomic push) and
	// deferred as well, to cover the early-return paths below. It uses the outer ctx, not the budget actx,
	// so a spent time budget still lets the last checkpoint land.
	stopCheckpoint := x.startDevCheckpoint(ctx, ex, wt, token, devBranch)
	defer stopCheckpoint()

	// PR / FULL: implement ON the dev branch (it already carries the accumulated work).
	lim, err := x.runAgentLive(actx, ex, wt, prompt, "bypassPermissions", "implement", tuningFor(run), atts, &rr, saver)
	if lim.limited {
		return rr, lim
	}
	if err != nil {
		rr.Error = "implement: " + budgetAwareError(actx, budget, err)
		recordNotExecuted(&rr, x.mode, "implement", saver)
		return rr, repoSignal{}
	}
	// tokens were already settled onto rr inside runAgentLive (climbing live during the implement step)

	// The agent usually commits its own work — Claude Code does that routinely. Judging by the
	// WORKING TREE alone therefore misses a finished implementation entirely: the tree is clean, the
	// run concludes "nothing happened", and real commits are silently discarded while the result is
	// reported OK. So: commit whatever is still loose, then decide by what is ahead of the base.
	if changes := workspace.Changes(wt); len(changes) > 0 {
		for _, c := range changes {
			if err := ex.Stage(ctx, wt, c.Path); err != nil {
				rr.Error = "stage " + c.Path + ": " + err.Error()
				return rr, repoSignal{}
			}
		}
		if _, _, err := ex.Commit(ctx, wt, "mercury-run: "+run.Name, ghLogin, ghID); err != nil {
			rr.Error = "commit: " + err.Error()
			return rr, repoSignal{}
		}
	}
	stopCheckpoint() // implement done; publish deliberately from here, no more background checkpoints

	finalTip, err := ex.RevParse(ctx, wt, "HEAD")
	if err != nil {
		rr.Error = "dev-Tip ermitteln: " + err.Error()
		return rr, repoSignal{}
	}
	rr.DevCommit = finalTip

	// A DELIVERY is the runner's own work: the commits the agent added on top of the delivery base (the
	// fold). devAdvanced is broader — the dev branch moved AT ALL (agent work and/or a folded-in default
	// branch), i.e. there is a new state to serve on dev. A brand-new dev branch always counts.
	deliveryCommits, err := ex.CommitsAhead(ctx, wt, deliveryBase)
	if err != nil {
		rr.Error = "Commits zählen: " + err.Error()
		return rr, repoSignal{}
	}
	hasDelivery := deliveryCommits > 0
	// The dev state is worth publishing when this run added work (tip moved), when the branch was just
	// created, OR when it recovered an interrupted run's unpublished commits — the last case pushes them to
	// the durable remote even if this run itself changed nothing (req 3/5).
	devAdvanced := devPrep.Created || devPrep.Recovered > 0 || finalTip != startTip

	if !devAdvanced {
		step("implement", "keine neuen Änderungen — dev-Stand unverändert ("+devBranch+"@"+short(finalTip)+"), nichts auszuliefern", runs.StepOK)
		rr.OK = runs.StepsSucceeded(rr.Steps) // the repo result follows the chain (req 5)
		return rr, repoSignal{}
	}

	// Publish the grown dev state NOW — before the (possibly slow) dev-deploy and the delivery-branch/PR
	// stages — so a crash in any of them cannot cost the implement step's work (req 3). Best-effort: the
	// authoritative atomic push below still gates the delivery, so a transient failure here is not terminal.
	if _, err := ex.PushRefs(ctx, wt, token, false, devBranch); err != nil {
		log.Printf("devlabd: pre-deploy dev publish (%s) failed, deferring to the final push: %v", devBranch, err)
	}

	// DEV-DEPLOY (full mode only) — delivers EXACTLY the dev branch: nothing folded together, filtered or
	// skipped (req 2). BEFORE the push, deliberately: dev is the very box we built on, so this workspace
	// already IS the source record. PROD is untouched here — it ships only from a MERGED default branch
	// (Maintain, req 6).
	//
	// Finding C: the build runs UNPRIVILEGED here; the root wrapper only installs+restarts. The two outcomes
	// are split honestly: a dev-deploy that does NOT APPLY (switched off, or no vetted deploy target for
	// this repo) is recorded as not-applicable and the chain CONTINUES — it is never a green success. A
	// dev-deploy that genuinely FAILS (build or install) HALTS the chain: push and PR are marked
	// not-executed and no PR is opened, so a delivery never sits on top of code that does not run on dev.
	switch {
	case x.mode != "full":
		// report/pr never deploy at all — no step, as before.
	case !x.shouldDevDeploy(repo.ID):
		step("dev-deploy", devDeploySkipReason(repo.ID), runs.StepNotApplicable)
	case !x.deployable(repo.Name):
		step("dev-deploy", noDeployTargetReason(repo.Name), runs.StepNotApplicable)
	default:
		// No folding of open PRs any more: the dev branch IS the accumulated state, so what is built here
		// is by construction the sum of everything delivered so far. Assembling a state per deploy — the
		// old approach — is what let features disappear whenever one PR did not merge cleanly.
		if artifactDir, berr := x.buildArtifact(ctx, wt, repo); berr != nil {
			step("dev-deploy", "Build fehlgeschlagen: "+berr.Error(), runs.StepFailed)
			rr.Error = "dev-deploy (Build): " + berr.Error()
			recordNotExecuted(&rr, x.mode, "dev-deploy", saver)
			return rr, repoSignal{}
		} else if depLog, derr := x.deploy(ctx, repo, artifactDir, "dev"); derr != nil {
			step("dev-deploy", depLog+"\n"+derr.Error(), runs.StepFailed)
			rr.Error = "dev-deploy: " + derr.Error()
			recordNotExecuted(&rr, x.mode, "dev-deploy", saver)
			return rr, repoSignal{}
		} else {
			step("dev-deploy", depLog+"\nAusgelieferter Stand: "+devBranch+"@"+short(finalTip), runs.StepOK)
			rr.Deployed = true
		}
	}

	// Publish the grown dev state. Push mercury-dev so the durable, nameable record advances; when this run
	// produced a delivery, ALSO snapshot the dev tip as an immutable per-delivery branch for its stacked
	// PR. A push failure is terminal for this repo.
	var deliveryID, deliveryBranch string
	if hasDelivery {
		deliveryID = runs.NewDeliveryID()
		deliveryBranch = "mercury-run/" + run.ID + "/" + deliveryID
		if err := ex.BranchAt(ctx, wt, deliveryBranch, "HEAD"); err != nil {
			// Branch prep is part of publishing — a failure here halts the chain under the push stage so a
			// genuinely errored repo never mislabels push as merely not-applicable.
			step("push", "Lieferungs-Branch anlegen: "+err.Error(), runs.StepFailed)
			rr.Error = "Lieferungs-Branch: " + err.Error()
			recordNotExecuted(&rr, x.mode, "push", saver)
			return rr, repoSignal{}
		}
	}
	// The dev branch is pushed as a BACKUP, never as a way onto the default branch: it is the ONLY durable
	// record of work that is already built but not yet merged, so if a workspace is lost the accumulated dev
	// state can still be recovered from the remote. It is deliberately never raised as a pull request and
	// never deleted (unlike a delivery branch, which is pruned once merged) — one per repo, the same name
	// (mercury-dev) everywhere. The ONLY path onto the default branch is the per-delivery branch and its PR.
	pushRefs := []string{devBranch}
	if deliveryBranch != "" {
		pushRefs = append(pushRefs, deliveryBranch)
	}
	if _, err := ex.PushRefs(ctx, wt, token, false, pushRefs...); err != nil {
		step("push", strings.Join(pushRefs, ", ")+"\n"+err.Error(), runs.StepFailed)
		rr.Error = "push: " + err.Error()
		recordNotExecuted(&rr, x.mode, "push", saver)
		return rr, repoSignal{}
	}
	step("push", strings.Join(pushRefs, ", "), runs.StepOK)

	if !hasDelivery {
		// The dev branch advanced only by folding in the default branch — dev now serves it, but there is
		// no PR-worthy unit of the runner's OWN work, so no delivery and no PR.
		step("implement", "nur Standard-Branch eingefaltet — dev-Stand aktualisiert ("+devBranch+"@"+short(finalTip)+"), kein eigener Beitrag", runs.StepOK)
		rr.OK = runs.StepsSucceeded(rr.Steps) // the repo result follows the chain (req 5)
		return rr, repoSignal{}
	}

	// Stacked PR base (req 9): the previous still-open delivery's branch, else the default branch. So the
	// PR shows ONLY this delivery's changes even though its branch sits on the prior work (req 3). GitHub
	// re-targets the base to the default branch automatically once the predecessor merges.
	prBase := branch
	if prev, ok, _ := x.s.deliveries.LatestOpen(repo.FullName); ok && prev.Branch != "" {
		prBase = prev.Branch
	}
	pr, err := github.CreatePullRequest(ctx, token, repo.FullName, deliveryBranch, prBase, "Mercury-Lauf: "+run.Name, runPRBody(run))
	if err != nil {
		if found, ok := github.FindOpenPullRequest(ctx, token, repo.FullName, deliveryBranch); ok {
			pr = found
		} else {
			step("pr", err.Error(), runs.StepFailed)
			rr.Error = "pr: " + err.Error()
			return rr, repoSignal{}
		}
	}
	rr.PRUrl = pr.HTMLURL
	rr.PRBase = prBase
	rr.DeliveryID = deliveryID
	step("pr", pr.HTMLURL+" (Basis: "+prBase+")", runs.StepOK)
	now := time.Now().UTC()
	_ = x.s.runPRs.Add(runs.PendingPR{
		Repo: repo.FullName, Number: pr.Number, URL: pr.HTMLURL, RunID: run.ID,
		CreatedAt: now, MergeBy: now.Add(x.autoMergeAfter),
	})
	// Record the delivery: the addressable unit (req 8) — its commit range, snapshot branch, stacked PR.
	_ = x.s.deliveries.Add(runs.Delivery{
		ID: deliveryID, RunID: run.ID, ResultID: res.ResultID, RunName: run.Name, Repo: repo.FullName,
		Branch: deliveryBranch, DevBranch: devBranch, BaseBranch: prBase,
		FromCommit: deliveryBase, ToCommit: finalTip, PRNumber: pr.Number, PRUrl: pr.HTMLURL,
		CreatedAt: now, Status: runs.DeliveryOpen,
	})

	// PROD-DEPLOY is intentionally NOT done here — prod ships only from a MERGED default branch (Maintain,
	// req 6). dev already serves the grown state above.
	rr.OK = runs.StepsSucceeded(rr.Steps) // the repo result follows the chain (req 5)
	return rr, repoSignal{}
}

// foldPendingForDeploy prepares the tree the dev-deploy BUILDS from: this run's branch plus every other
// open Mercury PR on the repo, merged into a throwaway local branch. It returns a restore func that
// switches back to the run branch, so push and PR still carry this run's work alone.
//
// An automatic run already bases on main + pending PRs, so there is nothing to fold; a ToDo bases on
// plain main, which is exactly the case that used to make dev regress. A branch that does not merge
// cleanly is skipped with a logged note — a deploy is worth doing even if one pending PR conflicts.
func (x *runExecutor) foldPendingForDeploy(ctx context.Context, ex workspace.Executor, wt, token string,
	repo model.Repo, run runs.Run, runBranch string, step func(name, logtxt string, ok bool)) func() {
	noop := func() {}
	if !run.IsTodo() {
		return noop
	}
	heads := x.openPRHeads(ctx, token, repo.FullName)
	var others []string
	for _, h := range heads {
		if h != runBranch {
			others = append(others, h)
		}
	}
	if len(others) == 0 {
		return noop
	}
	deployBranch := "mercury-dev/" + strings.TrimPrefix(runBranch, "mercury-run/")
	if err := ex.CreateBranch(ctx, wt, deployBranch, runBranch); err != nil {
		step("dev-deploy", "Sammel-Branch nicht anlegbar — es wird nur die Arbeit dieses Laufs deployt: "+err.Error(), false)
		return noop
	}
	_ = ex.Fetch(ctx, wt, token)
	merged := 0
	for _, h := range others {
		if err := ex.MergeRef(ctx, wt, "origin/"+h); err != nil {
			step("dev-deploy", "offener PR-Branch "+h+" nicht konfliktfrei mergebar — ohne ihn deployt: "+err.Error(), false)
			continue
		}
		merged++
	}
	step("dev-deploy", fmt.Sprintf("dev-Stand = diese Arbeit + %d offene(r) PR(s)", merged), true)
	return func() {
		if err := ex.Checkout(ctx, wt, runBranch); err != nil {
			log.Printf("devlabd: run %s could not return to %s after the deploy build: %v", run.ID, runBranch, err)
		}
	}
}

// shouldDevDeploy reports whether executeRepo performs an in-process dev-deploy for repoID: only in
// full mode and only while dev-deploy is enabled. report/pr never dev-deploy at all.
//
// The self repo is NO LONGER excluded. It used to be (dev-deploying devlab restarts THIS devlabd and
// would kill the running sweep), but the exclusion had no counterpart: the one service the owner
// watches never updated from its own runs, so every ToDo against it landed as an unmerged PR and
// nothing was ever live. The disruptive half is only the RESTART, not the install — so the per-repo
// deploy script installs immediately and defers the restart until the run slot is free (see
// deploy/devlab-restart-idle and the busy marker in the runs package).
func (x *runExecutor) shouldDevDeploy(repoID string) bool {
	return x.mode == "full" && devDeployEnabled()
}

// devDeploySkipReason explains, for the recorded step, why shouldDevDeploy said no in full mode: the
// kill switch is now the only reason. Any other "no" comes from the deploy target, which
// hasDeployTarget/noDeployTargetReason report.
func devDeploySkipReason(repoID string) string {
	return "dev-deploy abgeschaltet (DEVLAB_RUNS_DEV_DEPLOY) — nur der prod-Deploy bei Merge ist scharf"
}

// hasDeployTarget reports whether repoName has a vetted per-repo deploy script — the SAME allowlist the
// root wrapper enforces (/etc/devlab/deploy.d/<repo>), and therefore the single source of truth for "is
// this repo deployable at all". Repos without one are libraries, templates, axiom/semantics data or
// tooling: they install no service anywhere, so building and deploying them is not a failure to report
// but a step to SKIP — visibly, and BEFORE the build. Attempting it anyway produced the two noisiest
// false alarms in the pipeline: a red "nothing to build (no package.json / go.mod)" for data repos, and a
// merged PR whose prod-deploy failed with wrapper exit 3 and was retried every recheck interval forever.
func hasDeployTarget(repoName string) bool {
	fi, err := os.Stat(filepath.Join(deployScriptDir, repoName))
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

func noDeployTargetReason(repoName string) string {
	return "kein Deploy-Ziel — für " + repoName + " ist kein geprüftes Deploy-Skript hinterlegt (" +
		filepath.Join(deployScriptDir, repoName) + "); das Repo installiert keinen Dienst, es gibt nichts zu deployen"
}

// deliveryChain is the ordered pipeline executeRepo walks for a mode — the sequence whose order is
// BINDING. A halt at a failed step records every LATER stage of this chain as not-executed, so a result
// states plainly WHERE the chain stopped and that nothing past it was attempted.
func deliveryChain(mode string) []string {
	switch mode {
	case "report":
		return []string{"analyze"}
	case "full":
		return []string{"implement", "dev-deploy", "push", "pr"}
	default: // pr
		return []string{"implement", "push", "pr"}
	}
}

// recordNotExecuted appends a not-executed step for every stage of the delivery chain AFTER `failed` that
// has no recorded step yet — the binding-order guarantee made explicit: once a step fails the rest is
// never attempted, and it is shown as not-executed (never skipped, and never a hollow success). The
// reason states plainly, in the user's language, that the chain was halted by the failed step.
func recordNotExecuted(rr *runs.RepoResult, mode, failed string, saver *liveSaver) {
	have := make(map[string]bool, len(rr.Steps))
	for i := range rr.Steps {
		have[rr.Steps[i].Name] = true
	}
	reason := "nicht ausgeführt — vorheriger Schritt fehlgeschlagen (" + failed + "); die Kette wurde angehalten"
	past := false
	for _, name := range deliveryChain(mode) {
		if name == failed {
			past = true
			continue
		}
		if !past || have[name] {
			continue
		}
		rr.Steps = append(rr.Steps, runs.Step{Name: name, Status: runs.StepNotExecuted, Log: reason, At: time.Now().UTC()})
	}
	saver.force()
}

// repoNameOf reduces an owner/repo full name to the bare repo name the deploy allowlist is keyed by.
func repoNameOf(fullName string) string {
	if i := strings.LastIndex(fullName, "/"); i >= 0 {
		return fullName[i+1:]
	}
	return fullName
}

// devBranchName is the persistent per-repo integration branch the runner GROWS and that dev serves —
// the single, nameable source of the dev state (req 2). Overridable via DEVLAB_RUNS_DEV_BRANCH; default
// "mercury-dev". It is never the default branch (prod ships from that).
func devBranchName() string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_DEV_BRANCH")); v != "" {
		return v
	}
	return "mercury-dev"
}

// short trims a commit SHA for display in a step/result (the delivered state is named as <branch>@<sha>).
func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// devDeployEnabled reports whether the in-run dev-deploy step is active. DEVLAB_RUNS_DEV_DEPLOY=off (or
// 0/false/no) turns it OFF so full mode arms ONLY the prod-deploy-on-merge half of the pipeline. This is
// how full is armed SAFELY before the dev/prod cutover: while the dev instances still serve live traffic
// (env-less names), a run must not restart them — so dev-deploy is disabled, and only merged PRs deploy
// (to the prod VPS, which is not the box the runner runs on). Default on.
func devDeployEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DEVLAB_RUNS_DEV_DEPLOY")))
	return v != "off" && v != "0" && v != "false" && v != "no"
}

// selfRepoID is the repo id of the DevLab service itself — its own run must never in-process
// self-deploy (the deploy restarts devlabd, killing the executor mid-sweep). Overridable via
// DEVLAB_RUNS_SELF_REPO so the guard isn't a hardcoded instance assumption, and matched
// case-insensitively (see isSelfRepo). Default "devlab".
func selfRepoID() string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_SELF_REPO")); v != "" {
		return v
	}
	return "devlab"
}

func isSelfRepo(repoID string) bool {
	return strings.EqualFold(strings.TrimSpace(repoID), selfRepoID())
}

// detectLimit adapts mercury.DetectUsageLimit to the executor's repoSignal signal.
func detectLimit(out []byte, err error) repoSignal {
	limited, resetAt, hasReset := mercury.DetectUsageLimit(out, err)
	return repoSignal{limited: limited, resetAt: resetAt, hasReset: hasReset}
}

// infraErrorMarkers are substrings of git/HTTP errors that mean the HOST's connectivity failed, not the
// repository — a DNS outage, a dropped link, a refused/timed-out connection. Matched case-insensitively.
// These must carry the sweep over (retry later) rather than fail the repo permanently.
var infraErrorMarkers = []string{
	"could not resolve host",
	"temporary failure in name resolution",
	"server misbehaving",
	"name or service not known",
	"dial tcp",
	"no such host",
	"connection refused",
	"connection reset",
	"network is unreachable",
	"no route to host",
	"i/o timeout",
	"timeout",
	"timed out",
	"tls handshake",
	"unexpected eof",
	"eof",
	"could not read from remote",
	"failed to connect",
	"operation timed out",
}

// hasInfraMarker reports whether a message carries a connectivity-failure signature. Split out of
// isInfraError so the SAME classification can run over a deploy wrapper's combined OUTPUT (where the real
// ssh/rsync cause lives) as well as over a Go error's text.
func hasInfraMarker(s string) bool {
	m := strings.ToLower(s)
	for _, marker := range infraErrorMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// isInfraError reports whether an error is an infrastructure/connectivity failure (host can't reach
// GitHub) rather than a problem with the specific repo or the work. This is the "gucken, WARUM nicht
// geklont werden kann" step: the runner distinguishes "the network is down" from "this repo is broken"
// and reacts differently — the former is transient and retried, the latter is a real per-repo failure.
func isInfraError(err error) bool {
	return err != nil && hasInfraMarker(err.Error())
}

// deployTransient tells a temporary prod-deploy failure (network hiccup, target briefly unreachable —
// keep retrying, never counts toward a block) from a permanent one (everything else — count an attempt,
// block after a few). It inspects the wrapper's combined OUTPUT first, because the real ssh/rsync cause
// lives there, then the Go exit-status error.
func deployTransient(out string, err error) bool {
	return hasInfraMarker(out) || isInfraError(err)
}

// deploySetupMissingMarkers are signatures that a prod-deploy failed because the service is NOT SET UP on
// the target — the unit isn't installed, no vetted deploy script exists, no prod target/key is configured,
// nothing was staged. Used ONLY to phrase the block reason honestly ("nicht eingerichtet") — never to
// decide transient-vs-permanent (all of these are permanent).
var deploySetupMissingMarkers = []string{
	".service not found", // systemctl: the prod unit is not installed on the target
	"unit not found",
	"no such unit",
	"no deploy script",   // wrapper exit 3: no vetted per-repo script on the runner host
	"prod: missing",      // per-repo script exit 11: missing prod-target / deploy key config
	"prod: empty target",
	"no staged artifact", // receiver exit 3: nothing staged / no mapping for this repo on the target
	"no mapping",
	"unknown env",
}

func deploySetupMissing(out string) bool {
	m := strings.ToLower(out)
	for _, marker := range deploySetupMissingMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// deployBlockReason phrases a human cause naming the service and the target, so a blocked delivery reads
// as "Dienst X ist im Ziel Y nicht eingerichtet: …" rather than a bare exit code.
func deployBlockReason(service, out string, err error) string {
	target := prodTargetName()
	detail := lastMeaningfulLine(out, err)
	if deploySetupMissing(out) {
		return fmt.Sprintf("Dienst »%s« ist im Ziel »%s« nicht eingerichtet: %s", service, target, detail)
	}
	return fmt.Sprintf("Auslieferung von »%s« nach »%s« dauerhaft fehlgeschlagen: %s", service, target, detail)
}

// prodTargetName is the human name of the prod target for reasons/logs (DEVLAB_RUNS_PROD_TARGET_NAME
// overrides). Instance-neutral: a name, never a host.
func prodTargetName() string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_PROD_TARGET_NAME")); v != "" {
		return v
	}
	return "prod"
}

// lastMeaningfulLine returns the last non-blank line of the deploy output (the actual failure), or the
// Go error text if the output was empty — clipped so a reason never dumps a whole log.
func lastMeaningfulLine(out string, err error) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return clipLine(s)
		}
	}
	if err != nil {
		return clipLine(err.Error())
	}
	return "unbekannter Fehler"
}

func clipLine(s string) string {
	const max = 240
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// noDeployRepos parses DEVLAB_RUNS_NO_DEPLOY — bare repo names a service prod deliberately does NOT run —
// into a case-insensitive set. Separators: comma, space, newline, tab, semicolon.
func noDeployRepos() map[string]bool {
	out := map[string]bool{}
	fields := strings.FieldsFunc(os.Getenv("DEVLAB_RUNS_NO_DEPLOY"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';'
	})
	for _, f := range fields {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out[f] = true
		}
	}
	return out
}

func isNoDeployRepo(name string) bool {
	return noDeployRepos()[strings.ToLower(strings.TrimSpace(name))]
}

// defaultMaxDeployAttempts / maxDeployAttempts bound how many PERMANENT prod-deploy failures a merged PR
// suffers before it is blocked (waits for an explicit resume). DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS overrides;
// a non-positive value keeps the default.
const defaultMaxDeployAttempts = 3

func maxDeployAttempts() int {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxDeployAttempts
}

// shipToProd is the SINGLE gate deciding whether a merged PR's repo should be prod-deployed at all. Two
// no-ship cases, unified: a repo explicitly listed as not-to-be-delivered (DEVLAB_RUNS_NO_DEPLOY), and a
// repo with no deploy target configured. Either way the caller untracks the PR with NO deploy attempt.
func (x *runExecutor) shipToProd(name string) (bool, string) {
	if isNoDeployRepo(name) {
		return false, "als nicht auszuliefern geführt (DEVLAB_RUNS_NO_DEPLOY) — " + name +
			" wird bewusst nicht nach prod ausgeliefert"
	}
	if !x.deployable(name) {
		return false, noDeployTargetReason(name)
	}
	return true, ""
}

// githubReachable probes GitHub's API (a cheap, unauthenticated-safe rate-limit GET) to decide whether
// connectivity is up. Uses the token so a network-level block surfaces, not an auth wall.
func githubReachable(ctx context.Context, token string) error {
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// GET /user is a minimal authenticated call; an infra error here is a genuine connectivity failure,
	// while any HTTP response (even an error status) proves the network itself is up.
	_, err := github.GetViewer(pctx, token)
	return err
}

// ensureGitHubReachable is the self-healing wait: before a sweep starts, if GitHub is unreachable the
// runner does not charge ahead cloning 19 repos into 19 DNS failures — it WAITS for connectivity to
// come back (short blips like the 5-minute resolver degradation that killed the 21.07 night), retrying
// with backoff. Returns nil once reachable, or the last error if it never came up within the budget
// (the caller then carries the whole sweep over to the next fire). Honours ctx cancellation/deadline.
func ensureGitHubReachable(ctx context.Context, token string) error {
	const attempts = 8
	const backoff = 30 * time.Second
	var last error
	for i := 0; i < attempts; i++ {
		if err := githubReachable(ctx, token); err == nil {
			if i > 0 {
				log.Printf("devlabd: GitHub reachable again after %s — sweep proceeding", plural(i, "retry"))
			}
			return nil
		} else if !isInfraError(err) {
			return nil // reachable enough (a non-connectivity error still proves the network is up)
		} else {
			last = err
		}
		if i < attempts-1 {
			log.Printf("devlabd: GitHub unreachable (%v) — waiting %s before retry %d/%d", last, backoff, i+2, attempts)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return last
}

// retryInfra runs op up to `attempts` times, but ONLY retries INFRASTRUCTURE failures (a network blip);
// a real error (repo gone, auth rejected, a work failure) returns immediately so the caller can handle
// it as terminal. Backs off between tries and honours ctx cancellation. Returns the last error (nil on
// success). This is the "noch paar mal versuchen" — a couple of retries before resigning.
func retryInfra(ctx context.Context, attempts int, backoff time.Duration, op func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = op(); err == nil || !isInfraError(err) {
			return err
		}
		if i < attempts-1 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(backoff):
			}
		}
	}
	return err
}

// openMercuryPRHeads returns the head branch names of every OPEN Mercury PR on fullName — the still-
// pending, not-yet-merged work the run must BASE ON (main + pending), so it neither redoes it nor skips
// the repo. A Mercury PR is recognised by its hidden body marker (mercuryPRMarker), which is stable and
// language-independent now that the branch itself follows the human <kind>/<description> convention; the
// legacy "mercury-run/" branch prefix is still matched so a PR opened before the rename folds in too. A
// lookup error yields no heads (the run then bases on plain main; a genuine outage is caught as an infra
// carry-over at the clone step).
func (x *runExecutor) openMercuryPRHeads(ctx context.Context, token, fullName string) []string {
	prs, err := github.ListOpenPullRequests(ctx, token, fullName)
	if err != nil {
		return nil
	}
	var heads []string
	for _, pr := range prs {
		if isMercuryPR(pr) {
			heads = append(heads, pr.Head.Ref)
		}
	}
	return heads
}

// isMercuryPR reports whether pr was opened by a Mercury run: primarily by the hidden body marker, with a
// fallback to the legacy branch prefix for PRs created before branches moved to <kind>/<description>.
func isMercuryPR(pr github.PullRequest) bool {
	return strings.Contains(pr.Body, mercuryPRMarker) || strings.HasPrefix(pr.Head.Ref, "mercury-run/")
}

// openPRHeads returns the head branch of EVERY open pull request on fullName — Mercury's own and the
// ones a human raised alike. The dev box should show the sum of what is pending, whoever wrote it: a
// deploy that folds in only Mercury's branches silently reinstalls over a person's un-merged work, which
// is exactly how a hand-built change vanished from dev minutes after it was deployed. The base of a RUN
// stays Mercury-only (openMercuryPRHeads) — that is about not redoing the agent's own work — while the
// DEPLOY, which decides what is live, takes everything.
func (x *runExecutor) openPRHeads(ctx context.Context, token, fullName string) []string {
	prs, err := github.ListOpenPullRequests(ctx, token, fullName)
	if err != nil {
		return nil
	}
	heads := make([]string, 0, len(prs))
	for _, pr := range prs {
		heads = append(heads, pr.Head.Ref)
	}
	return heads
}

// resolveTodoTargets resolves every target of a ToDo to a concrete repo. A to-be-created target is
// created first (CreateRepo is idempotent — an already-existing repo of that name is reused, which also
// makes a resume safe); an existing target is looked up in the discovered set. It returns the resolved
// repos, a repo-id→new-name map (non-empty only for freshly-planned repos, so the prompt tells the
// agent to scaffold them), and a failed RepoResult for every target that could not be resolved. The
// existing set is listed at most once, lazily. Duplicate targets were already collapsed in validation.
func (x *runExecutor) resolveTodoTargets(ctx context.Context, run runs.Run, token string) ([]model.Repo, map[string]string, []runs.RepoResult) {
	newRepoName := map[string]string{}
	var repos []model.Repo
	var fails []runs.RepoResult

	var existing []model.Repo
	var listErr error
	listed := false
	ensureListed := func() {
		if !listed {
			existing, listErr = discover.ReposForUser(ctx, x.tokenUser, token)
			listed = true
		}
	}

	for _, t := range run.TodoTargets() {
		if t.NewRepo != "" {
			full, err := github.CreateRepo(ctx, token, discover.Owner(), t.NewRepo,
				"Holistic-Service — angelegt vom Mercury-ToDo \""+run.Name+"\"", discover.Topic())
			if err != nil {
				fails = append(fails, runs.RepoResult{Repo: t.NewRepo, OK: false, Error: fmt.Sprintf("Repo %q anlegen: %v", t.NewRepo, err)})
				continue
			}
			repos = append(repos, model.Repo{ID: t.NewRepo, Name: t.NewRepo, FullName: full})
			newRepoName[t.NewRepo] = t.NewRepo
			continue
		}
		ensureListed()
		if listErr != nil {
			fails = append(fails, runs.RepoResult{Repo: t.Repo, OK: false, Error: "Repos ermitteln: " + listErr.Error()})
			continue
		}
		if r, ok := findRepo(existing, t.Repo); ok {
			repos = append(repos, r)
		} else {
			fails = append(fails, runs.RepoResult{Repo: t.Repo, OK: false, Error: fmt.Sprintf("Ziel-Repo %q nicht gefunden", t.Repo)})
		}
	}
	return repos, newRepoName, fails
}

// findRepo matches a target's repo reference (an id or a bare name) against the discovered set.
func findRepo(repos []model.Repo, idOrName string) (model.Repo, bool) {
	for _, r := range repos {
		if r.ID == idOrName || r.Name == idOrName {
			return r, true
		}
	}
	return model.Repo{}, false
}

// promptFor is the prompt one repo of a run receives. An auto run uses its stored snapshot for every
// repo; a ToDo composes its prompt per target so that ONLY a freshly-created repo (newRepo non-empty)
// is told to scaffold from scratch, while an existing target is worked as-is. A ToDo's attachments are
// the same across targets and are announced in every target's prompt (they are materialized per repo).
func (x *runExecutor) promptFor(run runs.Run, repoID, newRepo string, atts []loadedAttachment) string {
	if run.IsTodo() {
		return mercury.ComposeTodoPrompt(run.Name, run.Task, newRepo, attachmentDescriptors(atts))
	}
	// The stored snapshot is shared by every repo of the sweep; WHICH commit each repo was last examined
	// against is per repo, so it is appended here rather than baked into the snapshot.
	return run.Prompt + x.repoScope(run, repoID)
}

// repoScope renders the "last examined stand" addendum for one repo of an automatic run: per axiom the
// commit it was checked against, so the agent looks only at what came after it. Empty when nothing is
// recorded yet AND the run has no axioms; otherwise it explicitly names the never-examined axioms as
// full-repository work, which is what makes the incremental instruction actionable instead of a wish.
func (x *runExecutor) repoScope(run runs.Run, repoID string) string {
	if len(run.AxiomIDs) == 0 {
		return ""
	}
	recorded := x.s.axiomChecks.ForRepo(repoID)
	checked := make(map[string]mercury.LastCheck, len(recorded))
	for id, c := range recorded {
		at := ""
		if !c.At.IsZero() {
			at = c.At.Format("2006-01-02")
		}
		checked[id] = mercury.LastCheck{Commit: c.Commit, At: at}
	}
	axioms := make([]mercury.RunAxiom, 0, len(run.AxiomIDs))
	for _, id := range run.AxiomIDs {
		axioms = append(axioms, mercury.RunAxiom{ID: id})
	}
	return mercury.RepoScopeSection(axioms, checked)
}

// loadedAttachment is one of a ToDo's media, read from the passive pool and ready to drop into a
// workspace: its bytes, its workspace-relative destination, and the metadata the prompt references.
type loadedAttachment struct {
	rel  string
	data []byte
	meta runs.Attachment
}

// loadAttachments reads a ToDo's media from the pool once per execution. An unreadable blob is a
// non-blocking skip (logged, left out) rather than a run-stopping error — per the runner's Laufregeln.
func (x *runExecutor) loadAttachments(run runs.Run) []loadedAttachment {
	if x.s.attachments == nil {
		return nil
	}
	out := make([]loadedAttachment, 0, len(run.Attachments))
	for _, a := range run.Attachments {
		data, err := x.s.attachments.Get(run.ID, a.ID)
		if err != nil {
			log.Printf("devlabd: run %s attachment %s (%s) unreadable — skipping: %v", run.ID, a.ID, a.Filename, err)
			continue
		}
		out = append(out, loadedAttachment{rel: mercury.TodoAttachmentRel(a.Filename), data: data, meta: a})
	}
	return out
}

// attachmentDescriptors projects the loaded attachments to the prompt descriptors — so only media that
// was actually read (and will actually be present in the workspace) is announced to the agent.
func attachmentDescriptors(atts []loadedAttachment) []mercury.TodoAttachment {
	out := make([]mercury.TodoAttachment, 0, len(atts))
	for _, a := range atts {
		out = append(out, mercury.TodoAttachment{Filename: a.meta.Filename, MIME: a.meta.MIME})
	}
	return out
}

// writeWorkspaceAttachments materializes a ToDo's media into the agent's workspace (under
// mercury.TodoAttachmentDir) so the agent can open them, and returns a cleanup that removes them again
// BEFORE anything is committed — the media is CONTEXT, never part of the change set. A workspace is
// disposable (the next run's CleanWorktree clears any leftover), so cleanup is best-effort.
func writeWorkspaceAttachments(ex workspace.Executor, wt string, atts []loadedAttachment) (func(), error) {
	written := make([]string, 0, len(atts))
	cleanup := func() {
		for _, rel := range written {
			_ = ex.DeleteFile(wt, rel)
		}
	}
	for _, a := range atts {
		if err := ex.WriteFileBytes(wt, a.rel, a.data); err != nil {
			cleanup() // roll back partial writes so nothing dangles into the commit
			return func() {}, err
		}
		written = append(written, a.rel)
	}
	return cleanup, nil
}

// runAgent materializes a ToDo's attachments into the workspace, runs the claude CLI, and removes the
// attachments again as it returns (the deferred cleanup runs the instant the agent finishes, BEFORE
// executeRepo commits) — so the media reaches the agent yet never leaks into the PR. For an auto run
// (no attachments) it is exactly the plain agent call.
func (x *runExecutor) runAgent(actx context.Context, ex workspace.Executor, wt, prompt, permMode string, t agentTuning, atts []loadedAttachment, onLine func([]byte)) ([]byte, error) {
	cleanup, err := writeWorkspaceAttachments(ex, wt, atts)
	if err != nil {
		return nil, fmt.Errorf("Medien bereitstellen: %w", err)
	}
	defer cleanup()
	// Stream when a live sink is present (a real run) and streaming is enabled — so the agent can be
	// followed as it works. Otherwise the plain buffered call. resultEvent() reconciles both wire formats.
	if onLine != nil && streamEnabled() {
		return ex.AgentStream(actx, wt, onLine, streamAgentArgs(prompt, permMode, t)...)
	}
	return ex.Agent(actx, wt, agentArgs(prompt, permMode, t)...)
}

// liveSaver persists the in-progress result. Boundary events (a step starting/ending, a repo starting)
// force an immediate save; the streaming hot path — a line of agent output — uses throttled(), which
// coalesces to at most one save per interval so a chatty agent never thrashes the disk.
type liveSaver struct {
	do   func()
	last time.Time
}

func (l *liveSaver) force() {
	l.last = time.Now()
	l.do()
}

func (l *liveSaver) throttled() {
	const interval = 1200 * time.Millisecond
	if time.Since(l.last) < interval {
		return
	}
	l.force()
}

// agentStep renders the long agent stage (analyze/implement) as a LIVE step: a running step whose log
// grows with the agent's streaming transcript, finalized to the agent's report on success or the error
// text on failure. The fast git stages record completed steps directly (via executeRepo's step()).
type agentStep struct {
	rr    *runs.RepoResult
	saver *liveSaver
	idx   int
	tr    transcript
	// Live token accounting for THIS agent invocation. Claude streams per-turn usage on its assistant
	// events (repeated once per content block, all under one message id — see streamUsage), so byMsg
	// dedupes by id and the live total is the sum over distinct messages, folded into rr as it climbs.
	// applied is exactly what this invocation has already added to rr, so settle() can swap the live
	// estimate for the authoritative result-event total without double-counting.
	byMsg   map[string]usage
	applied usage
}

func beginAgentStep(rr *runs.RepoResult, saver *liveSaver, name string) *agentStep {
	rr.Steps = append(rr.Steps, runs.Step{Name: name, Running: true, At: time.Now().UTC()})
	saver.force()
	return &agentStep{rr: rr, saver: saver, idx: len(rr.Steps) - 1, byMsg: map[string]usage{}}
}

// onProgress folds one line of streamed agent output into the running step — both its transcript and its
// live token counters — and re-saves (throttled) whenever either moved, so a watcher sees the tokens climb.
func (a *agentStep) onProgress(line []byte) {
	changed := a.tr.push(line)
	if changed {
		a.rr.Steps[a.idx].Log = a.tr.clipped()
	}
	if a.foldUsage(line) {
		changed = true
	}
	if changed {
		a.saver.throttled()
	}
}

// foldUsage climbs the repo result's token/turn counters from one streamed assistant event, so the
// Tokenverbrauch updates live during the run rather than jumping only when the agent finishes. Usage is
// deduped by message id (each turn recurs once per content block with identical usage), and rr is moved
// by the delta over what this invocation already applied. Cost is not carried per-turn — it lands in
// settle() from the final result event. Returns true when the live total actually moved.
func (a *agentStep) foldUsage(line []byte) bool {
	id, u, ok := streamUsage(line)
	if !ok {
		return false
	}
	a.byMsg[id] = u // overwrite, never add: the same id repeats once per content block
	sum := usage{turns: len(a.byMsg)}
	for _, v := range a.byMsg {
		sum.in += v.in
		sum.out += v.out
	}
	if sum == a.applied {
		return false
	}
	a.rr.InputTokens += sum.in - a.applied.in
	a.rr.OutputTokens += sum.out - a.applied.out
	a.rr.NumTurns += sum.turns - a.applied.turns
	a.applied = sum
	return true
}

// settle reconciles the repo result's token/cost totals to the AUTHORITATIVE figures from the final
// result event, replacing whatever live estimate foldUsage accumulated for this invocation (so the two
// never double-count) and bringing in cost, which the per-turn stream does not carry.
func (a *agentStep) settle(final []byte) {
	u := parseClaudeUsage(final)
	a.rr.InputTokens += u.in - a.applied.in
	a.rr.OutputTokens += u.out - a.applied.out
	a.rr.NumTurns += u.turns - a.applied.turns
	a.rr.CostUSD += u.cost - a.applied.cost
	a.applied = u
}

func (a *agentStep) finish(report string) {
	s := &a.rr.Steps[a.idx]
	s.Running, s.Status, s.Log = false, runs.StepOK, clip(report)
	a.saver.force()
}

func (a *agentStep) fail(logtxt string) {
	s := &a.rr.Steps[a.idx]
	s.Running, s.Status, s.Log = false, runs.StepFailed, clip(logtxt)
	a.saver.force()
}

// failBudget fails the step on a time-budget overrun WITHOUT discarding the streamed transcript. That
// partial transcript is exactly "what was reached until then" (req 4), so it stays as the step's Bericht
// under a heading instead of being clobbered by the raw kill error ("signal: killed") — which is the very
// technical text req 4 forbids surfacing. The honest "Zeitbudget überschritten (<value>)" naming is carried
// by the repo-level Error shown right beside this step (budgetAwareError), so it is deliberately not
// repeated here. An empty transcript (nothing streamed before the cap fired) leaves the log empty — that
// banner alone then states the overrun.
func (a *agentStep) failBudget() {
	s := &a.rr.Steps[a.idx]
	s.Running, s.Status = false, runs.StepFailed
	if tr := a.tr.clipped(); tr != "" {
		s.Log = clip("Bis dahin erreicht:\n\n" + tr)
	}
	a.saver.force()
}

// runAgentLive runs the agent as a live step `name` on rr, streaming its transcript AND its climbing
// token counters into rr as it works, then settling the token/cost totals to the authoritative final
// result event. On a usage-limit stop it leaves the step running and the repo unrecorded (it retries on
// resume); on error it fails the step — keeping the partial transcript when the failure is this repo's own
// time-budget overrun (req 4); on success it finalizes the step to the report.
func (x *runExecutor) runAgentLive(actx context.Context, ex workspace.Executor, wt, prompt, permMode, name string, t agentTuning, atts []loadedAttachment, rr *runs.RepoResult, saver *liveSaver) (lim repoSignal, err error) {
	ag := beginAgentStep(rr, saver, name)
	out, aerr := x.runAgent(actx, ex, wt, prompt, permMode, t, atts, ag.onProgress)
	final := resultEvent(out)
	if l := detectLimit(final, aerr); l.limited {
		// Leave the step running; the repo is not recorded (it retries on resume) — so the live token
		// estimate folded so far is discarded with it, not settled, and never double-counts on resume.
		return l, aerr
	}
	// settle FIRST so the forced save in finish/fail persists the authoritative token/cost totals, not
	// the mid-stream estimate. On error the tokens actually spent on the failed invocation are recorded.
	ag.settle(final)
	if aerr != nil {
		// A time-budget overrun — THIS repo's own cap fired (cause errBudgetExceeded, told apart from the
		// whole-sweep cap and a deliberate abort, which cancel with different causes) — keeps the partial
		// transcript as its report; every other error fails plainly with its message.
		if errors.Is(context.Cause(actx), errBudgetExceeded) {
			ag.failBudget()
		} else {
			ag.fail(agentError(aerr))
		}
		return repoSignal{}, aerr
	}
	ag.finish(parseClaudeResult(final).Output)
	return repoSignal{}, nil
}

// devCheckpointInterval bounds how much freshly-committed work an interruption can leave unpublished:
// while the agent implements (routinely committing on its own, sometimes for a long time), the dev branch
// is pushed to origin at most this far apart. Short enough that a crash costs minutes, not hours (req 3).
const devCheckpointInterval = 2 * time.Minute

// startDevCheckpoint publishes the dev branch to origin as a best-effort BACKUP whenever it advances, on a
// ticker, so an interruption during a long implement step costs at most the work since the last checkpoint
// — never the whole run (req 3). It is NON-fatal by design: a failed checkpoint (a network blip, or the
// remote briefly ahead) is logged and retried on the next tick; the authoritative end-of-repo push still
// gates the delivery. The push is a plain fast-forward of the dev branch (the repo is locked, so origin/
// mercury-dev only ever moves under this run). The returned stop is idempotent and BLOCKS until the ticker
// goroutine has exited, so a checkpoint can never race the final atomic publish. It is only ever called
// sequentially from the executeRepo goroutine (explicitly, then via defer), so it needs no lock.
func (x *runExecutor) startDevCheckpoint(ctx context.Context, ex workspace.Executor, wt, token, devBranch string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(devCheckpointInterval)
		defer t.Stop()
		var last string
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				tip, err := ex.RevParse(ctx, wt, "HEAD")
				if err != nil || tip == "" || tip == last {
					continue // nothing new committed since the last checkpoint
				}
				if _, err := ex.PushRefs(ctx, wt, token, false, devBranch); err != nil {
					log.Printf("devlabd: dev checkpoint push (%s) failed, retrying next tick: %v", devBranch, err)
					continue
				}
				last = tip
			}
		}
	}()
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		close(stop)
		<-done
	}
}

// deploy hands a PREBUILT artifact to the root wrapper, which INSTALLS ONLY — it never builds (Finding
// C). env picks the target: "dev" installs the artifact to this box's local service and restarts the
// local unit; "prod" ships the artifact to the prod VPS (over a root-held forced-command SSH key) where
// the receiver installs+restarts. The prod TARGET host lives server-side (/etc/devlab/prod-target),
// never here — an artifact dir the runner built, plus the repo name and env, are the only args, so a
// compromised runner can neither build-as-root nor redirect a deploy to a foreign host. The wrapper
// re-canonicalizes artifactDir with realpath and requires it under /var/lib/devlab/workspaces/.
func (x *runExecutor) deploy(ctx context.Context, repo model.Repo, artifactDir, env string) (string, error) {
	cmd := exec.CommandContext(ctx, "sudo", "-n", deployWrapper, repo.Name, artifactDir, env)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("devlab-deploy %s (%s): %w", repo.Name, env, err)
	}
	return string(out), nil
}

// buildArtifact compiles repo's deployable artifact IN ITS WORKSPACE, as the UNPRIVILEGED workspace
// OWNER (via the pinned per-user devlab-exec artifact-build verb) — NEVER root (Finding C: a build hook
// would run as root and could read the root-only prod deploy key) and never the devlabd service user
// (which cannot write the runs-user-owned workspace). The privileged wrapper only ever INSTALLS the
// prebuilt result. The artifact is <wt>/.mercury-artifact (Go daemon binary + built web SPA in web/); its
// path resolves under /var/lib/devlab/workspaces/, which the root wrapper re-checks with realpath before
// installing. The recipe (npm + go build, mirroring .sxgate/preview.conf) lives in the artifact-build
// verb; a build failure is non-fatal for a dev-deploy (executeRepo continues to push).
func (x *runExecutor) buildArtifact(ctx context.Context, wt string, repo model.Repo) (string, error) {
	ex := workspace.Executor{User: x.user, PerUser: true}
	// Build AS THE WORKSPACE OWNER via the pinned per-user devlab-exec wrapper: the runs-user owns the
	// workspace, and the devlabd service user that runs THIS process cannot write it — and root must
	// never build (Finding C). The recipe (npm + CGO_ENABLED=0 go build → <wt>/.mercury-artifact with
	// the daemon binary + web/) lives in the artifact-build verb.
	if out, err := ex.ArtifactBuild(ctx, wt); err != nil {
		return "", fmt.Errorf("Artefakt bauen (%s): %v: %s", repo.ID, err, clip(out))
	}
	return filepath.Join(wt, ".mercury-artifact"), nil
}

// prodDeployMerged ships a MERGED run PR to prod. It materializes the merged default branch in the
// runner's workspace (Ensure + Lock + ResetToRemote), builds the artifact UNPRIVILEGED (Finding C),
// then hands it to the deploy wrapper with env=prod (which ships the prebuilt artifact to the VPS and
// installs+restarts there). The self repo is fine here — this restarts the VPS devlabd, not the dev
// runner. A nil error means prod is live on the merged code; Maintain then untracks the PR.
func (x *runExecutor) prodDeployMerged(ctx context.Context, token string, p runs.PendingPR) (string, error) {
	name := repoNameOf(p.Repo)
	repo := model.Repo{ID: name, Name: name, FullName: p.Repo}

	if _, err := x.s.workspaces.Ensure(ctx, x.user, repo.ID, repo.FullName, token, true); err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	unlock, err := x.s.workspaces.Lock(x.user, repo.ID)
	if err != nil {
		return "", fmt.Errorf("lock: %w", err)
	}
	defer unlock()
	branch, err := github.DefaultBranch(ctx, token, repo.FullName)
	if err != nil || branch == "" {
		return "", fmt.Errorf("default branch: %v", err)
	}
	wt, err := x.s.workspaces.Path(x.user, repo.ID)
	if err != nil {
		return "", fmt.Errorf("workspace-Pfad: %w", err)
	}
	ex := workspace.Executor{User: x.user, PerUser: true}
	if err := ex.ResetToRemote(ctx, wt, token, branch); err != nil {
		return "", fmt.Errorf("auf %s zurücksetzen: %w", branch, err)
	}
	artifactDir, err := x.buildArtifact(ctx, wt, repo)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	return x.deploy(ctx, repo, artifactDir, "prod")
}

func fileExists(p string) bool { fi, err := os.Stat(p); return err == nil && !fi.IsDir() }
func dirExists(p string) bool  { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// Maintain reconciles tracked run PRs each tick.
//
// report/pr mode behaves EXACTLY as before (Finding A): a PR still inside its auto-merge window is
// skipped BEFORE any GitHub call (shouldCheckPR), so those modes make ZERO extra GitHub calls; only an
// OVERDUE PR is fetched and, if still open, auto-merged, and a merged/closed one is untracked.
//
// full mode additionally detects merges (human OR auto) to drive a PROD-deploy, but THROTTLES the
// per-PR reads: an in-window PR is re-fetched at most once per recheck interval (default 5m, tracked
// via PendingPR.LastChecked), so the sweep never GETs every tracked PR every 30s tick and exhausts the
// rate budget. A merged PR is shipped to prod; the PR is untracked only on a SUCCESSFUL prod-deploy, so
// a failed deploy simply retries next eligible tick (never re-merges) — idempotent via untrack-on-
// success rather than a fragile persisted flag.
func (x *runExecutor) Maintain(ctx context.Context) {
	token, err := x.token()
	if err != nil {
		return
	}
	if prs, err := x.s.runPRs.List(); err == nil && len(prs) > 0 {
		x.maintainMergePRs(ctx, token, prs)
	}
	// Prune the branches those merges made redundant. This runs even when no PR is pending right now, so
	// the accumulated backlog of already-merged delivery branches is still swept away (req 25) — the
	// persistent dev branch always exempt.
	x.pruneMergedDeliveryBranches(ctx, token)
}

// maintainMergePRs auto-merges due PRs in creation order (per repo) and, in full mode, prod-deploys the
// merged ones. Split out of Maintain so the branch-prune sweep can run independently of whether any PR is
// pending this tick.
func (x *runExecutor) maintainMergePRs(ctx context.Context, token string, prs []runs.PendingPR) {
	now := time.Now()
	recheck := prRecheck()

	// Auto-merge STRICTLY in creation order, per repo. Every run branches off the default branch, so a
	// younger PR that lands first turns the older one — written against the same files — into a conflict
	// that then never merges on its own. Sorting alone is not enough: the older PR must also BLOCK the
	// younger one while it is still open, so a stuck PR halts its repo's queue instead of letting the
	// rest overtake it. Merging is the only gated action; observing a merge someone else performed (and
	// the prod-deploy that follows) stays free, so a manually merged younger PR still ships.
	sort.SliceStable(prs, func(i, j int) bool { return prs[i].CreatedAt.Before(prs[j].CreatedAt) })
	blocked := map[string]bool{} // repo → an older PR is still open, so no younger one may auto-merge

	for _, p := range prs {
		if p.Blocked {
			// A delivery blocked on a permanent prod-deploy failure waits for an EXPLICIT resume — it is
			// never re-fetched or retried, and (being already merged) it does not gate the merge queue, so
			// one broken repo can't hold up the others.
			continue
		}
		if !shouldCheckPR(x.mode, now, p.MergeBy, p.LastChecked, recheck) {
			blocked[p.Repo] = true // still open and unexamined → the queue behind it waits
			continue               // report/pr: within window → not touched. full: throttled between rechecks.
		}
		if x.mode == "full" {
			_ = x.s.runPRs.Touch(p.Repo, p.Number, now) // stamp the recheck up front (throttle even on error)
		}
		cur, err := x.fetchPR(ctx, token, p.Repo, p.Number)
		if err != nil {
			blocked[p.Repo] = true // unknown state → do not let a younger PR of this repo overtake it
			continue               // transient; retry next eligible tick
		}
		action := decidePR(x.mode, cur, !now.Before(p.MergeBy))
		if action == prMerge && blocked[p.Repo] {
			log.Printf("devlabd: %s#%d is due but an older Mercury PR of %s is still open — merging in creation order",
				p.Repo, p.Number, p.Repo)
			blocked[p.Repo] = true
			continue
		}
		if cur.State == "open" && action != prMerge {
			blocked[p.Repo] = true // still open (in-window) → younger PRs of this repo keep waiting
		}
		switch action {
		case prUntrack:
			// Mirror the outcome onto the delivery ledger so LatestOpen (the stacked-PR base) stays honest:
			// a merged PR closes its delivery as merged, a closed-without-merge one as closed.
			if cur.Merged {
				x.markDelivered(p, true, false)
				x.markDelivery(p.Repo, p.Number, runs.DeliveryMerged)
			} else {
				x.markDelivery(p.Repo, p.Number, runs.DeliveryClosed)
			}
			_ = x.s.runPRs.Remove(p.Repo, p.Number) // merged (report/pr) or closed → stop tracking
		case prMerge:
			// One merge per repo per tick, whatever the outcome: a failed merge leaves the older PR open
			// (its queue must wait), and a successful one has just moved the default branch — GitHub needs
			// a moment to recompute mergeability for the rest, so they go on the next tick.
			blocked[p.Repo] = true
			if err := x.mergePR(ctx, token, p.Repo, p.Number); err != nil {
				log.Printf("devlabd: auto-merge %s#%d failed (will retry): %v", p.Repo, p.Number, err)
				continue
			}
			log.Printf("devlabd: auto-merged %s#%d (run %s)", p.Repo, p.Number, p.RunID)
			x.markDelivery(p.Repo, p.Number, runs.DeliveryMerged)
			x.markDelivered(p, true, false)
			if x.mode != "full" {
				_ = x.s.runPRs.Remove(p.Repo, p.Number) // report/pr: merged and done
			}
			// full: keep tracked — the next eligible tick sees it merged and prod-deploys it.
		case prDeploy:
			x.markDelivery(p.Repo, p.Number, runs.DeliveryMerged) // the PR is merged; prod-deploy follows
			x.deployMergedPR(ctx, token, p, now)
		case prNone:
			// full-mode recheck: still open within its window → nothing to do yet.
		}
	}
}

// pruneMergedDeliveryBranches deletes the per-delivery snapshot branch of every delivery whose work has
// reached the default branch — i.e. whose ledger status is merged. Driven by the ledger, not by git commit
// identity, so a squash- or rebase-merge (whose commits do not appear verbatim on the default branch) is
// pruned all the same: what counts is that the content arrived, which is exactly what "merged" records.
//
// The persistent dev branch (mercury-dev) is EXEMPT — it is never a delivery's snapshot branch, and the
// devBranchName guard makes that explicit. It is the only durable backup of built-but-unmerged work and
// must never be deleted. Each branch is deleted at most once (the BranchPruned flag), so a steady-state
// ledger costs one in-memory scan and no GitHub calls.
func (x *runExecutor) pruneMergedDeliveryBranches(ctx context.Context, token string) {
	if x.s == nil || x.s.deliveries == nil {
		return
	}
	all, err := x.s.deliveries.List()
	if err != nil {
		return
	}
	dev := devBranchName()
	for _, d := range all {
		if d.Status != runs.DeliveryMerged || d.BranchPruned || d.Branch == "" || d.Branch == dev {
			continue
		}
		if err := x.deleteBranch(ctx, token, d.Repo, d.Branch); err != nil {
			log.Printf("devlabd: could not prune merged delivery branch %s@%s (will retry): %v", d.Repo, d.Branch, err)
			continue // leave BranchPruned false → retried next tick
		}
		_ = x.s.deliveries.Update(d.ID, func(dl *runs.Delivery) { dl.BranchPruned = true })
		log.Printf("devlabd: pruned merged delivery branch %s@%s (delivery %s)", d.Repo, d.Branch, d.ID)
	}
}

// deleteBranch dispatches to the injected seam when present (tests), else the real GitHub ref-delete.
func (x *runExecutor) deleteBranch(ctx context.Context, token, fullName, branch string) error {
	if x.deleteBranchFn != nil {
		return x.deleteBranchFn(ctx, token, fullName, branch)
	}
	return github.DeleteBranch(ctx, token, fullName, branch)
}

// deployMergedPR ships one merged run PR to prod, distinguishing permanent from transient failure so a
// broken delivery blocks (waits for a resume) instead of retrying forever. The flow:
//   - shipToProd gate: a not-to-be-delivered repo, or one with no deploy target, is untracked with NO
//     attempt (there is nothing to try).
//   - success: untrack (idempotent) and mark the delivery prod-live.
//   - transient failure (network / target briefly unreachable): keep tracked, retry next tick, DO NOT
//     count it — a passing network hiccup must never exhaust the block budget.
//   - permanent failure: count one attempt atomically; once DeployAttempts reaches maxDeployAttempts the
//     PR is blocked with a reason that names the service and target.
func (x *runExecutor) deployMergedPR(ctx context.Context, token string, p runs.PendingPR, now time.Time) {
	name := repoNameOf(p.Repo)
	if ship, why := x.shipToProd(name); !ship {
		x.markDelivered(p, true, false) // merged; there is simply nothing to ship
		_ = x.s.runPRs.Remove(p.Repo, p.Number)
		log.Printf("devlabd: %s#%d gemerged — %s → untracked (kein Deploy-Versuch)", p.Repo, p.Number, why)
		return
	}
	depLog, derr := x.runProdDeploy(ctx, token, p)
	if derr == nil {
		x.markDelivered(p, true, true)
		_ = x.s.runPRs.Remove(p.Repo, p.Number) // idempotent untrack-on-success
		log.Printf("devlabd: prod-deployed %s#%d (run %s)", p.Repo, p.Number, p.RunID)
		return
	}
	if deployTransient(depLog, derr) {
		log.Printf("devlabd: prod-deploy %s#%d transient failure (will retry, no count): %v\n%s", p.Repo, p.Number, derr, clip(depLog))
		return // keep tracked; retry the DEPLOY next eligible tick (never re-merge, never count)
	}
	// Permanent failure: count this attempt; block once the threshold is reached.
	reason := deployBlockReason(name, depLog, derr)
	limit := maxDeployAttempts()
	attempts := 0
	blockedNow := false
	_, _ = x.s.runPRs.Update(p.Repo, p.Number, func(pr *runs.PendingPR) {
		pr.DeployAttempts++
		attempts = pr.DeployAttempts
		if pr.DeployAttempts >= limit {
			pr.Blocked, pr.BlockedReason, pr.BlockedAt = true, reason, now
			blockedNow = true
		}
	})
	if blockedNow {
		log.Printf("devlabd: prod-deploy %s#%d BLOCKED after %d attempt(s) — %s", p.Repo, p.Number, attempts, reason)
		return
	}
	log.Printf("devlabd: prod-deploy %s#%d permanent failure %d/%d (retry, then block): %v\n%s",
		p.Repo, p.Number, attempts, limit, derr, clip(depLog))
}

// markDelivered records on the run that its PR reached the next rung of the delivery ladder, so the
// surface can say "merged" / "prod-live" instead of stopping at "PR offen". Patch, not Mutate: this is
// observed delivery state, not a config edit. It only touches a LastResult that still points at THIS
// PR — a newer execution has its own ladder and must not inherit an older PR's merge.
func (x *runExecutor) markDelivered(p runs.PendingPR, merged, prod bool) {
	if x.s.runs == nil {
		return
	}
	_, _ = x.s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		for i := range cur {
			if cur[i].ID != p.RunID || cur[i].LastResult == nil || cur[i].LastResult.PRUrl != p.URL {
				continue
			}
			if merged {
				cur[i].LastResult.Merged = true
			}
			if prod {
				cur[i].LastResult.ProdDeployed = true
			}
		}
		return cur, nil
	})
}

// prAction is Maintain's decision for one tracked PR after it has been fetched.
type prAction int

const (
	prNone    prAction = iota // leave it tracked, do nothing this tick
	prMerge                   // overdue and still open → auto-merge
	prDeploy                  // merged → prod-deploy then untrack (full mode only)
	prUntrack                 // merged (report/pr) or closed without merge → stop tracking
)

const defaultPRRecheck = 5 * time.Minute

// prRecheck is the minimum spacing between full-mode merge-detection reads of one in-window PR.
// DEVLAB_RUNS_PR_RECHECK overrides; a non-positive value keeps the default.
func prRecheck() time.Duration {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_PR_RECHECK")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultPRRecheck
}

// shouldCheckPR reports whether Maintain may spend a GitHub read on this tracked PR now. An OVERDUE PR
// is always checked (to auto-merge / detect a merge) in every mode — historical behavior. report/pr
// mode NEVER touches an in-window PR (zero extra calls). full mode rechecks an in-window PR too, but at
// most once per recheck interval, so a large tracked set can't exhaust the rate budget (Finding A).
func shouldCheckPR(mode string, now, mergeBy, lastChecked time.Time, recheck time.Duration) bool {
	if !now.Before(mergeBy) {
		return true // overdue
	}
	if mode != "full" {
		return false // report/pr: in-window PRs are never fetched
	}
	return lastChecked.IsZero() || now.Sub(lastChecked) >= recheck
}

// decidePR maps a freshly-fetched PR (+ whether it is past its auto-merge deadline) to an action.
// report/pr NEVER deploy: a merged or closed PR is simply untracked. full mode turns a merged PR (by a
// human OR the auto-merge) into a prod-deploy, auto-merges an overdue still-open PR, and leaves an
// in-window still-open PR alone.
func decidePR(mode string, pr github.PullRequest, overdue bool) prAction {
	if pr.Merged {
		if mode == "full" {
			return prDeploy
		}
		return prUntrack
	}
	if pr.State != "open" {
		return prUntrack // closed without merging
	}
	if overdue {
		return prMerge
	}
	return prNone
}

func (x *runExecutor) runnerIdentity() (string, int64) {
	if x.s.links == nil {
		return "", 0
	}
	l, err := x.s.links.Get(x.tokenUser)
	if err != nil || l == nil {
		return "", 0
	}
	return l.GHLogin, l.GHID
}

type usage struct {
	in, out int
	cost    float64
	turns   int
}

// parseClaudeUsage extracts token counts + cost from the claude CLI's --output-format json.
func parseClaudeUsage(out []byte) usage {
	var raw struct {
		TotalCost float64 `json:"total_cost_usd"`
		NumTurns  int     `json:"num_turns"`
		Usage     struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(out, &raw)
	return usage{
		in:    raw.Usage.InputTokens + raw.Usage.CacheCreationInputTokens + raw.Usage.CacheReadInputTokens,
		out:   raw.Usage.OutputTokens,
		cost:  raw.TotalCost,
		turns: raw.NumTurns,
	}
}

// agentTuning selects the model + effort tier for one agent invocation, taken from the run/todo. Empty
// fields fall back to the runner defaults (opus / max), so a record written before these fields existed
// behaves exactly as before.
type agentTuning struct {
	model  string
	effort string
}

func tuningFor(run runs.Run) agentTuning { return agentTuning{model: run.Model, effort: run.Effort} }

// ultracodeDirective is folded into the runner's system prompt when a run picks the "ultracode" effort:
// the maximal tier runs at max reasoning AND asks the agent to decompose and verify via multi-agent
// orchestration, trading token economy for thoroughness. It is opt-in per run — the default never sets it.
const ultracodeDirective = "Operate in ultracode mode: decompose the task and use multi-agent workflow " +
	"orchestration, adversarially verifying your work before committing. Favour correctness and " +
	"completeness over token economy."

// resolve turns the (possibly empty) tuning into the concrete claude CLI model + effort and the system
// preamble. "ultracode" is not a native --effort level, so it maps to max plus the ultracode directive;
// every other empty case falls back to the historical opus / max the runner has always used.
func (t agentTuning) resolve() (model, effort, preamble string) {
	// Re-guard at the argv boundary: this feeds a bypassPermissions CLI, so a model/effort that somehow
	// reached the store unvalidated (a hand-edited runs.json, a future writer that skips validateTuning)
	// still cannot put an arbitrary token onto the command line — a non-conforming value falls back to the
	// safe default rather than being forwarded verbatim.
	model = t.model
	if model == "" || !runModelRe.MatchString(model) {
		model = "opus"
	}
	effort = t.effort
	if effort != "" && !runEffortAllowed[effort] {
		effort = ""
	}
	preamble = runnerPreamble
	switch effort {
	case "ultracode":
		effort = "max"
		preamble = runnerPreamble + "\n\n" + ultracodeDirective
	case "":
		effort = "max"
	}
	return model, effort, preamble
}

func agentArgs(prompt, mode string, t agentTuning) []string {
	model, effort, preamble := t.resolve()
	return []string{
		"-p", prompt,
		"--output-format", "json",
		"--permission-mode", mode,
		"--model", model,
		"--effort", effort,
		"--append-system-prompt", preamble,
	}
}

func runPRBody(run runs.Run) string {
	return "Automatisch erzeugt vom Mercury-Lauf **" + run.Name + "**. Dieser PR bündelt die vom autonomen " +
		"Runner implementierten Änderungen gegen die Axiome dieses Laufs. Merge jederzeit möglich; ohne Merge " +
		"innerhalb der Frist wird automatisch gemergt.\n\n🤖 Mercury\n" + mercuryPRMarker
}

// mercuryPRMarker is the stable, language-independent fingerprint of a Mercury-created PR. It sits in the
// PR body as an HTML comment (invisible when rendered, untouched by the nightly translation pass), so a
// run can recognise its own still-open PRs WITHOUT relying on the branch name — the branch now follows the
// human <kind>/<description> convention and no longer carries a distinguishing prefix.
const mercuryPRMarker = "<!-- holistic-mercury-run -->"

func clip(s string) string {
	const max = 20000
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "\n…(gekürzt)"
	}
	return s
}

// runNow triggers a run immediately, detached from this request (it can take a long time). Returns at
// once; the UI polls the run's results. 503 when unconfigured, 409 when a run is already in progress.
func (s *Server) runNow(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeErr(w, http.StatusServiceUnavailable, "Ausführung ist nicht konfiguriert (DEVLAB_RUNS_MODE/DEVLAB_RUNS_USER)")
		return
	}
	id := r.PathValue("id")
	run, ok, err := s.runs.Get(id)
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
		return
	}
	// ?fresh=1 (or {"fresh":true}) explicitly discards any interrupted execution and starts over (req 4);
	// the default continues an interrupted execution if one exists (req 1). The optional body carries a
	// slot strategy (req 5): queue (einreihen) / defer (einen Vorgang zurückstellen) / overload (ein
	// zusätzlicher Platz nur für diese Ausführung).
	strategy, deferRunID, bodyFresh := decodeRunNowBody(r)
	q := r.URL.Query().Get("fresh")
	fresh := bodyFresh || q == "1" || strings.EqualFold(q, "true")
	who := actor(r)

	switch strategy {
	case "", "start":
		if plan, started := s.scheduler.FireNow(id, who, fresh); started {
			writeJSON(w, http.StatusOK, map[string]any{"started": true, "plan": plan})
			return
		}
		// Blocked. A run already executing is a plain conflict; anything else (cap / repo-busy / exclusive)
		// returns a decision the caller can act on — einreihen, zurückstellen, or überladen.
		block := s.scheduler.Admissibility(run)
		if block.Reason == runs.AdmitRunning {
			writeErr(w, http.StatusConflict, "Dieser Lauf läuft bereits")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "decision": s.buildStartDecision(block)})
	case "queue":
		if plan, started := s.scheduler.FireNow(id, who, fresh); started {
			writeJSON(w, http.StatusOK, map[string]any{"started": true, "plan": plan})
			return
		}
		s.markRunDueNow(id) // leave it due → the scheduler starts it at the next free slot
		writeJSON(w, http.StatusOK, map[string]any{"queued": true})
	case "overload":
		if plan, started := s.scheduler.StartOverload(id, who, fresh); started {
			writeJSON(w, http.StatusOK, map[string]any{"started": true, "overloaded": true, "plan": plan})
			return
		}
		writeErr(w, http.StatusConflict, "Überladen nicht möglich — zwei Vorgänge im selben Repository oder ein exklusiver Lauf bleiben ausgeschlossen")
	case "defer":
		if strings.TrimSpace(deferRunID) == "" {
			writeErr(w, http.StatusBadRequest, "deferRunId ist für die Strategie 'defer' erforderlich")
			return
		}
		if !s.scheduler.Defer(deferRunID) {
			writeErr(w, http.StatusConflict, "Der zurückzustellende Lauf ist nicht aktiv")
			return
		}
		if plan, started := s.scheduler.FireNow(id, who, fresh); started {
			writeJSON(w, http.StatusOK, map[string]any{"started": true, "deferred": deferRunID, "plan": plan})
			return
		}
		s.markRunDueNow(id) // the freed slot may be reclaimed by the deferred run's immediate resume; queue then
		writeJSON(w, http.StatusOK, map[string]any{"queued": true, "deferred": deferRunID})
	default:
		writeErr(w, http.StatusBadRequest, "unbekannte Strategie")
	}
}

// runActive reports the run executing in THIS process right now — its id, the live result id (once the
// executor mints it), and when it started — or null when nothing runs. It is the single source of truth
// the UI reads on mount (so a running run survives a page reload) and polls to follow a live run: it
// mirrors an actually-alive goroutine, hence correct across reloads and empty after a restart. Cheap (no
// scheme scan), so it is safe to poll frequently.
//
// Alongside it the endpoint returns `inflight`: the transparent list of every run the system is currently
// working — the executing one PLUS every run SUSPENDED mid-execution on the usage limit (waiting to
// resume). `active` stays the minimal projection existing consumers depend on; `inflight` is the enriched
// list the "Aktive Läufe" overview renders. One endpoint, two portioned views of the same truth — no
// parallel data path.
func (s *Server) runActive(w http.ResponseWriter, r *http.Request) {
	active := []runs.Activity{}
	if s.scheduler != nil {
		active = s.scheduler.Active()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active":   active,
		"inflight": s.assembleInFlight(active),
		"slots":    s.slotOverview(active), // capacity/used/free/overload + deferred runs with resume points
	})
}

// inFlightRun is one run the system is currently working: either EXECUTING right now (a live goroutine,
// state "executing") or SUSPENDED on the usage limit mid-execution (state "suspended", waiting for its
// window to reset). A portioned, read-only projection assembled for the "Aktive Läufe" overview so the UI
// can render a transparent list — which run, on which repo/step, how far, how much spent — without a
// follow-up fetch per run. Purely observational: nothing here drives scheduling or resume.
type inFlightRun struct {
	RunID   string `json:"runId"`
	RunName string `json:"runName"`
	Type    string `json:"type"`  // auto|todo
	State   string `json:"state"` // executing|suspended

	ResultID  string     `json:"resultId,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"` // execution start (executing)
	ResumeAt  *time.Time `json:"resumeAt,omitempty"`  // when a suspended run resumes
	Attempts  int        `json:"attempts,omitempty"`  // suspended: resume attempts so far

	CurrentRepo string `json:"currentRepo,omitempty"` // the repo in flight (executing)
	CurrentStep string `json:"currentStep,omitempty"` // the step running right now (executing)
	ReposDone   int    `json:"reposDone"`             // repos already completed this execution
	ReposTotal  int    `json:"reposTotal,omitempty"`  // known only for ToDos (exact target count)

	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
	NumTurns     int     `json:"numTurns"`
}

// assembleInFlight builds the transparent list of runs the system is currently working: EVERY run
// EXECUTING right now (from the live Activities — several may run concurrently) followed by every
// SUSPENDED run (paused on the usage limit or deferred). Each entry is enriched from its live result
// document so the overview shows the current repo/step, progress and spend at a glance. Read-only.
func (s *Server) assembleInFlight(active []runs.Activity) []inFlightRun {
	out := []inFlightRun{}
	if s.runs == nil {
		return out
	}
	all, err := s.runs.List()
	if err != nil {
		all = nil
	}
	byID := make(map[string]runs.Run, len(all))
	for _, r := range all {
		byID[r.ID] = r
	}
	executing := make(map[string]bool, len(active))

	// 1) Every run executing right now (concurrent).
	for _, a := range active {
		executing[a.RunID] = true
		e := inFlightRun{RunID: a.RunID, State: "executing", ResultID: a.ResultID}
		st := a.StartedAt
		e.StartedAt = &st
		if run, ok := byID[a.RunID]; ok {
			e.RunName = run.Name
			e.Type = string(runs.NormalizeType(run.Type))
			e.ReposTotal = todoRepoTotal(run)
		}
		s.enrichInFlight(&e, a.RunID, a.ResultID)
		out = append(out, e)
	}

	// 2) Every run suspended mid-execution (usage limit or deferred) — genuinely in flight, just paused.
	//    Never double-count one that is executing right now (a run cannot be both).
	for _, run := range all {
		if run.Suspended == nil || executing[run.ID] {
			continue
		}
		e := inFlightRun{
			RunID: run.ID, RunName: run.Name, State: "suspended",
			Type:       string(runs.NormalizeType(run.Type)),
			ResultID:   run.Suspended.ResultID,
			Attempts:   run.Suspended.Attempts,
			ReposTotal: todoRepoTotal(run),
		}
		resume := run.Suspended.ResumeAt
		e.ResumeAt = &resume
		s.enrichInFlight(&e, run.ID, run.Suspended.ResultID)
		out = append(out, e)
	}
	return out
}

// todoRepoTotal is the exact destination-repo count for a ToDo (its Targets). An automatic run's repo set
// is derived at execution time and not cheaply known here, so it returns 0 (unknown) and the UI shows
// only the completed count rather than a fabricated denominator.
func todoRepoTotal(run runs.Run) int {
	if run.IsTodo() {
		return len(run.TodoTargets())
	}
	return 0
}

// enrichInFlight fills an entry from its live result document: the repo currently in flight and its
// running step, how many repos are already done, and the running token/cost totals. A missing or
// unreadable result is non-fatal — the entry keeps its base fields so the overview still lists the run.
func (s *Server) enrichInFlight(e *inFlightRun, runID, resultID string) {
	if s.runResults == nil || resultID == "" {
		return
	}
	res, ok, err := s.runResults.Get(runID, resultID)
	if err != nil || !ok {
		return
	}
	e.ReposDone = len(res.Repos)
	e.InputTokens, e.OutputTokens = res.InputTokens, res.OutputTokens
	e.CostUSD, e.NumTurns = res.CostUSD, res.NumTurns
	if res.Live != nil {
		// The repo in flight has not settled into the execution totals yet, so add its live spend — the
		// overview then climbs as the current repo works, not only when it finishes and rolls up.
		e.InputTokens += res.Live.InputTokens
		e.OutputTokens += res.Live.OutputTokens
		e.CostUSD += res.Live.CostUSD
		e.NumTurns += res.Live.NumTurns
		e.CurrentRepo = res.Live.Repo
		// The step running right now is the last one still marked Running.
		for i := len(res.Live.Steps) - 1; i >= 0; i-- {
			if res.Live.Steps[i].Running {
				e.CurrentStep = res.Live.Steps[i].Name
				break
			}
		}
	}
}

// runCancel aborts a SPECIFIC run in progress (kill-switch). With concurrent runs the target is named by
// {id} — other live runs keep going.
func (s *Server) runCancel(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeErr(w, http.StatusServiceUnavailable, "Ausführung ist nicht konfiguriert")
		return
	}
	if !s.scheduler.Cancel(r.PathValue("id")) {
		writeErr(w, http.StatusConflict, "Dieser Lauf ist nicht aktiv")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// blockedDeployView is one blocked delivery, portioned for the surface: which service/PR, why it is
// blocked, how many permanent attempts it took, and since when. Read-only projection of the PR pool.
type blockedDeployView struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	URL       string    `json:"url"`
	RunID     string    `json:"runId"`
	Reason    string    `json:"reason"`
	Attempts  int       `json:"attempts"`
	BlockedAt time.Time `json:"blockedAt"`
}

// runDeploysBlocked lists the deliveries blocked on a permanent prod-deploy failure — the ones waiting for
// an explicit resume. Newest block first; an empty list, never null.
func (s *Server) runDeploysBlocked(w http.ResponseWriter, r *http.Request) {
	out := []blockedDeployView{}
	if s.runPRs != nil {
		prs, err := s.runPRs.List()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Auslieferungen konnten nicht gelesen werden")
			return
		}
		for _, p := range prs {
			if !p.Blocked {
				continue
			}
			out = append(out, blockedDeployView{
				Repo: p.Repo, Number: p.Number, URL: p.URL, RunID: p.RunID,
				Reason: p.BlockedReason, Attempts: p.DeployAttempts, BlockedAt: p.BlockedAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BlockedAt.After(out[j].BlockedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"blocked": out})
}

// runDeployResume clears the block on one delivery so Maintain retries its prod-deploy on the next tick,
// with the full attempt budget again. This is the ONLY way a blocked delivery moves — the block waits for
// exactly this deliberate action.
func (s *Server) runDeployResume(w http.ResponseWriter, r *http.Request) {
	if s.runPRs == nil {
		writeErr(w, http.StatusServiceUnavailable, "Auslieferungen-Store nicht verfügbar")
		return
	}
	var body struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Repo) == "" || body.Number <= 0 {
		writeErr(w, http.StatusBadRequest, "repo und number sind erforderlich")
		return
	}
	resumed := false
	found, err := s.runPRs.Update(body.Repo, body.Number, func(p *runs.PendingPR) {
		if !p.Blocked {
			return
		}
		p.Blocked = false
		p.BlockedReason = ""
		p.BlockedAt = time.Time{}
		p.DeployAttempts = 0 // fresh start — the full attempt budget again
		resumed = true
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Wiederaufnahme fehlgeschlagen")
		return
	}
	if !found || !resumed {
		writeErr(w, http.StatusNotFound, "Keine blockierte Auslieferung für dieses Repository/PR")
		return
	}
	log.Printf("devlabd: deploy %s#%d resumed by %s — retried on the next maintain tick", body.Repo, body.Number, actor(r))
	writeJSON(w, http.StatusOK, map[string]bool{"resumed": true})
}
