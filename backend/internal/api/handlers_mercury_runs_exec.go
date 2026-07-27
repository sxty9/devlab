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

	// Ensure() returns an ALREADY-CLONED workspace untouched — it never fetches. Without this the
	// runner analyses the snapshot taken at first clone (and in pr/full mode branches off a stale
	// origin/<branch>), so nightly runs silently work against outdated code and never see anything
	// pushed since. Refresh to the real remote state before the agent looks at it. Network-classified so
	// a fetch failure carries over rather than failing the repo.
	if err := retryInfra(ctx, 3, 10*time.Second, func() error {
		return ex.ResetToRemote(ctx, wt, token, branch)
	}); err != nil {
		if isInfraError(err) {
			return rr, repoSignal{infra: true, infraErr: "workspace aktualisieren " + repo.ID + ": " + err.Error()}
		}
		rr.Error = "workspace aktualisieren: " + err.Error()
		return rr, repoSignal{}
	}

	actx, cancel := context.WithTimeout(ctx, runAgentTimeout)
	defer cancel()

	// REPORT: read-only plan; no branch, no writes, no push, no deploy.
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
		rr.OK = true
		return rr, repoSignal{}
	}

	// PR / FULL: implement on a fresh run branch.
	runBranch := "mercury-run/" + run.ID + "/" + runs.NewResultID(time.Now())
	if err := ex.CreateBranch(ctx, wt, runBranch, "origin/"+branch); err != nil {
		rr.Error = "branch: " + err.Error()
		return rr, repoSignal{}
	}
	if err := ex.Checkout(ctx, wt, runBranch); err != nil {
		rr.Error = "checkout: " + err.Error()
		return rr, repoSignal{}
	}

	// Base the run on main + the still-open pending Mercury PRs (NOT a skip). Fold each open Mercury run
	// branch into this run branch so the agent sees not-yet-merged work as already present and does not
	// redo it — while still implementing whatever else this (possibly different-axiom) run needs. A PR
	// branch that no longer merges cleanly is skipped with a logged note; the run still proceeds. ToDos
	// keep a plain main base (they are explicit, targeted tasks). report mode never reaches here.
	if !run.IsTodo() {
		if heads := x.openMercuryPRHeads(ctx, token, repo.FullName); len(heads) > 0 {
			_ = ex.Fetch(ctx, wt, token) // refresh the pending PR branches before merging
			merged := 0
			for _, h := range heads {
				if h == runBranch {
					continue
				}
				if err := ex.MergeRef(ctx, wt, "origin/"+h); err != nil {
					step("pending-pr", "offener PR-Branch "+h+" nicht konfliktfrei mergebar — ohne ihn fortgefahren: "+err.Error(), false)
					continue
				}
				merged++
			}
			if merged > 0 {
				step("pending-prs", fmt.Sprintf("Base = main + %d offene(r) Mercury-PR(s) berücksichtigt — keine Doppelarbeit", merged), true)
			}
		}
	}

	// The run's base AFTER folding in the pending PRs. New work is measured against THIS tip (not plain
	// origin/main), so a run that only re-surfaces already-pending changes contributes nothing new and
	// opens no redundant PR; only genuine additions from this run get pushed.
	baseTip, err := ex.RevParse(ctx, wt, "HEAD")
	if err != nil {
		rr.Error = "Base-Tip ermitteln: " + err.Error()
		return rr, repoSignal{}
	}

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

	// Measure what THIS run added on top of the base (main + pending PRs), not on top of plain main — so
	// a run that only reproduced already-pending work contributes nothing new and opens no redundant PR.
	ahead, err := ex.CommitsAhead(ctx, wt, baseTip)
	if err != nil {
		rr.Error = "Commits zählen: " + err.Error()
		return rr, repoSignal{}
	}
	if ahead == 0 {
		step("implement", "keine neuen Änderungen über main + pending PRs hinaus — nichts zu pushen", true)
		rr.OK = true
		return rr, repoSignal{}
	}

	// DEV-DEPLOY (full mode only) — BEFORE the push, deliberately. dev is the very box we built on, so
	// this workspace and its run branch already ARE the source record for the code going live here;
	// there is no "live code with no source" risk that the old push-first ordering guarded against.
	// That invariant still holds for PROD, which is no longer touched here at all — a prod-deploy is
	// driven only by a MERGED PR in Maintain, where the pushed branch + merge are the durable record
	// before anything ships to the VPS.
	//
	// Finding C: the build runs UNPRIVILEGED here (buildArtifact, as the runner itself); the root
	// wrapper only installs+restarts a prebuilt artifact, never builds. Finding B: the self repo is
	// NEVER dev-deployed — restarting THIS devlabd would kill the running sweep; its PR still lands and
	// its deploy happens out-of-band (its prod-deploy, which restarts the VPS not the runner, is fine
	// via Maintain). A dev-deploy failure is NON-fatal: we log the step and still push + open the PR.
	//
	// EVERY full-mode repo records a dev-deploy step, including one that is skipped: an omitted step reads
	// as "never attempted" in the pipeline view, which is how a whole disabled half of the pipeline stayed
	// invisible. A skip that is expected (self repo, switched off, no deploy target) is a SUCCESSFUL step
	// carrying its reason; only a real build/install failure is red.
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
			step("dev-deploy", depLog, true)
			rr.Deployed = true
		}
	}

	// Push + PR. The remote branch and PR are the durable record of what was built — established before
	// prod ever runs (prod-deploy happens only in Maintain, on merge). A push failure is terminal for
	// this repo; the local run branch is discarded by the next run's ResetToRemote.
	if _, err := ex.Push(ctx, wt, token); err != nil {
		rr.Error = "push: " + err.Error()
		return rr, repoSignal{}
	}
	step("push", runBranch, true)

	pr, err := github.CreatePullRequest(ctx, token, repo.FullName, runBranch, branch, "Mercury-Lauf: "+run.Name, runPRBody(run))
	if err != nil {
		if found, ok := github.FindOpenPullRequest(ctx, token, repo.FullName, runBranch); ok {
			pr = found
		} else {
			step("pr", err.Error(), false)
			rr.Error = "pr: " + err.Error()
			return rr, repoSignal{}
		}
	}
	rr.PRUrl = pr.HTMLURL
	step("pr", pr.HTMLURL, true)
	_ = x.s.runPRs.Add(runs.PendingPR{
		Repo: repo.FullName, Number: pr.Number, URL: pr.HTMLURL, RunID: run.ID,
		CreatedAt: time.Now().UTC(), MergeBy: time.Now().Add(x.autoMergeAfter).UTC(),
	})

	// PROD-DEPLOY is intentionally NOT done here. In the two-environment model the dev-deploy above
	// already put this build live on the dev box; prod is reached only after the PR is MERGED (by a
	// human or by the auto-merge window), detected and shipped by Maintain in full mode. That keeps the
	// merge as the gate for prod and avoids deploying unreviewed code straight to the VPS.
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

