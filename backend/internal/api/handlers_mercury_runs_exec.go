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
	deployWrapper   = "/usr/local/sbin/devlab-deploy"
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

func (x *runExecutor) Execute(ctx context.Context, run runs.Run) (runs.ResultRef, error) {
	// Resume an execution suspended on the usage limit (same ResultID, skip the repos already done), or
	// start a fresh one. A resume that can't find its open result silently starts fresh.
	res, resuming := x.resumeOrNew(run)
	res.Suspended, res.ResumeAt = false, nil // recomputed below if we hit the limit again
	save := func() { _ = x.s.runResults.Save(res) }

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

	// An auto run sweeps every Holistic repo; a ToDo hits exactly one (existing, or newly created).
	var repos []model.Repo
	if run.IsTodo() {
		target, terr := x.todoTarget(ctx, run, token)
		if terr != nil {
			if isInfraError(terr) {
				return carryOver("Ziel-Repo (Netz/DNS): " + terr.Error())
			}
			return fail(terr.Error())
		}
		repos = []model.Repo{target}
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
		rr, lim := x.executeRepo(ctx, run, repo, token, ghLogin, ghID)
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
			Mode: x.mode, StartedAt: start.UTC(), PromptHash: run.PromptHash}
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

func (x *runExecutor) executeRepo(ctx context.Context, run runs.Run, repo model.Repo, token, ghLogin string, ghID int64) (runs.RepoResult, repoSignal) {
	rr := runs.RepoResult{Repo: repo.ID}
	step := func(name, logtxt string, ok bool) {
		rr.Steps = append(rr.Steps, runs.Step{Name: name, OK: ok, Log: clip(logtxt), At: time.Now().UTC()})
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
		out, err := ex.Agent(actx, wt, agentArgs(run.Prompt, "plan")...)
		if lim := detectLimit(out, err); lim.limited {
			return rr, lim
		}
		if err != nil {
			step("analyze", agentError(err), false)
			rr.Error = "analyze: " + err.Error()
			return rr, repoSignal{}
		}
		step("analyze", parseClaudeResult(out).Output, true) // the agent's report (per the Bericht Laufregel)
		applyUsage(&rr, out)
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

	out, err := ex.Agent(actx, wt, agentArgs(run.Prompt, "bypassPermissions")...)
	if lim := detectLimit(out, err); lim.limited {
		return rr, lim
	}
	if err != nil {
		step("implement", agentError(err), false)
		rr.Error = "implement: " + err.Error()
		return rr, repoSignal{}
	}
	step("implement", parseClaudeResult(out).Output, true) // the agent's report (per the Bericht Laufregel)
	applyUsage(&rr, out)

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
	if x.mode == "full" && isSelfRepo(repo.ID) {
		step("dev-deploy", "Selbst-Deploy übersprungen — "+repo.ID+" wird nicht aus seinem eigenen Lauf heraus neugestartet (PR steht, Deploy out-of-band)", true)
	} else if x.shouldDevDeploy(repo.ID) {
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
// full mode, and NEVER for the self repo (Finding B — dev-deploying devlab restarts THIS devlabd,
// killing the running sweep). report/pr never dev-deploy at all.
func (x *runExecutor) shouldDevDeploy(repoID string) bool {
	return x.mode == "full" && !isSelfRepo(repoID)
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

// todoTarget resolves a ToDo's single target repo: an existing Holistic repo (by id or name), or a
// newly created one for a newly planned service (created in the holistic owner/topic namespace, so
// it immediately belongs to the set).
func (x *runExecutor) todoTarget(ctx context.Context, run runs.Run, token string) (model.Repo, error) {
	if run.NewRepo != "" {
		full, err := github.CreateRepo(ctx, token, discover.Owner(), run.NewRepo,
			"Holistic-Service — angelegt vom Mercury-ToDo \""+run.Name+"\"", discover.Topic())
		if err != nil {
			return model.Repo{}, fmt.Errorf("Repo %q anlegen: %w", run.NewRepo, err)
		}
		return model.Repo{ID: run.NewRepo, Name: run.NewRepo, FullName: full}, nil
	}
	repos, err := discover.ReposForUser(ctx, x.tokenUser, token)
	if err != nil {
		return model.Repo{}, fmt.Errorf("Repos ermitteln: %w", err)
	}
	for _, r := range repos {
		if r.ID == run.Repo || r.Name == run.Repo {
			return r, nil
		}
	}
	return model.Repo{}, fmt.Errorf("Ziel-Repo %q nicht gefunden", run.Repo)
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

// buildArtifact compiles repo's deployable artifact IN ITS WORKSPACE, as the UNPRIVILEGED runner
// process itself (os/exec as the current user — NEVER root, and never through the deploy wrapper). This
// is Finding C: building agent-written code as root would be an RCE-to-root — an npm postinstall hook
// or a go generate step would run as root and could read the root-only prod deploy key. So the
// privileged wrapper only ever installs a PREBUILT artifact. The result is an artifact directory UNDER
// the workspace (<wt>/.mercury-artifact): the Go daemon binary plus the built web assets in web/. Its
// path resolves under /var/lib/devlab/workspaces/, which the root wrapper re-checks with realpath
// before installing.
//
// The recipe mirrors .sxgate/preview.conf: the web SPA (npm ci, or install when there is no lockfile,
// then npm run build) and the Go backend (CGO_ENABLED=0 go build). Each half runs only when its
// manifest is present, so a web-only or backend-only service still builds; a present half that fails
// to build is a hard error (executeRepo treats a dev-deploy build failure as non-fatal upstream).
func (x *runExecutor) buildArtifact(ctx context.Context, wt string, repo model.Repo) (string, error) {
	artifactDir := filepath.Join(wt, ".mercury-artifact")
	if err := os.RemoveAll(artifactDir); err != nil {
		return "", fmt.Errorf("Artefakt-Verzeichnis leeren: %w", err)
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", fmt.Errorf("Artefakt-Verzeichnis anlegen: %w", err)
	}
	built := false

	// Web SPA: npm ci (fall back to install when there is no lockfile) then npm run build; collect the
	// build output (dist/ or build/) into <artifactDir>/web.
	if fileExists(filepath.Join(wt, "package.json")) {
		if out, err := x.runBuild(ctx, wt, nil, "npm", "ci", "--no-audit", "--no-fund"); err != nil {
			if out2, err2 := x.runBuild(ctx, wt, nil, "npm", "install", "--no-audit", "--no-fund"); err2 != nil {
				return "", fmt.Errorf("npm install: %v: %s", err2, clip(out+out2))
			}
		}
		if out, err := x.runBuild(ctx, wt, []string{"CI=true"}, "npm", "run", "build"); err != nil {
			return "", fmt.Errorf("npm run build: %v: %s", err, clip(out))
		}
		src := ""
		for _, cand := range []string{"dist", "build"} {
			if dirExists(filepath.Join(wt, cand)) {
				src = filepath.Join(wt, cand)
				break
			}
		}
		if src != "" {
			if out, err := x.runBuild(ctx, wt, nil, "cp", "-a", src, filepath.Join(artifactDir, "web")); err != nil {
				return "", fmt.Errorf("Web-Assets sammeln: %v: %s", err, clip(out))
			}
			built = true
		}
	}

	// Go backend: CGO_ENABLED=0 go build of the command package(s) into the artifact dir. Prefer a
	// backend/ module (the uniform Holistic layout), else a module at the repo root.
	goModDir := ""
	if fileExists(filepath.Join(wt, "backend", "go.mod")) {
		goModDir = filepath.Join(wt, "backend")
	} else if fileExists(filepath.Join(wt, "go.mod")) {
		goModDir = wt
	}
	if goModDir != "" {
		target := "./cmd/..."
		if !dirExists(filepath.Join(goModDir, "cmd")) {
			target = "./..." // no cmd/ tree — build whatever main packages exist
		}
		// Trailing separator on -o makes go write each main package's binary INTO artifactDir.
		if out, err := x.runBuild(ctx, goModDir, []string{"CGO_ENABLED=0"},
			"go", "build", "-o", artifactDir+string(os.PathSeparator), target); err != nil {
			return "", fmt.Errorf("go build: %v: %s", err, clip(out))
		}
		built = true
	}

	if !built {
		return "", fmt.Errorf("kein Build-Rezept erkannt (weder package.json noch go.mod) für %s", repo.ID)
	}
	return artifactDir, nil
}

// runBuild runs one build command as the runner PROCESS ITSELF (current user), rooted at dir. It never
// uses sudo or the devlab-exec wrapper — the build must stay unprivileged (Finding C). Combined output
// is returned for the step log.
func (x *runExecutor) runBuild(ctx context.Context, dir string, extraEnv []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// prodDeployMerged ships a MERGED run PR to prod. It materializes the merged default branch in the
// runner's workspace (Ensure + Lock + ResetToRemote), builds the artifact UNPRIVILEGED (Finding C),
// then hands it to the deploy wrapper with env=prod (which ships the prebuilt artifact to the VPS and
// installs+restarts there). The self repo is fine here — this restarts the VPS devlabd, not the dev
// runner. A nil error means prod is live on the merged code; Maintain then untracks the PR.
func (x *runExecutor) prodDeployMerged(ctx context.Context, token string, p runs.PendingPR) (string, error) {
	name := p.Repo
	if i := strings.LastIndex(p.Repo, "/"); i >= 0 {
		name = p.Repo[i+1:]
	}
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
