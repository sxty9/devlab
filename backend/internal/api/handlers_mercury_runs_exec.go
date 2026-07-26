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
	deployScriptDir = "/etc/devlab/deploy.d"
	runAgentTimeout = 60 * time.Minute // a full implement pass can be long
	runnerPreamble  = "You are the autonomous Holistic runner, executing unattended on the server. Work " +
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
	if s.runs == nil || s.runResults == nil || s.runPRs == nil || s.links == nil {
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
	// Defaults to user so an existing single-account setup keeps working untouched.
	tokenUser := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_TOKEN_USER"))
	if tokenUser == "" {
		tokenUser = user
	}
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
	// WARNING: user is the WORKSPACE owner, and executeRepo calls ResetToRemote (git reset --hard +
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
		return runs.ResultRef{ResultID: res.ResultID, At: res.StartedAt, OK: false, RepoCount: len(res.Repos)}, fmt.Errorf("%s", msg)
	}
	// carryOver stops WITHOUT finalising (FinishedAt stays zero) so the next fire resumes this same
	// result and skips the done repos — for transient infrastructure failures (no network) that must not
	// be recorded as a permanent, "run complete" failure. It is the infra sibling of fail().
	carryOver := func(reason string) (runs.ResultRef, error) {
		res.OK = false
		save()
		log.Printf("devlabd: run %s carried over before completing (%s) — next fire resumes it", run.ID, reason)
		return runs.ResultRef{
			ResultID: res.ResultID, At: res.StartedAt, OK: false, RepoCount: len(res.Repos),
			InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD,
		}, nil
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
		rr, lim := x.executeRepo(ctx, run, repo, x.promptFor(run, newRepoName[repo.ID], todoAtts), token, ghLogin, ghID, todoAtts, &res, saver)
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
		return runs.ResultRef{
			ResultID: res.ResultID, At: res.StartedAt, OK: overallOK, RepoCount: len(res.Repos),
			InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD,
		}, nil
	}

	res.FinishedAt = time.Now().UTC()
	res.OK = overallOK
	save()
	return runs.ResultRef{
		ResultID: res.ResultID, At: res.StartedAt, OK: overallOK, RepoCount: len(res.Repos),
		InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD,
	}, nil
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
		return runs.Result{RunID: run.ID, ResultID: runs.NewResultID(start), RunName: run.Name,
			Type: runs.NormalizeType(run.Type), Mode: x.mode, StartedAt: start.UTC(),
			PromptHash: run.PromptHash, Prompt: run.Prompt}
	}
	consider := func(existing runs.Result) (runs.Result, bool, bool) {
		if existing.Mode != "" && existing.Mode != x.mode {
			x.reap(existing, fmt.Sprintf("Modus gewechselt (%s → %s) — Husk nicht fortgesetzt", existing.Mode, x.mode))
			return runs.Result{}, false, true // reaped: don't resume, fall through to fresh
		}
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
		return runs.ResultRef{ResultID: res.ResultID, At: res.StartedAt, OK: false, RepoCount: len(res.Repos),
			InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD}, nil
	}
	res.Suspended = true
	res.ResumeAt = &resumeAt
	save()
	log.Printf("devlabd: run %s suspended on usage limit — resuming at %s (attempt %d)", run.ID, resumeAt.Format(time.RFC3339), attempts+1)
	return runs.ResultRef{ResultID: res.ResultID, At: res.StartedAt, OK: false, RepoCount: len(res.Repos),
		InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, CostUSD: res.CostUSD,
		Suspended: true, ResumeAt: &resumeAt}, nil
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