// noDeployRepos is the set of repos an operator has marked NOT-TO-BE-DELIVERED to prod
// (DEVLAB_RUNS_NO_DEPLOY — comma/space/newline/semicolon separated bare repo names, case-insensitive).
// A merged PR for such a repo triggers NO prod-deploy attempt at all, so a service deliberately not run
// in prod never produces a failed attempt. This is RUNTIME config (an operator's decision), never a
// repo-committed instance specific — keeping the repos instance-neutral.
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

// shipToProd is the SINGLE gate deciding whether a merged PR's repo should be prod-deployed at all. Two
// deliberate no-ship cases return ship=false with a human reason and are untracked WITHOUT an attempt,
// so neither can produce a failed attempt:
//   - the repo is explicitly not-to-be-delivered (DEVLAB_RUNS_NO_DEPLOY) — an operator's decision; or
//   - the repo has no vetted deploy script (a library/template/data repo — nothing to ship).
//
// Otherwise ship=true and Maintain runs the deploy. Unifying both "don't ship" reasons behind one call
// keeps a single access point for "is this repo prod-deployable" rather than two parallel checks.
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

const defaultMaxDeployAttempts = 3

// maxDeployAttempts is how many consecutive PERMANENT prod-deploy failures a merged PR gets before it is
// blocked — a small safety margin against a misclassified transient failure. Transient failures never
// count toward it. DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS overrides; a non-positive value keeps the default.
func maxDeployAttempts() int {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxDeployAttempts
}

// repoNameOf reduces an owner/repo full name to the bare repo name the deploy allowlist is keyed by.
func repoNameOf(fullName string) string {
	if i := strings.LastIndex(fullName, "/"); i >= 0 {
		return fullName[i+1:]
	}
	return fullName
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
	return err != nil && hasInfraMarker(err.Error())
}

