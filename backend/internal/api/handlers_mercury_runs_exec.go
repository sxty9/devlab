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
	defaultAgentTimeout = 60 * time.Minute // a full implement pass can be long
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

// agentTimeout caps ONE repo's agent pass. Sixty minutes covers an ordinary change, but not building a
// service from nothing — that hit the ceiling and was killed mid-implement, leaving an empty repo, no
// PR and no usable output. So the cap is configurable (DEVLAB_RUNS_AGENT_TIMEOUT); an explicit "0"
// removes it entirely, leaving the whole-sweep duration cap as the only bound.
func runAgentTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_AGENT_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return defaultAgentTimeout
}

// maxCostUSD is the cumulative-spend ceiling for one execution; once crossed, the sweep stops cleanly
// (remaining repos are left for the next scheduled run). 0 (the default) = no ceiling.
func maxCostUSD() float64 {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_MAX_COST_USD")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 0
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
	s.scheduler = runs.NewScheduler(s.runs, x, tick)
	log.Printf("devlabd: runs scheduler ENABLED — mode=%s user=%s tokenUser=%s automerge=%s tick=%s",
		mode, user, tokenUser, autoMerge, tick)
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

func (x *runExecutor) Execute(ctx context.Context, run runs.Run, trigger runs.Trigger, report func(resultID string)) (runs.ResultRef, error) {
	// Resume an execution suspended on the usage limit (same ResultID, skip the repos already done), or
	// start a fresh one. A resume that can't find its open result silently starts fresh.
	res, resuming := x.resumeOrNew(run)
	if !resuming {
		// Stamp origin only on a fresh execution: how it was triggered (autonomous vs a person) and the
		// run's author it acts for. A resume keeps the original stamp so provenance never rewrites.
		res.Trigger = trigger
		res.RequestedBy = run.CreatedBy
	}
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

	// Cap the whole sweep's wall-clock (belt-and-braces beside the per-repo timeout). A resume inherits
	// the remaining spend budget via the accumulated res.CostUSD, but a fresh duration budget.
	if d := maxRunDuration(); d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	costCeiling := maxCostUSD()

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

	// Per-ATTEMPT cost budget. The cost ceiling is a per-night cap, not cumulative: it measures spend
	// since this attempt began, so a carried-over run resumes with a fresh budget and always makes
	// progress (a cumulative cap would re-trip forever on the loaded res.CostUSD and deadlock).
	attemptStartCost := res.CostUSD
	carriedOver := false // true = stop early but DON'T finalise; the next scheduled fire continues this
	//                      same result (via the stranded-resume path), skipping the repos already done.

	for _, repo := range repos {
		if done[repo.ID] {
			continue // completed in an earlier attempt of this same execution
		}
		// Spend ceiling for THIS attempt: stop before starting another expensive repo. Carry over — the
		// remaining repos continue on the next scheduled run, not redone-from-scratch (which would
		// duplicate PRs and raise spend). The check is before a repo, so one repo can overshoot by its
		// own cost (soft cap).
		if costCeiling > 0 && res.CostUSD-attemptStartCost >= costCeiling {
			log.Printf("devlabd: run %s hit the per-run cost ceiling ($%.2f this attempt ≥ $%.2f) after %d repos — carrying the rest to the next run",
				run.ID, res.CostUSD-attemptStartCost, costCeiling, len(res.Repos))
			carriedOver = true
			break
		}
		if ctx.Err() != nil {
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
		rr, lim := x.executeRepo(ctx, run, repo, newRepoName[repo.ID] != "", x.promptFor(run, repo.ID, newRepoName[repo.ID], todoAtts), token, ghLogin, ghID, todoAtts, &res, saver)
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
	return runs.ResultRef{
		ResultID: res.ResultID, At: res.StartedAt, OK: overallOK, RepoCount: len(res.Repos),
		InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD,
		// A ToDo that opened PRs is not "done" yet — it awaits its merge to main (Maintain flips Done then).
		PRsOpen: anyOpenPR(res.Repos),
	}, nil
}

// anyOpenPR reports whether the finished sweep opened at least one pull request (a repo carrying a PR
// URL). It is the "did this execution leave work awaiting a merge" signal that keeps a ToDo in the
// active list until the main-merge is through, rather than checking it off the moment a PR is opened.
func anyOpenPR(repos []runs.RepoResult) bool {
	for _, rr := range repos {
		if rr.PRUrl != "" {
			return true
		}
	}
	return false
}

// resumeOrNew continues an interrupted execution, or mints a fresh one. Three interruption kinds resume
// the SAME result (so repos already done are skipped, never re-implemented into duplicate PRs):
//  1. a usage-limit suspension (run.Suspended points at the open result);
//  2. a crash/restart mid-run — no suspension, but a stranded result (unfinished, unsuspended) is on
//     disk. Without (2), a devlabd restart during a sweep makes the next fire redo every repo; and
//  3. an ORPHANED suspension — an execution that paused on the limit but whose run lost its Suspended
//     pointer (a restart between the two writes). FindResumable recovers it, so it no longer freezes
//     forever with its spend stranded (the freeze bug).
//
// A husk started under a DIFFERENT safety-ladder mode is NOT continued (a report husk marks repos
// "done" after mere analysis; resuming it in pr mode would skip implementing them). Such a husk is
// reaped — finalised as failed so it stops lingering — and a fresh execution starts.
func (x *runExecutor) resumeOrNew(run runs.Run) (runs.Result, bool) {
	fresh := func() runs.Result {
		start := time.Now()
		model, _, _ := tuningFor(run).resolve() // the engine this execution is minted with (re-stamped on resume, see consider)
		return runs.Result{RunID: run.ID, ResultID: runs.NewResultID(start), RunName: run.Name,
			Type: runs.NormalizeType(run.Type), Mode: x.mode, Model: model, Effort: run.Effort, StartedAt: start.UTC(),
			PromptHash: run.PromptHash, Prompt: run.Prompt}
	}
	consider := func(existing runs.Result) (runs.Result, bool, bool) {
		if existing.Mode != "" && existing.Mode != x.mode {
			x.reap(existing, fmt.Sprintf("Modus gewechselt (%s → %s) — Husk nicht fortgesetzt", existing.Mode, x.mode))
			return runs.Result{}, false, true // reaped: don't resume, fall through to fresh
		}
		// A resumed attempt is driven by the run's CURRENT tuning (executeRepo reads tuningFor(run) each
		// fire), so re-stamp the engine label to match — else a legacy husk shows no model, and one whose
		// tuning was edited while suspended keeps a stale label. Label-only; it steers no resume decision.
		m, _, _ := tuningFor(run).resolve()
		existing.Model, existing.Effort = m, run.Effort
		return existing, true, false
	}
	if run.Suspended != nil && run.Suspended.ResultID != "" {
		if existing, ok, _ := x.s.runResults.Get(run.ID, run.Suspended.ResultID); ok {
			if res, resume, reaped := consider(existing); resume {
				return res, true
			} else if reaped {
				return fresh(), false
			}
		}
	}
	if existing, ok := x.s.runResults.FindResumable(run.ID, time.Now().Add(-strandedWindow)); ok {
		if res, resume, _ := consider(existing); resume {
			return res, true
		}
	}
	return fresh(), false
}

// reap finalises an unresumable husk (mode-mismatched, or otherwise not to be continued) as a failed,
// FINISHED result so it stops showing as "suspended"/unfinished forever and cannot be picked up again.
// The work it already did remains in its stored report; only its open state is closed out.
func (x *runExecutor) reap(res runs.Result, reason string) {
	res.Suspended = false
	res.ResumeAt = nil
	res.FinishedAt = time.Now().UTC()
	res.OK = false
	res.Repos = append(res.Repos, runs.RepoResult{Repo: "-", OK: false, Error: "abgeschlossen (nicht fortgesetzt): " + reason})
	_ = x.s.runResults.Save(res)
	log.Printf("devlabd: reaped husk %s/%s — %s", res.RunID, res.ResultID, reason)
}

// suspend persists the partial execution as paused and returns a suspended ResultRef — UNLESS the resume
// budget is exhausted, in which case it finalizes the execution as failed and returns a normal ref (so
// the scheduler clears the suspension and stops retrying).
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

func (x *runExecutor) executeRepo(ctx context.Context, run runs.Run, repo model.Repo, isNewRepo bool, prompt, token, ghLogin string, ghID int64, atts []loadedAttachment, res *runs.Result, saver *liveSaver) (runs.RepoResult, repoSignal) {
	rr := runs.RepoResult{Repo: repo.ID, Running: true}
	res.Live = &rr // publish this repo as the in-flight one; the caller clears Live once it settles
	saver.force()
	// step records a COMPLETED stage and re-saves, so the fast git stages (push/pr/deploy) also surface
	// live as they land. The long agent stages instead use runAgentLive (a running step that streams).
	step := func(name, logtxt string, ok bool) {
		rr.Steps = append(rr.Steps, runs.Step{Name: name, OK: ok, Log: clip(logtxt), At: time.Now().UTC()})
		saver.force()
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
	var devCreated bool
	if err := retryInfra(ctx, 3, 10*time.Second, func() error {
		c, e := ex.EnsureDevBranch(ctx, wt, token, branch, devBranch)
		devCreated = c
		return e
	}); err != nil {
		if isInfraError(err) {
			return rr, repoSignal{infra: true, infraErr: "dev-Branch vorbereiten " + repo.ID + ": " + err.Error()}
		}
		rr.Error = "dev-Branch vorbereiten: " + err.Error()
		return rr, repoSignal{}
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
			step("fold", "Standard-Branch "+branch+" nicht konfliktfrei einfaltbar — dev-Stand ohne die neuesten "+branch+"-Änderungen fortgeführt", false)
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

	actx, cancel := context.WithCancel(ctx)
	if d := runAgentTimeout(); d > 0 {
		cancel()
		actx, cancel = context.WithTimeout(ctx, d)
	}
	defer cancel()

	// REPORT: read-only plan against the dev state; no push, no deploy, no delivery.
	if x.mode == "report" {
		final, lim, err := x.runAgentLive(actx, ex, wt, prompt, "plan", "analyze", tuningFor(run), atts, &rr, saver)
		if lim.limited {
			return rr, lim
		}
		if err != nil {
			rr.Error = "analyze: " + err.Error()
			return rr, repoSignal{}
		}
		applyUsage(&rr, final) // the agent's report already streamed into the analyze step
		if tip, e := ex.RevParse(ctx, wt, "HEAD"); e == nil {
			rr.DevCommit = tip
		}
		rr.OK = true
		return rr, repoSignal{}
	}

	// PR / FULL: implement ON the dev branch (it already carries the accumulated work).
	final, lim, err := x.runAgentLive(actx, ex, wt, prompt, "bypassPermissions", "implement", tuningFor(run), atts, &rr, saver)
	if lim.limited {
		return rr, lim
	}
	if err != nil {
		rr.Error = "implement: " + err.Error()
		return rr, repoSignal{}
	}
	applyUsage(&rr, final) // the agent's report already streamed into the implement step

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
	devAdvanced := devCreated || finalTip != startTip

	if !devAdvanced {
		step("implement", "keine neuen Änderungen — dev-Stand unverändert ("+devBranch+"@"+short(finalTip)+"), nichts auszuliefern", true)
		rr.OK = true
		return rr, repoSignal{}
	}

	// DEV-DEPLOY (full mode only) — delivers EXACTLY the dev branch: nothing folded together, filtered or
	// skipped (req 2). BEFORE the push, deliberately: dev is the very box we built on, so this workspace
	// already IS the source record. PROD is untouched here — it ships only from a MERGED default branch
	// (Maintain, req 6).
	//
	// Finding C: the build runs UNPRIVILEGED here; the root wrapper only installs+restarts. Finding B: the
	// self repo is NEVER dev-deployed (restarting THIS devlabd would kill the running sweep). A dev-deploy
	// failure is NON-fatal. A skip that is expected (self repo, switched off, no deploy target) is a
	// SUCCESSFUL step carrying its reason; only a real build/install failure is red.
	switch {
	case x.mode != "full":
		// report/pr never deploy at all — no step, as before.
	case !x.shouldDevDeploy(repo.ID):
		step("dev-deploy", devDeploySkipReason(repo.ID), true)
	case !x.deployable(repo.Name):
		step("dev-deploy", noDeployTargetReason(repo.Name), true)
	default:
		// No folding of open PRs any more: the dev branch IS the accumulated state, so what is built here
		// is by construction the sum of everything delivered so far. Assembling a state per deploy — the
		// old approach — is what let features disappear whenever one PR did not merge cleanly.
		if artifactDir, berr := x.buildArtifact(ctx, wt, repo); berr != nil {
			step("dev-deploy", "Build fehlgeschlagen (nicht fatal): "+berr.Error(), false)
		} else if depLog, derr := x.deploy(ctx, repo, artifactDir, "dev"); derr != nil {
			step("dev-deploy", depLog+"\n"+derr.Error(), false)
		} else {
			step("dev-deploy", depLog+"\nAusgelieferter Stand: "+devBranch+"@"+short(finalTip), true)
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
			rr.Error = "Lieferungs-Branch: " + err.Error()
			return rr, repoSignal{}
		}
	}
	pushRefs := []string{devBranch}
	if deliveryBranch != "" {
		pushRefs = append(pushRefs, deliveryBranch)
	}
	if _, err := ex.PushRefs(ctx, wt, token, false, pushRefs...); err != nil {
		rr.Error = "push: " + err.Error()
		return rr, repoSignal{}
	}
	step("push", strings.Join(pushRefs, ", "), true)

	if !hasDelivery {
		// The dev branch advanced only by folding in the default branch — dev now serves it, but there is
		// no PR-worthy unit of the runner's OWN work, so no delivery and no PR.
		step("implement", "nur Standard-Branch eingefaltet — dev-Stand aktualisiert ("+devBranch+"@"+short(finalTip)+"), kein eigener Beitrag", true)
		rr.OK = true
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
			step("pr", err.Error(), false)
			rr.Error = "pr: " + err.Error()
			return rr, repoSignal{}
		}
	}
	rr.PRUrl = pr.HTMLURL
	rr.PRBase = prBase
	rr.DeliveryID = deliveryID
	step("pr", pr.HTMLURL+" (Basis: "+prBase+")", true)
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
	rr.OK = true
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

// isInfraError reports whether an error is an infrastructure/connectivity failure (host can't reach
// GitHub) rather than a problem with the specific repo or the work. This is the "gucken, WARUM nicht
// geklont werden kann" step: the runner distinguishes "the network is down" from "this repo is broken"
// and reacts differently — the former is transient and retried, the latter is a real per-repo failure.
func isInfraError(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	for _, s := range infraErrorMarkers {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
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
}

func beginAgentStep(rr *runs.RepoResult, saver *liveSaver, name string) *agentStep {
	rr.Steps = append(rr.Steps, runs.Step{Name: name, Running: true, At: time.Now().UTC()})
	saver.force()
	return &agentStep{rr: rr, saver: saver, idx: len(rr.Steps) - 1}
}

// onProgress folds one line of streamed agent output into the running step's transcript (throttled save).
func (a *agentStep) onProgress(line []byte) {
	if a.tr.push(line) {
		a.rr.Steps[a.idx].Log = a.tr.clipped()
		a.saver.throttled()
	}
}

func (a *agentStep) finish(report string) {
	s := &a.rr.Steps[a.idx]
	s.Running, s.OK, s.Log = false, true, clip(report)
	a.saver.force()
}

func (a *agentStep) fail(logtxt string) {
	s := &a.rr.Steps[a.idx]
	s.Running, s.OK, s.Log = false, false, clip(logtxt)
	a.saver.force()
}

// runAgentLive runs the agent as a live step `name` on rr and returns the extracted final result event
// (for usage/limit parsing). On a usage-limit stop it leaves the step running and the repo unrecorded
// (it retries on resume); on error it fails the step; on success it finalizes the step to the report.
func (x *runExecutor) runAgentLive(actx context.Context, ex workspace.Executor, wt, prompt, permMode, name string, t agentTuning, atts []loadedAttachment, rr *runs.RepoResult, saver *liveSaver) (final []byte, lim repoSignal, err error) {
	ag := beginAgentStep(rr, saver, name)
	out, aerr := x.runAgent(actx, ex, wt, prompt, permMode, t, atts, ag.onProgress)
	final = resultEvent(out)
	if l := detectLimit(final, aerr); l.limited {
		return final, l, aerr // leave the step running; the repo is not recorded
	}
	if aerr != nil {
		ag.fail(agentError(aerr))
		return final, repoSignal{}, aerr
	}
	ag.finish(parseClaudeResult(final).Output)
	return final, repoSignal{}, nil
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
	// Self-healing runs every tick, independent of any tracked PR: a wedged run that has blown past the
	// hard duration ceiling is cancelled so it stops blocking every future trigger, and crash-orphaned
	// husks are reaped so nothing lingers forever as a running "Leiche". Both keep the pipeline restartable.
	x.cancelWedgedRun()
	x.reapOrphanedHusks()

	prs, err := x.s.runPRs.List()
	if err != nil || len(prs) == 0 {
		return
	}
	token, err := x.token()
	if err != nil {
		return
	}
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
			// A PR that reached main (merged) is what checks a ToDo off; a PR CLOSED without merging is a
			// rejection — the ToDo stays open so it can be restarted, never silently marked done.
			if cur.Merged {
				x.markTodoDoneIfAllMerged(p.RunID)
			}
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
				x.markTodoDoneIfAllMerged(p.RunID)      // reached main → the ToDo may now be checked off
			}
			// full: keep tracked — the next eligible tick sees it merged and prod-deploys it.
		case prDeploy:
			x.markDelivery(p.Repo, p.Number, runs.DeliveryMerged) // the PR is merged; prod-deploy follows
			// A merged PR for a repo with NO deploy target has nothing to ship: retrying it every recheck
			// interval only reset its workspace and rebuilt nothing, forever. Untrack it like report/pr does.
			if name := repoNameOf(p.Repo); !x.deployable(name) {
				x.markDelivered(p, true, false) // merged; there is simply nothing to ship
				_ = x.s.runPRs.Remove(p.Repo, p.Number)
				x.markTodoDoneIfAllMerged(p.RunID) // merged (deploy is a no-op here) → done once all merged
				log.Printf("devlabd: %s#%d gemerged — %s → untracked", p.Repo, p.Number, noDeployTargetReason(name))
				continue
			}
			depLog, derr := x.runProdDeploy(ctx, token, p)
			if derr != nil {
				log.Printf("devlabd: prod-deploy %s#%d failed (will retry the deploy): %v\n%s", p.Repo, p.Number, derr, clip(depLog))
				continue // keep tracked; retry the DEPLOY next eligible tick (never re-merge)
			}
			x.markDelivered(p, true, true)
			_ = x.s.runPRs.Remove(p.Repo, p.Number) // idempotent untrack-on-success
			x.markTodoDoneIfAllMerged(p.RunID)      // reached main AND shipped → the ToDo may now be checked off
			log.Printf("devlabd: prod-deployed %s#%d (run %s)", p.Repo, p.Number, p.RunID)
		case prNone:
			// full-mode recheck: still open within its window → nothing to do yet.
		}
	}
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

// markTodoDoneIfAllMerged checks a ToDo off ONCE the main-merge is through: called right after one of a
// run's PRs merges, it verifies NO pending PR of that run remains, and only then flips the ToDo's Done.
// This is the "in die History erst, wenn der main-Merge durch ist" rule — a ToDo opened as several PRs
// stays in the active list until the LAST one lands. A no-op for auto runs (they are recurring, never
// "done") and when the stores are absent.
func (x *runExecutor) markTodoDoneIfAllMerged(runID string) {
	if x.s == nil || x.s.runs == nil || x.s.runPRs == nil || runID == "" {
		return
	}
	pending, err := x.s.runPRs.List()
	if err != nil {
		return
	}
	for _, p := range pending {
		if p.RunID == runID {
			return // more of this run's PRs are still awaiting their merge
		}
	}
	_, _ = x.s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		for i := range cur {
			if cur[i].ID == runID && cur[i].IsTodo() && !cur[i].Done {
				cur[i].Done = true
				log.Printf("devlabd: ToDo %s abgeschlossen — alle PRs sind auf main gemergt", runID)
			}
		}
		return cur, nil
	})
}

// wedgedRunGrace is how far past the hard duration ceiling a run may linger before it is treated as
// wedged and cancelled. A healthy run self-terminates at maxRunDuration via its own context deadline;
// only a genuinely stuck one survives past ceiling + grace, and it would otherwise hold the "one run at
// a time" lock forever, making EVERY future trigger return "es läuft bereits". The grace absorbs clock
// skew and shutdown lag so a run that is merely finishing up is never cancelled.
const wedgedRunGrace = 30 * time.Minute

// orphanHuskGrace is how long an unfinished ToDo husk may sit untouched before it is reaped. A crash or
// devlabd restart mid-run leaves a husk whose steps read "läuft" forever; unlike an auto run (which
// resumes on its next scheduled fire) a fired-once ToDo has nothing to pick it up, so it is a "Leiche".
// The grace lets a fast restart resume it first (a manual re-run within the window still continues it).
const orphanHuskGrace = 15 * time.Minute

// cancelWedgedRun self-heals a stuck run: if the run in flight has blown past the hard duration ceiling
// by more than the grace, it is cancelled (the existing kill-switch) so it releases the single-run lock
// and triggers work again. No-op when no ceiling is configured (can't tell wedged from legitimately long)
// or nothing is running.
func (x *runExecutor) cancelWedgedRun() {
	if x.s == nil || x.s.scheduler == nil {
		return
	}
	ceiling := maxRunDuration()
	if ceiling <= 0 {
		return
	}
	a := x.s.scheduler.Active()
	if a == nil {
		return
	}
	if runWedged(a.StartedAt, ceiling, time.Now()) {
		log.Printf("devlabd: run %s läuft seit %s (> Grenze %s) — als hängend abgebrochen, damit wieder gestartet werden kann",
			a.RunID, time.Since(a.StartedAt).Round(time.Minute), ceiling)
		x.s.scheduler.Cancel()
	}
}

// runWedged reports whether a run started at startedAt has exceeded the hard ceiling by more than the
// grace and is therefore stuck. Pure so the threshold is unit-tested without a live scheduler; a zero
// ceiling (cap disabled) or zero start is never wedged.
func runWedged(startedAt time.Time, ceiling time.Duration, now time.Time) bool {
	if ceiling <= 0 || startedAt.IsZero() {
		return false
	}
	return now.Sub(startedAt) > ceiling+wedgedRunGrace
}

// reapOrphanedHusks finalizes crash-orphaned ToDo husks so nothing lingers as a running "Leiche" and the
// ToDo becomes cleanly restartable. Only fire-once ToDos are swept: an auto run resumes a stranded husk on
// its next scheduled fire, and a suspended run resumes when its window resets — neither is orphaned. The
// currently-live run is skipped, and only husks untouched past the grace are reaped, so an in-flight or
// just-restarted resume is never disturbed.
func (x *runExecutor) reapOrphanedHusks() {
	if x.s == nil || x.s.runs == nil || x.s.runResults == nil {
		return
	}
	live := ""
	if x.s.scheduler != nil {
		if a := x.s.scheduler.Active(); a != nil {
			live = a.RunID
		}
	}
	all, err := x.s.runs.List()
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-orphanHuskGrace)
	for _, run := range all {
		if !run.IsTodo() || run.ID == live || run.Suspended != nil {
			continue
		}
		if husk, ok := x.s.runResults.FindStaleHusk(run.ID, cutoff); ok {
			x.reap(husk, "unterbrochen (Absturz/Neustart) — als fehlgeschlagen abgeschlossen; jetzt neu startbar")
		}
	}
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

// applyUsage folds the Claude CLI's token/cost usage into a repo result.
func applyUsage(rr *runs.RepoResult, out []byte) {
	u := parseClaudeUsage(out)
	rr.InputTokens += u.in
	rr.OutputTokens += u.out
	rr.CostUSD += u.cost
	rr.NumTurns += u.turns
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
	if _, ok, err := s.runs.Get(id); err != nil || !ok {
		writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
		return
	}
	if !s.scheduler.FireNow(id, actor(r)) {
		writeErr(w, http.StatusConflict, "Es läuft bereits ein Lauf — bitte warten")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"started": true})
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
	var active *runs.Activity
	if s.scheduler != nil {
		active = s.scheduler.Active()
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "inflight": s.assembleInFlight(active)})
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

// assembleInFlight builds the transparent list of runs the system is currently working: the one
// EXECUTING right now (from the live Activity) followed by every SUSPENDED run (paused on the usage
// limit). Each entry is enriched from its live result document so the overview shows the current
// repo/step, progress and spend at a glance. Read-only — it never touches scheduling state.
func (s *Server) assembleInFlight(active *runs.Activity) []inFlightRun {
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

	// 1) The run executing right now (at most one — the scheduler runs runs serially).
	if active != nil {
		e := inFlightRun{RunID: active.RunID, State: "executing", ResultID: active.ResultID}
		st := active.StartedAt
		e.StartedAt = &st
		if run, ok := byID[active.RunID]; ok {
			e.RunName = run.Name
			e.Type = string(runs.NormalizeType(run.Type))
			e.ReposTotal = todoRepoTotal(run)
		}
		s.enrichInFlight(&e, active.RunID, active.ResultID)
		out = append(out, e)
	}

	// 2) Every run suspended mid-execution on the usage limit — genuinely in flight, just paused. Never
	//    double-count the executing one (a run cannot be both).
	for _, run := range all {
		if run.Suspended == nil || (active != nil && run.ID == active.RunID) {
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

// runCancel aborts the run in progress (kill-switch).
func (s *Server) runCancel(w http.ResponseWriter, r *http.Request) {
	if s.scheduler == nil {
		writeErr(w, http.StatusServiceUnavailable, "Ausführung ist nicht konfiguriert")
		return
	}
	if !s.scheduler.Cancel() {
		writeErr(w, http.StatusConflict, "Kein Lauf aktiv")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