func (x *runExecutor) executeRepo(ctx context.Context, run runs.Run, repo model.Repo, prompt, token, ghLogin string, ghID int64, atts []loadedAttachment, res *runs.Result, saver *liveSaver) (runs.RepoResult, repoSignal) {
	rr := runs.RepoResult{Repo: repo.ID, Running: true}
	res.Live = &rr // publish this repo as the in-flight one; the caller clears Live once it settles
	saver.force()
	// step records a COMPLETED stage and re-saves, so the fast git stages (push/pr/deploy) also surface
	// live as they land. The long agent stages instead use runAgentLive (a running step that streams).
	step := func(name, logtxt string, ok bool) {
		rr.Steps = append(rr.Steps, runs.Step{Name: name, OK: ok, Log: clip(logtxt), At: time.Now().UTC()})
		saver.force()
	}

	// NOTE: a repo with an open Mercury PR is NEVER skipped. The run proceeds normally; it just bases its
	// work on main + the still-open pending PRs (see the run-branch setup below), so the agent sees not-yet-
	// merged work as present and does not redo it, while still implementing whatever a (possibly different-
	// axiom) run still needs.

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

	actx, cancel := context.WithTimeout(ctx, runAgentTimeout)
	defer cancel()

	// REPORT: read-only plan against the dev state; no push, no deploy, no delivery.
	if x.mode == "report" {
		final, lim, err := x.runAgentLive(actx, ex, wt, prompt, "plan", "analyze", atts, &rr, saver)
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
	final, lim, err := x.runAgentLive(actx, ex, wt, prompt, "bypassPermissions", "implement", atts, &rr, saver)
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

// shouldDevDeploy reports whether executeRepo performs an in-process dev-deploy for repoID: only in
// full mode, only while dev-deploy is enabled, and NEVER for the self repo (Finding B — dev-deploying
// devlab restarts THIS devlabd, killing the running sweep). report/pr never dev-deploy at all.
func (x *runExecutor) shouldDevDeploy(repoID string) bool {
	return x.mode == "full" && devDeployEnabled() && !isSelfRepo(repoID)
}

// devDeploySkipReason explains, for the recorded step, why shouldDevDeploy said no in full mode — the
// self-repo guard (Finding B) or the kill switch. It never invents a third reason: any other "no" comes
// from the deploy target, which hasDeployTarget/noDeployTargetReason report.
func devDeploySkipReason(repoID string) string {
	if isSelfRepo(repoID) {
		return "Selbst-Deploy übersprungen — " + repoID + " wird nicht aus seinem eigenen Lauf heraus neugestartet (PR steht, Deploy out-of-band)"
	}
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
func (x *runExecutor) promptFor(run runs.Run, newRepo string, atts []loadedAttachment) string {
	if run.IsTodo() {
		return mercury.ComposeTodoPrompt(run.Name, run.Task, newRepo, attachmentDescriptors(atts))
	}
	return run.Prompt
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
// disposable (the next run's ResetToRemote clears any leftover), so cleanup is best-effort.
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
func (x *runExecutor) runAgent(actx context.Context, ex workspace.Executor, wt, prompt, permMode string, atts []loadedAttachment, onLine func([]byte)) ([]byte, error) {
	cleanup, err := writeWorkspaceAttachments(ex, wt, atts)
	if err != nil {
		return nil, fmt.Errorf("Medien bereitstellen: %w", err)
	}
	defer cleanup()
	// Stream when a live sink is present (a real run) and streaming is enabled — so the agent can be
	// followed as it works. Otherwise the plain buffered call. resultEvent() reconciles both wire formats.
	if onLine != nil && streamEnabled() {
		return ex.AgentStream(actx, wt, onLine, streamAgentArgs(prompt, permMode)...)
	}
	return ex.Agent(actx, wt, agentArgs(prompt, permMode)...)
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
func (x *runExecutor) runAgentLive(actx context.Context, ex workspace.Executor, wt, prompt, permMode, name string, atts []loadedAttachment, rr *runs.RepoResult, saver *liveSaver) (final []byte, lim repoSignal, err error) {
	ag := beginAgentStep(rr, saver, name)
	out, aerr := x.runAgent(actx, ex, wt, prompt, permMode, atts, ag.onProgress)
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
	for _, p := range prs {
		if !shouldCheckPR(x.mode, now, p.MergeBy, p.LastChecked, recheck) {
			continue // report/pr: within window → not touched. full: throttled between rechecks.
		}
		if x.mode == "full" {
			_ = x.s.runPRs.Touch(p.Repo, p.Number, now) // stamp the recheck up front (throttle even on error)
		}
		cur, err := x.fetchPR(ctx, token, p.Repo, p.Number)
		if err != nil {
			continue // transient; retry next eligible tick
		}
		switch decidePR(x.mode, cur, !now.Before(p.MergeBy)) {
		case prUntrack:
			// Mirror the outcome onto the delivery ledger so LatestOpen (the stacked-PR base) stays honest:
			// a merged PR closes its delivery as merged, a closed-without-merge one as closed.
			if cur.Merged {
				x.markDelivery(p.Repo, p.Number, runs.DeliveryMerged)
			} else {
				x.markDelivery(p.Repo, p.Number, runs.DeliveryClosed)
			}
			_ = x.s.runPRs.Remove(p.Repo, p.Number) // merged (report/pr) or closed → stop tracking
		case prMerge:
			if err := x.mergePR(ctx, token, p.Repo, p.Number); err != nil {
				log.Printf("devlabd: auto-merge %s#%d failed (will retry): %v", p.Repo, p.Number, err)
				continue
			}
			log.Printf("devlabd: auto-merged %s#%d (run %s)", p.Repo, p.Number, p.RunID)
			x.markDelivery(p.Repo, p.Number, runs.DeliveryMerged)
			if x.mode != "full" {
				_ = x.s.runPRs.Remove(p.Repo, p.Number) // report/pr: merged and done
			}
			// full: keep tracked — the next eligible tick sees it merged and prod-deploys it.
		case prDeploy:
			x.markDelivery(p.Repo, p.Number, runs.DeliveryMerged) // the PR is merged; prod-deploy follows
			// A merged PR for a repo with NO deploy target has nothing to ship: retrying it every recheck
			// interval only reset its workspace and rebuilt nothing, forever. Untrack it like report/pr does.
			if name := repoNameOf(p.Repo); !x.deployable(name) {
				_ = x.s.runPRs.Remove(p.Repo, p.Number)
				log.Printf("devlabd: %s#%d gemerged — %s → untracked", p.Repo, p.Number, noDeployTargetReason(name))
				continue
			}
			depLog, derr := x.runProdDeploy(ctx, token, p)
			if derr != nil {
				log.Printf("devlabd: prod-deploy %s#%d failed (will retry the deploy): %v\n%s", p.Repo, p.Number, derr, clip(depLog))
				continue // keep tracked; retry the DEPLOY next eligible tick (never re-merge)
			}
			_ = x.s.runPRs.Remove(p.Repo, p.Number) // idempotent untrack-on-success
			log.Printf("devlabd: prod-deployed %s#%d (run %s)", p.Repo, p.Number, p.RunID)
		case prNone:
			// full-mode recheck: still open within its window → nothing to do yet.
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

func agentArgs(prompt, mode string) []string {
	return []string{
		"-p", prompt,
		"--output-format", "json",
		"--permission-mode", mode,
		"--model", "opus",
		"--effort", "max",
		"--append-system-prompt", runnerPreamble,
	}
}

func runPRBody(run runs.Run) string {
	return "Automatisch erzeugt vom Mercury-Lauf **" + run.Name + "**. Dieser PR bündelt die vom autonomen " +
		"Runner implementierten Änderungen gegen die Axiome dieses Laufs. Merge jederzeit möglich; ohne Merge " +
		"innerhalb der Frist wird automatisch gemergt.\n\n🤖 Mercury"
}

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
func (s *Server) runActive(w http.ResponseWriter, r *http.Request) {
	var active *runs.Activity
	if s.scheduler != nil {
		active = s.scheduler.Active()
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active})
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