// hasInfraMarker reports whether a message carries a connectivity-failure signature. Split out of
// isInfraError so a deploy failure can be classified on the wrapper's combined OUTPUT (where the real
// ssh/rsync cause lives) as well as on the Go error. Matched case-insensitively.
func hasInfraMarker(s string) bool {
	m := strings.ToLower(s)
	for _, marker := range infraErrorMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// ─── prod-deploy failure classification ─────────────────────────────────────
//
// A merged PR's prod-deploy can fail two very different ways, and Maintain must tell them apart: a
// TRANSIENT connectivity failure (the VPS is briefly unreachable) will succeed on a later tick and is
// retried; a PERMANENT failure (the service is not set up on the target, a missing unit, a bad config)
// will fail identically forever and, after a few attempts, blocks. The real cause lives in the deploy
// wrapper's combined OUTPUT, not in the Go exit-status error — so classification reads the output first.

// deployTransient reports whether a failed prod-deploy is a transient connectivity failure (retry)
// rather than a permanent one (eventually block). Reuses the sweep's infra-marker set against the
// wrapper output and the Go error, so an ssh/rsync "connection refused" / "timed out" / "no route to
// host" is retried while a missing-unit or bad-config failure is not.
func deployTransient(out string, err error) bool {
	return hasInfraMarker(out) || isInfraError(err)
}

// deploySetupMissingMarkers are substrings of a deploy's combined OUTPUT that mean the service is NOT
// SET UP ON THE TARGET — no unit to (re)start, no vetted per-repo deploy script, missing prod config, or
// an unknown repo on the receiver — as distinct from a generic build/argument failure. Matched
// case-insensitively; used only to phrase the block reason, never to decide transient-vs-permanent.
var deploySetupMissingMarkers = []string{
	".service not found", // systemctl: the prod unit is not installed on the target
	"unit not found",
	"no such unit",
	"no deploy script",  // wrapper exit 3: no vetted per-repo script on the runner host
	"prod: missing",     // per-repo script exit 11: missing prod-target / deploy key config
	"prod: empty target",
	"no staged artifact", // receiver exit 3: nothing staged / no mapping for this repo on the target
	"no mapping",
	"unknown env",
}

// deploySetupMissing reports whether a permanent deploy failure specifically means "the service is not
// set up on the target", so the block reason can NAME that cause rather than leave a bare exit code.
func deploySetupMissing(out string) bool {
	m := strings.ToLower(out)
	for _, marker := range deploySetupMissingMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// deployBlockReason renders a human, service-and-target-naming reason for a blocked prod-deploy — never
// a bare technical return value (requirement 1). When the output matches a "not set up on the target"
// signature it says exactly that; otherwise it reports a durable delivery failure with the output tail.
func deployBlockReason(service, out string, err error) string {
	target := prodTargetName()
	detail := lastMeaningfulLine(out, err)
	if deploySetupMissing(out) {
		return fmt.Sprintf("Dienst »%s« ist im Ziel »%s« nicht eingerichtet: %s", service, target, detail)
	}
	return fmt.Sprintf("Auslieferung von »%s« nach »%s« dauerhaft fehlgeschlagen: %s", service, target, detail)
}

// prodTargetName is the human name of the delivery target a block reason cites. The concrete host lives
// server-side (/etc/devlab/prod-target) and is deliberately never exposed to the runner, so the target
// is named by its ENVIRONMENT ("prod"); DEVLAB_RUNS_PROD_TARGET_NAME overrides for a friendlier label.
func prodTargetName() string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_RUNS_PROD_TARGET_NAME")); v != "" {
		return v
	}
	return "prod"
}

// lastMeaningfulLine pulls the most informative line out of a deploy's combined output for a block
// reason — the last non-empty line (usually the concrete error), falling back to the Go error — clipped
// so the reason stays one readable sentence rather than a wall of log.
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

// openMercuryPRHeads returns the head branch names of every OPEN Mercury PR on fullName (prefix
// "mercury-run/") — the still-pending, not-yet-merged work the run must BASE ON (main + pending), so it
// neither redoes it nor skips the repo. A lookup error yields no heads (the run then bases on plain main;
// a genuine outage is caught as an infra carry-over at the clone step).
func (x *runExecutor) openMercuryPRHeads(ctx context.Context, token, fullName string) []string {
	prs, err := github.ListOpenPullRequests(ctx, token, fullName)
	if err != nil {
		return nil
	}
	var heads []string
	for _, pr := range prs {
		if strings.HasPrefix(pr.Head.Ref, "mercury-run/") {
			heads = append(heads, pr.Head.Ref)
		}
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
// a TRANSIENT deploy failure simply retries next eligible tick (never re-merges) — idempotent via
// untrack-on-success rather than a fragile persisted flag. A PERMANENT deploy failure (the service is
// not set up on the target) is NOT retried forever: after a few attempts the PR is BLOCKED and skipped
// here until an explicit resume, so one broken repo can never storm the pipeline or hold up the others.
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
		if p.Blocked {
			// A blocked delivery waits for an EXPLICIT resume — never auto-fetched or retried. Skipping
			// it here (before any GitHub read) is also what keeps one broken repo from holding up the
			// others: every other tracked PR is reconciled as usual.
			continue
		}
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
			_ = x.s.runPRs.Remove(p.Repo, p.Number) // merged (report/pr) or closed → stop tracking
		case prMerge:
			if err := x.mergePR(ctx, token, p.Repo, p.Number); err != nil {
				log.Printf("devlabd: auto-merge %s#%d failed (will retry): %v", p.Repo, p.Number, err)
				continue
			}
			log.Printf("devlabd: auto-merged %s#%d (run %s)", p.Repo, p.Number, p.RunID)
			if x.mode != "full" {
				_ = x.s.runPRs.Remove(p.Repo, p.Number) // report/pr: merged and done
			}
			// full: keep tracked — the next eligible tick sees it merged and prod-deploys it.
		case prDeploy:
			x.deployMergedPR(ctx, token, p, now)
		case prNone:
			// full-mode recheck: still open within its window → nothing to do yet.
		}
	}
}

// deployMergedPR ships one MERGED PR to prod (full mode) and reconciles the tracked record accordingly:
//   - a repo that must NOT be shipped (not-to-be-delivered, or no deploy script) is untracked at once,
//     WITHOUT a deploy attempt — so it can never produce a failed attempt;
//   - a successful deploy untracks the PR (idempotent untrack-on-success, never a re-merge);
//   - a TRANSIENT failure (VPS briefly unreachable) keeps it tracked to retry, and does NOT count
//     toward the block — only genuine outages retry;
//   - a PERMANENT failure counts one attempt and, once maxDeployAttempts is reached, BLOCKS the PR with
//     a service-and-target-naming reason and a timestamp, so it stops retrying and waits for an explicit
//     resume instead of failing identically forever.
func (x *runExecutor) deployMergedPR(ctx context.Context, token string, p runs.PendingPR, now time.Time) {
	name := repoNameOf(p.Repo)
	if ship, why := x.shipToProd(name); !ship {
		_ = x.s.runPRs.Remove(p.Repo, p.Number)
		log.Printf("devlabd: %s#%d gemerged — %s → untracked (kein Deploy-Versuch)", p.Repo, p.Number, why)
		return
	}
	depLog, derr := x.runProdDeploy(ctx, token, p)
	if derr == nil {
		_ = x.s.runPRs.Remove(p.Repo, p.Number) // idempotent untrack-on-success
		log.Printf("devlabd: prod-deployed %s#%d (run %s)", p.Repo, p.Number, p.RunID)
		return
	}
	if deployTransient(depLog, derr) {
		log.Printf("devlabd: prod-deploy %s#%d transient failure (will retry): %v\n%s", p.Repo, p.Number, derr, clip(depLog))
		return // keep tracked; retry next eligible tick. Does not count toward the block.
	}
	// Permanent failure: count this attempt; block once the threshold is reached (unobservable transition
	// via the pool's atomic Update — the WHEN-to-block decision stays here, outside the passive pool).
	reason := deployBlockReason(name, depLog, derr)
	limit := maxDeployAttempts()
	var attempts int
	blocked := false
	_, _ = x.s.runPRs.Update(p.Repo, p.Number, func(pr *runs.PendingPR) {
		pr.DeployAttempts++
		attempts = pr.DeployAttempts
		if pr.DeployAttempts >= limit {
			pr.Blocked, pr.BlockedReason, pr.BlockedAt = true, reason, now
			blocked = true
		}
	})
	if blocked {
		log.Printf("devlabd: prod-deploy %s#%d BLOCKED after %d attempt(s) — %s", p.Repo, p.Number, attempts, reason)
		return
	}
	log.Printf("devlabd: prod-deploy %s#%d permanent failure %d/%d (retry, then block): %v\n%s",
		p.Repo, p.Number, attempts, limit, derr, clip(depLog))
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

// blockedDeployView is the portioned shape the UI needs for a blocked delivery: enough to identify it
// (repo, PR number + url, originating run), understand it (reason, attempts, since when) and act on it
// (resume). Internal scheduling fields (MergeBy/LastChecked) are deliberately omitted.
type blockedDeployView struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	URL       string    `json:"url"`
	RunID     string    `json:"runId"`
	Reason    string    `json:"reason"`
	Attempts  int       `json:"attempts"`
	BlockedAt time.Time `json:"blockedAt"`
}

// runDeploysBlocked lists the deliveries the scheduler has BLOCKED after repeated permanent prod-deploy
// failures — the UI's window onto a stuck delivery (repo, reason, attempts) that today only the system
// log reveals. Read-only; the empty list when nothing is blocked or the PR store is absent. The
// blocked/not-blocked evaluation lives HERE, outside the passive PR pool.
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
	// Freshest problem first, so the newest block is on top.
	sort.Slice(out, func(i, j int) bool { return out[i].BlockedAt.After(out[j].BlockedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"blocked": out})
}

// runDeployResume clears the block on ONE delivery so the next Maintain tick retries its prod-deploy
// from scratch (the attempt count is reset, giving it the full budget again). This is the explicit,
// human-driven "Wiederaufnahme" a blocked delivery waits for. 404 when no such blocked delivery is
// tracked (already resumed, merged, or never blocked).
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
