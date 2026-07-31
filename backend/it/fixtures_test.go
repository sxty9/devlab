package it

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/api"
	"devlab/backend/internal/deliver"
	"devlab/backend/internal/deploy"
	"devlab/backend/internal/executor"
	"devlab/backend/internal/github"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/telemetry"
	"devlab/backend/internal/workbench"
	"devlab/backend/internal/workspace"
)

// ── the fixture world ────────────────────────────────────────────────────────────────────────
//
// The integration suite substitutes exactly the three things a test machine cannot have: a real
// GitHub, a real agent process, and root. Everything between them — the motor, the chain, the
// documents, the stores, the routes, the WORKING STATE MACHINE and the DELIVERY PATH — is the real
// implementation.
//
// That last part is the point of this file, and it is a rule with teeth: a fixture may only stand at
// an I/O EDGE. It may not re-implement an invariant the acceptance matrix asks about, because an
// invariant proven by the fixture is not proven at all — the shipped code could lose it and this
// suite would stay green. Concretely:
//
//   - the working state runs on the SHIPPED workbench (workbench.Bench) over a REAL git remote, so
//     "work is folded in, never reset" (K-1) is a git fact here, not a property of a mock that has
//     no reset to begin with;
//   - pull requests run through the SHIPPED deliver.OpenOrAdoptPR, so adoption-instead-of-duplicate
//     (K-6) is proven at the code that does it;
//   - the fixture GitHub is a REST surface only: it stores pull requests and protection and answers
//     with the typed *github.StatusError the production client raises, so the ONE classification
//     point (faultclass, K-5) really decides permanent versus transient;
//   - repository kind DETECTION reads the real repository (deploy.Detect) — a skip must rest on an
//     attested repo property (K-4), and a fixture that hands out the verdict would attest nothing.
//
// What remains a fixture: the GitHub REST calls, the agent process, and installing as root.

// ── the persistent git reality ───────────────────────────────────────────────────────────────

// gitWorld is the git reality behind the fixture world: per repository a bare origin (what GitHub
// holds) and the runner's working clone (what the workspace holds). It lives OUTSIDE the daemon
// process state, so a reboot in this suite finds the same repositories with the same commits the
// killed process left behind — exactly as in production.
type gitWorld struct {
	mu     sync.Mutex
	dir    string
	repos  map[string]*gitRepo
	shapes map[string]string // repo → "service" (default) | "library"
	// gh is the fixture GitHub's own state. It lives here, beside the repositories, because
	// GitHub does not forget a pull request when the daemon restarts.
	gh *ghWorld
}

// ghWorld is the persistent state of the fixture GitHub: pull requests, protection, statuses. Its
// own mutex is never held across a git operation.
type ghWorld struct {
	mu         sync.Mutex
	prs        map[string]*fixturePR
	next       int
	protection map[string]deliver.Protection
	statuses   map[string]string
}

// gitRepo is one repository's git reality plus the SHIPPED bench that operates it.
type gitRepo struct {
	name   string
	origin string
	wt     string
	bench  *workbench.Bench
}

// worldRegistry keys the git worlds by their directory, so a REBOOT (same state root, hence the same
// world directory) finds the very same world the killed process used — GitHub does not forget its
// pull requests when a daemon restarts, and neither may this fixture. Each test owns its own
// directory (t.TempDir), so the registry never crosses tests.
var worldRegistry = struct {
	mu     sync.Mutex
	worlds map[string]*gitWorld
}{worlds: map[string]*gitWorld{}}

func openGitWorld(dir string) *gitWorld {
	worldRegistry.mu.Lock()
	defer worldRegistry.mu.Unlock()
	if w, ok := worldRegistry.worlds[dir]; ok {
		return w
	}
	w := &gitWorld{dir: dir, repos: map[string]*gitRepo{}, shapes: map[string]string{},
		gh: &ghWorld{prs: map[string]*fixturePR{}, protection: map[string]deliver.Protection{},
			statuses: map[string]string{}}}
	worldRegistry.worlds[dir] = w
	return w
}

// shape pins the repository layout used the first time the repo is created (a service by default).
func (w *gitWorld) shape(name, kind string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shapes[name] = kind
}

// get returns the repository's git reality — creating it on first use, REOPENING it after a reboot.
func (w *gitWorld) get(name string) (*gitRepo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r, ok := w.repos[name]; ok {
		return r, nil
	}
	root := filepath.Join(w.dir, name)
	r := &gitRepo{
		name:   name,
		origin: filepath.Join(root, "origin.git"),
		wt:     filepath.Join(root, "work"),
	}
	if _, err := os.Stat(r.origin); err != nil {
		if err := seedGitRepo(r, w.shapes[name]); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(filepath.Join(r.wt, ".git")); err != nil {
		if err := gitQuiet("", "clone", "--quiet", r.origin, r.wt); err != nil {
			return nil, err
		}
		// The commit identity lives in the CLONE's own config: the workbench commits without an
		// identity argument when no GitHub account is linked, and a hermetic test has no global
		// git config to fall back on.
		if err := gitQuiet(r.wt, "config", "user.name", "fixture-runner"); err != nil {
			return nil, err
		}
		if err := gitQuiet(r.wt, "config", "user.email", "runner@fixture.invalid"); err != nil {
			return nil, err
		}
		if err := gitQuiet(r.wt, "config", "commit.gpgsign", "false"); err != nil {
			return nil, err
		}
	}
	b, err := workbench.New(&workspace.Executor{PerUser: false}, r.wt)
	if err == nil {
		b, err = b.On(workbench.LegacyShared)
	}
	if err != nil {
		return nil, err
	}
	r.bench = b
	w.repos[name] = r
	return r, nil
}

// originHead resolves a ref in the ORIGIN — "what is published", asked of the remote itself.
func (r *gitRepo) originHead(ref string) string {
	out, err := gitOutput("", "--git-dir="+r.origin, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// seedGitRepo creates the bare origin with one seeded commit on the default branch. A "service"
// shape carries the uniform ./service CLI (so deploy.Detect attests a service); a "library" shape
// carries code without a deliverable daemon.
func seedGitRepo(r *gitRepo, shape string) error {
	if err := gitQuiet("", "init", "--quiet", "--bare", "--initial-branch=main", r.origin); err != nil {
		return err
	}
	seed := r.wt + ".seed"
	defer func() { _ = os.RemoveAll(seed) }()
	if err := gitQuiet("", "clone", "--quiet", r.origin, seed); err != nil {
		return err
	}
	files := map[string]string{
		"CLAUDE.md":  "# " + r.name + "\n\nthe repository's own notes\n",
		"go.mod":     "module " + r.name + "\n\ngo 1.24\n",
		"lib/lib.go": "package lib\n\n// Answer is the seeded content.\nfunc Answer() int { return 42 }\n",
	}
	if shape != "library" {
		files["service"] = "#!/usr/bin/env bash\n# the uniform service CLI\nexit 0\n"
	}
	for rel, body := range files {
		p := filepath.Join(seed, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if rel == "service" {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			return err
		}
	}
	if err := gitQuiet(seed, "add", "-A"); err != nil {
		return err
	}
	if err := gitQuiet(seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "seed"); err != nil {
		return err
	}
	return gitQuiet(seed, "push", "--quiet", "origin", "HEAD:main")
}

// gitQuiet runs git in dir, reporting its output as the error detail.
func gitQuiet(dir string, args ...string) error {
	_, err := gitOutput(dir, args...)
	return err
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// ── the per-execution knobs ──────────────────────────────────────────────────────────────────

// fixtureRepo holds what the SUBSTITUTED edges do for one repository — the agent's behaviour and
// the host's answers. Everything git-shaped is a fact of gitWorld, not a field here. The knobs are
// per process (per env): a reboot resets them, exactly as a restarted daemon forgets what its
// predecessor's agent did.
type fixtureRepo struct {
	// deployOut/deployErr are what installing on the host answers (the root edge).
	deployOut executor.DeployOutcome
	deployErr error
	// detection overrides the REAL repository detection — used only where a test needs a detector
	// answer the repository itself cannot produce (e.g. a verdict without evidence).
	detection *executor.Detection
	detectErr error

	agentRuns int
	// agentScript is the stream-json the fixture agent emits; empty ⇒ a default success stream.
	agentScript string
	agentErr    error
	// agentBlock, when non-nil, holds the agent until the channel is closed or the context ends
	// (the lever for kill, drain and slot tests).
	agentBlock chan struct{}
	// agentWrite pins what the agent leaves in the working tree; unset ⇒ one uniquely named file,
	// so every invocation really does leave work for the chain to commit.
	agentWrite map[string]string
	// lastWrite names the files the last invocation left behind (so a test can look for them).
	lastWrite []string
}

// fixtureDeps implements executor.Deps, preflight.Sources and the delivery/deploy slices over the
// fixture world.
type fixtureDeps struct {
	e  *env
	mu sync.Mutex

	world *gitWorld
	repos map[string]*fixtureRepo

	// ghFault injects a failure into ONE GitHub operation ("CreatePullRequest", "MergePullRequest",
	// …) — the input the ONE classification point (faultclass) then classifies.
	ghFault map[string]error
	ghCalls map[string]int

	usage    []telemetry.UsageSample
	restarts int
	pauses   []string
	// findings pins the preflight observation per repo; unset ⇒ derived from the repository.
	findings map[string]preflight.Finding
	// aigentic is the fixture AI service the API layer proxies to.
	aigentic *httptest.Server
}

// fixturePR is one pull request on the fixture GitHub.
type fixturePR struct {
	number   int
	repo     string
	head     string
	base     string
	state    string // "open" | "closed"
	merged   bool
	mergedAt *time.Time
	headSHA  string
	closed   string
}

func newFixtureDeps(e *env, world *gitWorld) *fixtureDeps {
	d := &fixtureDeps{
		e: e, world: world,
		repos:   map[string]*fixtureRepo{},
		ghFault: map[string]error{}, ghCalls: map[string]int{},
		findings: map[string]preflight.Finding{},
	}
	d.aigentic = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// aigentic's single /run endpoint plus its model catalogue — enough for the API layer to
		// answer end to end without any provider being reachable.
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"id": "claude-opus-4", "displayName": "Opus 4", "version": "4.0"}},
			})
			return
		}
		var body struct {
			Data struct{ Prompt string } `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "model": "claude-opus-4",
			"result": map[string]any{"text": "fixture answer"},
			"usage":  map[string]any{"inputTokens": 10, "outputTokens": 5},
		})
	}))
	e.t.Cleanup(d.aigentic.Close)
	e.t.Setenv("DEVLAB_AIGENTIC_URL", d.aigentic.URL)
	return d
}

// repo returns (registering on demand) the repository's knobs. Registration also means "this
// repository EXISTS on the fixture GitHub" — an unregistered one answers 404, which is what the
// repo-creation path needs.
func (d *fixtureDeps) repo(name string) *fixtureRepo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.repoLocked(name)
}

func (d *fixtureDeps) repoLocked(name string) *fixtureRepo {
	r, ok := d.repos[name]
	if !ok {
		r = &fixtureRepo{deployOut: executor.DeployOutcome{Installed: true, Running: true, Port: 8123}}
		d.repos[name] = r
	}
	return r
}

// libraryRepo registers a repository whose LAYOUT is a library — no deliverable daemon. The
// detection then attests that from the repository itself (K-4).
func (d *fixtureDeps) libraryRepo(name string) *fixtureRepo {
	d.world.shape(name, "library")
	return d.repo(name)
}

// git returns the repository's git reality, failing the test if it cannot be created.
func (d *fixtureDeps) git(name string) *gitRepo {
	d.repo(name) // registration: a repo the chain touches exists on the fixture GitHub
	gr, err := d.world.get(name)
	if err != nil {
		d.e.t.Fatalf("fixture git world for %s: %v", name, err)
	}
	return gr
}

// setFinding pins the preflight observation for one repo.
func (d *fixtureDeps) setFinding(repo string, f preflight.Finding) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.findings[repo] = f
}

// failGitHub injects a failure into one GitHub operation.
func (d *fixtureDeps) failGitHub(op string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ghFault[op] = err
}

// githubCalls counts how often one GitHub operation was attempted — the measurement behind "exactly
// one attempt" (K-5).
func (d *fixtureDeps) githubCalls(op string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ghCalls[op]
}

// openPRs lists the OPEN pull requests of the fixture GitHub.
func (d *fixtureDeps) openPRs() []fixturePR {
	gh := d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	out := []fixturePR{}
	for _, pr := range gh.prs {
		if pr.state == "open" {
			out = append(out, *pr)
		}
	}
	return out
}

// hold makes the repo's agent block until release (or the context ends) — the lever for kill,
// drain, slot and race tests.
func (d *fixtureDeps) hold(repo string) {
	r := d.repo(repo)
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.agentBlock == nil {
		r.agentBlock = make(chan struct{})
	}
}

// release lets a held agent finish.
func (d *fixtureDeps) release(repo string) {
	r := d.repo(repo)
	d.mu.Lock()
	defer d.mu.Unlock()
	closeOnce(&r.agentBlock)
}

// releaseAll frees every held agent (cleanup, so no goroutine outlives its test).
func (d *fixtureDeps) releaseAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.repos {
		closeOnce(&r.agentBlock)
	}
}

func closeOnce(ch *chan struct{}) {
	if *ch == nil {
		return
	}
	select {
	case <-*ch: // already closed
	default:
		close(*ch)
	}
}

// ── executor.Deps ────────────────────────────────────────────────────────────────────────────

// Workbench hands the motor the SHIPPED working-state machine of one repo. The seam has no error
// channel, so an unopenable repository answers through every method with its named reason.
func (d *fixtureDeps) Workbench(repo string) executor.WorkbenchOps {
	d.repo(repo)
	gr, err := d.world.get(repo)
	if err != nil {
		return brokenBench{err: fmt.Errorf("fixture git world for %s: %w", repo, err)}
	}
	return benchOps{b: gr.bench}
}

func (d *fixtureDeps) Agent(ctx context.Context, repo, prompt string, _ runs.ResolvedTuning, sess executor.AgentSession) (executor.AgentStream, error) {
	r := d.repo(repo)
	gr := d.git(repo)
	d.mu.Lock()
	r.agentRuns++
	nth := r.agentRuns
	script, aerr, block, pinned := r.agentScript, r.agentErr, r.agentBlock, r.agentWrite
	d.mu.Unlock()

	// The agent WORKS: it leaves its change in the runner's working tree, exactly as the real CLI
	// does. Committing and publishing it is the chain's job through the shipped workbench — which
	// is why the write is uncommitted here and never fabricated as a commit.
	writes := pinned
	if writes == nil {
		rel := fmt.Sprintf("work/%s-%d-%d.txt", repo, nth, time.Now().UnixNano())
		writes = map[string]string{rel: "implemented by the fixture agent\n"}
	}
	var wrote []string
	for rel, body := range writes {
		p := filepath.Join(gr.wt, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return nil, err
		}
		wrote = append(wrote, rel)
	}
	d.mu.Lock()
	r.lastWrite = wrote
	d.mu.Unlock()

	if script == "" {
		script = defaultAgentScript
	}
	return &fixtureAgent{ctx: ctx, out: strings.NewReader(script), err: aerr, block: block, prompt: prompt, sess: sess}, nil
}

// defaultAgentScript is one visible turn plus the authoritative final result event.
const defaultAgentScript = `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":120,"output_tokens":30},"content":[{"type":"text","text":"implemented the change"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"## implemented\n\nthe change is in place","num_turns":1,"total_cost_usd":0.02,"usage":{"input_tokens":120,"output_tokens":30}}
`

func (d *fixtureDeps) StageAttachments(_ context.Context, _ string, atts []runs.AttachmentRef) (string, func() error, error) {
	names := make([]string, 0, len(atts))
	for _, a := range atts {
		names = append(names, a.Filename)
	}
	return "attachments: " + strings.Join(names, ", "), func() error { return nil }, nil
}

func (d *fixtureDeps) GitHub() executor.GitHubOps   { return fixtureRepoOps{d: d} }
func (d *fixtureDeps) Deliver() executor.DeliverOps { return fixtureDeliver{d: d} }
func (d *fixtureDeps) Deploy() executor.DeployOps   { return fixtureDeploy{d: d} }

func (d *fixtureDeps) Preflight(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error) {
	d.mu.Lock()
	f, pinned := d.findings[repo]
	d.mu.Unlock()
	if pinned {
		if f.Err != "" {
			return f, errors.New(f.Err)
		}
		return f, nil
	}
	// Not pinned: the SHIPPED derivation over this fixture's own sources (the repository, the
	// ledger, the pull requests) — the observation is derived, never asserted.
	return preflight.Derive(ctx, d, repo, run)
}

// AxiomScope / RecordAxiomScope run on the SHIPPED join (api.ChainDeps over the real pools and the
// real constitution store): the examined stand is not an I/O edge, so a fixture that answered it
// itself would prove nothing about the code that has to carry it into the prompt.
func (d *fixtureDeps) AxiomScope(ctx context.Context, repo string, run runs.Run) string {
	return d.e.srv.ChainDeps(api.ChainHooks{}).AxiomScope(ctx, repo, run)
}

func (d *fixtureDeps) RecordAxiomScope(repo string, run runs.Run, commit string, at time.Time) error {
	return d.e.srv.ChainDeps(api.ChainHooks{}).RecordAxiomScope(repo, run, commit, at)
}

func (d *fixtureDeps) RequestRestart(by model.Actor) error {
	d.mu.Lock()
	d.restarts++
	d.mu.Unlock()
	_, err := d.e.sch.RequestRestart(by)
	return err
}

func (d *fixtureDeps) PauseAllUsageLimit(msg string, notBefore time.Time) error {
	d.mu.Lock()
	d.pauses = append(d.pauses, msg)
	d.mu.Unlock()
	return d.e.sch.PauseAllUsageLimit(msg, notBefore)
}

func (d *fixtureDeps) RecordAiUsage(u telemetry.UsageSample) {
	d.mu.Lock()
	d.usage = append(d.usage, u)
	d.mu.Unlock()
}

func (d *fixtureDeps) Publish(t live.Topic) { d.e.broker.Publish(t) }
func (d *fixtureDeps) Now() time.Time       { return time.Now().UTC() }

// ── preflight.Sources (the boot reconciliation and the admission gate read through this) ──────

// WorkbenchState asks the SHIPPED bench whether the working state carries work the default branch
// does not — a git fact, not a fixture claim. The remote refs are refreshed best-effort first, the
// way the production observation adapter does: an observation against a stale default branch would
// report work as undelivered long after it was merged.
func (d *fixtureDeps) WorkbenchState(ctx context.Context, repo string) (bool, string, error) {
	gr, err := d.world.get(repo)
	if err != nil {
		return false, "", err
	}
	d.refreshRefs(ctx, gr)
	return gr.bench.AheadOfDefault(ctx)
}

// refreshRefs updates the remote-tracking refs of the observation clone (best-effort: an
// unreachable remote must never turn an observation into a failure).
func (d *fixtureDeps) refreshRefs(ctx context.Context, gr *gitRepo) {
	ex := workspace.Executor{PerUser: false}
	_ = ex.Fetch(ctx, gr.wt, "")
}

// RunDeliveries joins the ledger to the run through the executions — the ledger record itself
// names only the execution, so the run is resolved exactly the way production must resolve it.
func (d *fixtureDeps) RunDeliveries(runID, repo string) ([]runs.Delivery, error) {
	all, err := d.e.deliveries.All()
	if err != nil {
		return nil, err
	}
	out := []runs.Delivery{}
	for _, del := range all {
		if del.Repo != repo {
			continue
		}
		if d.e.runIDOf(del.ExecutionID) != runID {
			continue
		}
		out = append(out, del)
	}
	return out, nil
}

// PriorImplementAt answers from the SHIPPED execution archive — the run-scoped reading of the
// shared workbench, resolved exactly the way production resolves it.
func (d *fixtureDeps) PriorImplementAt(runID, repo string) (bool, error) {
	prior, err := d.e.results.ForRun(runID)
	if err != nil {
		return false, err
	}
	for _, res := range prior {
		for _, rp := range res.Repos {
			// A rest-path implement created nothing and must not count itself as work — same
			// reading as production.
			if rp.Repo != repo || rp.TaskState == model.TaskImplementedUndelivered {
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

func (d *fixtureDeps) OpenPRByHead(_ context.Context, repo, head string) (*model.PRRef, error) {
	gh := d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	for _, pr := range gh.prs {
		if pr.repo == repo && pr.head == head && pr.state == "open" {
			return &model.PRRef{Number: pr.number, URL: prURL(pr.repo, pr.number), HeadBranch: pr.head}, nil
		}
	}
	return nil, nil
}

// ContainedInDefault asks the SHIPPED bench whether a commit arrived in the default branch — the
// honest "already delivered" probe over real git history.
func (d *fixtureDeps) ContainedInDefault(ctx context.Context, repo, commit string) (bool, error) {
	gr, err := d.world.get(repo)
	if err != nil {
		return false, err
	}
	d.refreshRefs(ctx, gr)
	return gr.bench.ContainedInDefault(ctx, commit)
}

// ── the workbench seam: the SHIPPED bench, adapted to the motor's shape ───────────────────────

// benchOps maps the motor's WorkbenchOps onto workbench.Bench, exactly as cmd/devlabd's production
// adapter does. It adds NO behaviour: every invariant K-1 asks about is the bench's.
type benchOps struct{ b *workbench.Bench }

func (o benchOps) Prepare(ctx context.Context, branch, base string) (executor.PrepareInfo, error) {
	res, err := o.b.Prepare(ctx, branch, base)
	return executor.PrepareInfo{
		Created:       res.Created,
		FoldedRemote:  res.FoldedRemote,
		FoldedDefault: res.FoldedDefault,
		Conflicted:    res.Conflicted,
		ConflictFiles: res.ConflictFiles,
		Head:          res.Head,
	}, err
}

func (o benchOps) CleanUntracked(ctx context.Context) error { return o.b.CleanUntracked(ctx) }
func (o benchOps) Head(ctx context.Context) (string, error) { return o.b.Head(ctx) }

func (o benchOps) CommitsAhead(ctx context.Context, since string) (int, error) {
	return o.b.CommitsAhead(ctx, since)
}
func (o benchOps) HasUncommitted(ctx context.Context) (bool, error) { return o.b.HasUncommitted(ctx) }

func (o benchOps) CommitAll(ctx context.Context, message string) (string, error) {
	return o.b.CommitAll(ctx, message, "", 0)
}
func (o benchOps) Publish(ctx context.Context) error { return o.b.Publish(ctx) }

func (o benchOps) ReadFile(_ context.Context, rel string) (string, bool, error) {
	return o.b.ReadFile(rel)
}
func (o benchOps) WriteFile(_ context.Context, rel string, data []byte) error {
	return o.b.WriteFile(rel, data)
}
func (o benchOps) BranchAt(ctx context.Context, name, at string) error {
	return o.b.BranchAt(ctx, name, at)
}
func (o benchOps) PushBranch(ctx context.Context, name string) error {
	return o.b.PushBranch(ctx, name)
}
func (o benchOps) MergeBaseDefault(ctx context.Context) (string, error) {
	return o.b.MergeBaseDefault(ctx)
}

// brokenBench answers every operation with the reason the repository could not be opened — the
// motor then FAILS the stage honestly instead of skipping it (K-4).
type brokenBench struct{ err error }

func (x brokenBench) Prepare(context.Context, string, string) (executor.PrepareInfo, error) {
	return executor.PrepareInfo{}, x.err
}
func (x brokenBench) CleanUntracked(context.Context) error              { return x.err }
func (x brokenBench) Head(context.Context) (string, error)              { return "", x.err }
func (x brokenBench) CommitsAhead(context.Context, string) (int, error) { return 0, x.err }
func (x brokenBench) HasUncommitted(context.Context) (bool, error)      { return false, x.err }
func (x brokenBench) CommitAll(context.Context, string) (string, error) { return "", x.err }
func (x brokenBench) Publish(context.Context) error                     { return x.err }
func (x brokenBench) ReadFile(context.Context, string) (string, bool, error) {
	return "", false, x.err
}
func (x brokenBench) WriteFile(context.Context, string, []byte) error  { return x.err }
func (x brokenBench) BranchAt(context.Context, string, string) error   { return x.err }
func (x brokenBench) PushBranch(context.Context, string) error         { return x.err }
func (x brokenBench) MergeBaseDefault(context.Context) (string, error) { return "", x.err }

// ── the fixture agent ────────────────────────────────────────────────────────────────────────

type fixtureAgent struct {
	ctx    context.Context
	out    io.Reader
	err    error
	block  chan struct{}
	prompt string
	sess   executor.AgentSession
}

func (a *fixtureAgent) Output() io.Reader { return a.out }

func (a *fixtureAgent) Wait() error {
	if a.block != nil {
		select {
		case <-a.block:
		case <-a.ctx.Done():
			return a.ctx.Err()
		}
	}
	if a.ctx.Err() != nil {
		return a.ctx.Err()
	}
	return a.err
}

func (a *fixtureAgent) Kill() error { return nil }

// ── the fixture GitHub: a REST surface, nothing more ─────────────────────────────────────────

// prURL is the canonical pull-request URL shape (…/owner/repo/pull/n) on a documentation host, so
// no concrete instance is named anywhere in the suite.
func prURL(repo string, number int) string {
	return "https://github.example/" + repo + "/pull/" + fmt.Sprint(number)
}

// fault answers the injected failure for one operation (once per injection) and counts the attempt —
// the measurement behind "a permanent fault gets exactly ONE attempt" (K-5).
func (d *fixtureDeps) fault(op string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ghCalls[op]++
	return d.ghFault[op]
}

// fixtureRepoOps is the motor's GitHubOps slice: repository existence and creation.
type fixtureRepoOps struct{ d *fixtureDeps }

func (g fixtureRepoOps) DefaultBranch(_ context.Context, repo string) (string, error) {
	if err := g.d.fault("DefaultBranch"); err != nil {
		return "", err
	}
	g.d.mu.Lock()
	_, known := g.d.repos[repo]
	g.d.mu.Unlock()
	if !known {
		return "", &github.StatusError{Status: 404, Msg: "repository not found"}
	}
	return "main", nil
}

// CreateRepo runs the SHIPPED creation path: create AND protect in the same pass (REQ-033.6), so a
// protection failure really fails the creation here too.
func (g fixtureRepoOps) CreateRepo(ctx context.Context, repo string, _ bool) error {
	_, err := deliver.CreateProtectedRepo(ctx, fixtureGH{d: g.d}, repo)
	return err
}

// fixtureGH is the in-memory GitHub REST surface (the I/O edge). It holds pull requests, protection
// and statuses, and it answers with the typed *github.StatusError the production client raises, so
// faultclass classifies here exactly as in production.
type fixtureGH struct{ d *fixtureDeps }

func (g fixtureGH) key(repo string, number int) string { return fmt.Sprintf("%s#%d", repo, number) }

func (g fixtureGH) CreatePullRequest(_ context.Context, repo, head, base, _, _ string) (model.PRRef, error) {
	if err := g.d.fault("CreatePullRequest"); err != nil {
		return model.PRRef{}, err
	}
	gr, err := g.d.world.get(repo)
	if err != nil {
		return model.PRRef{}, err
	}
	sha := gr.originHead("refs/heads/" + head)
	if sha == "" {
		// GitHub answers 422 when the head branch was never pushed — the permanent class.
		return model.PRRef{}, &github.StatusError{Status: 422, Msg: "head branch " + head + " does not exist on the remote"}
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	gh.next++
	pr := &fixturePR{number: gh.next, repo: repo, head: head, base: base, state: "open", headSHA: sha}
	gh.prs[g.key(repo, pr.number)] = pr
	return model.PRRef{Number: pr.number, URL: prURL(repo, pr.number), HeadBranch: head}, nil
}

func (g fixtureGH) FindOpenPRByHead(_ context.Context, repo, head string) (*model.PRRef, error) {
	if err := g.d.fault("FindOpenPRByHead"); err != nil {
		return nil, err
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	for _, pr := range gh.prs {
		if pr.repo == repo && pr.head == head && pr.state == "open" {
			return &model.PRRef{Number: pr.number, URL: prURL(repo, pr.number), HeadBranch: pr.head}, nil
		}
	}
	return nil, nil
}

func (g fixtureGH) GetPullRequest(_ context.Context, repo string, number int) (deliver.PRState, error) {
	if err := g.d.fault("GetPullRequest"); err != nil {
		return deliver.PRState{}, err
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	pr, ok := gh.prs[g.key(repo, number)]
	if !ok {
		return deliver.PRState{}, &github.StatusError{Status: 404, Msg: "pull request not found"}
	}
	return prState(pr), nil
}

func (g fixtureGH) ListOpenPullRequests(_ context.Context, repo string) ([]deliver.PRState, error) {
	if err := g.d.fault("ListOpenPullRequests"); err != nil {
		return nil, err
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	out := []deliver.PRState{}
	for _, pr := range gh.prs {
		if pr.repo == repo && pr.state == "open" {
			out = append(out, prState(pr))
		}
	}
	return out, nil
}

func prState(pr *fixturePR) deliver.PRState {
	return deliver.PRState{
		Number: pr.number, State: pr.state, Merged: pr.merged, MergedAt: pr.mergedAt,
		HeadRef: pr.head, HeadSHA: pr.headSHA,
	}
}

// MergePullRequest merges by FAST-FORWARDING the origin's default branch onto the pull request's
// head — a real git effect, so "the work arrived in the default branch" is a fact the startup
// reconciliation and the task-state derivation can observe (K-3/REQ-039.2).
func (g fixtureGH) MergePullRequest(_ context.Context, repo string, number int, method string) error {
	if err := g.d.fault("MergePullRequest"); err != nil {
		return err
	}
	if method != "merge" {
		return &github.StatusError{Status: 422, Msg: "merge method " + method + " is disabled on this repository"}
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	pr, ok := gh.prs[g.key(repo, number)]
	gh.mu.Unlock()
	if !ok {
		return &github.StatusError{Status: 404, Msg: "pull request not found"}
	}
	gr, err := g.d.world.get(repo)
	if err != nil {
		return err
	}
	if err := gitQuiet("", "--git-dir="+gr.origin, "update-ref", "refs/heads/"+pr.base, pr.headSHA); err != nil {
		return err
	}
	now := time.Now().UTC()
	gh.mu.Lock()
	pr.merged, pr.state, pr.mergedAt = true, "closed", &now
	gh.mu.Unlock()
	return nil
}

func (g fixtureGH) ClosePullRequest(_ context.Context, repo string, number int, reason string) error {
	if err := g.d.fault("ClosePullRequest"); err != nil {
		return err
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	pr, ok := gh.prs[g.key(repo, number)]
	if !ok {
		return &github.StatusError{Status: 404, Msg: "pull request not found"}
	}
	pr.state, pr.closed = "closed", reason
	return nil
}

// DeleteBranch deletes the ref in the ORIGIN; an already-absent branch answers 404, which the
// delivery maintenance reads as Satisfied (REQ-032.2).
func (g fixtureGH) DeleteBranch(_ context.Context, repo, branch string) error {
	if err := g.d.fault("DeleteBranch"); err != nil {
		return err
	}
	gr, err := g.d.world.get(repo)
	if err != nil {
		return err
	}
	if gr.originHead("refs/heads/"+branch) == "" {
		return &github.StatusError{Status: 404, Msg: "branch " + branch + " is already gone"}
	}
	return gitQuiet("", "--git-dir="+gr.origin, "update-ref", "-d", "refs/heads/"+branch)
}

func (g fixtureGH) CreateRepo(_ context.Context, name string, _ bool) (string, error) {
	if err := g.d.fault("CreateRepo"); err != nil {
		return "", err
	}
	g.d.repo(name)
	if _, err := g.d.world.get(name); err != nil {
		return "", err
	}
	return name, nil
}

func (g fixtureGH) ProtectDefaultBranch(_ context.Context, repo, requiredStatus string) error {
	if err := g.d.fault("ProtectDefaultBranch"); err != nil {
		return err
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	gh.protection[repo] = deliver.Protection{
		RequirePR: true, RequiredStatus: []string{requiredStatus},
		AllowForcePush: false, AllowDeletion: false, MergeMethods: []string{"merge"},
	}
	return nil
}

func (g fixtureGH) GetProtection(_ context.Context, repo string) (deliver.Protection, error) {
	if err := g.d.fault("GetProtection"); err != nil {
		return deliver.Protection{}, err
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	p, ok := gh.protection[repo]
	if !ok {
		return deliver.Protection{}, &github.StatusError{Status: 404, Msg: "branch not protected"}
	}
	return p, nil
}

func (g fixtureGH) PostCommitStatus(_ context.Context, repo, sha, statusContext, state, _ string) error {
	if err := g.d.fault("PostCommitStatus"); err != nil {
		return err
	}
	gh := g.d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	gh.statuses[repo+"@"+sha+"/"+statusContext] = state
	return nil
}

func (g fixtureGH) DefaultBranch(_ context.Context, repo string) (string, error) {
	if err := g.d.fault("DefaultBranchDeliver"); err != nil {
		return "", err
	}
	if _, err := g.d.world.get(repo); err != nil {
		return "", &github.StatusError{Status: 404, Msg: "repository not found"}
	}
	return "main", nil
}

// commitStatus reports the delivery-origin verdict posted for one commit ("" when none).
func (d *fixtureDeps) commitStatus(repo, sha string) string {
	gh := d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	return gh.statuses[repo+"@"+sha+"/"+deliver.OriginStatusContext]
}

// protectionOf reports the observed protection of one repository.
func (d *fixtureDeps) protectionOf(repo string) (deliver.Protection, bool) {
	gh := d.world.gh
	gh.mu.Lock()
	defer gh.mu.Unlock()
	p, ok := gh.protection[repo]
	return p, ok
}

// ── the delivery seam: the SHIPPED deliver package ───────────────────────────────────────────

// fixtureDeliver is the motor's DeliverOps — and it decides NOTHING. Base stacking, adoption and
// protection are the shipped functions of package deliver; this type only writes the ledger intent
// and registers the opened pull request for the auto-merge window, the two things the production
// adapter (api.chainDeliver) does around the same calls.
type fixtureDeliver struct{ d *fixtureDeps }

func (f fixtureDeliver) NextPRBase(_ context.Context, repo string) (string, error) {
	open, err := f.d.e.deliveries.Open(repo)
	if err != nil {
		return "", err
	}
	return deliver.NextPRBase(open, "main"), nil
}

func (f fixtureDeliver) OpenOrAdoptPR(ctx context.Context, in executor.DeliverPRIn) (model.PRRef, bool, error) {
	ledger := f.d.e.deliveries
	// Intent before effect: the delivery record exists BEFORE the pull request (REQ-024).
	if in.DeliveryID != "" {
		if _, ok, err := ledger.ByID(in.DeliveryID); err != nil {
			return model.PRRef{}, false, err
		} else if !ok {
			if err := ledger.Put(runs.Delivery{
				ID: in.DeliveryID, ExecutionID: in.ExecutionID,
				Repo: in.Repo, Branch: in.Head,
				FromCommit: in.FromCommit, ToCommit: in.ToCommit,
				CreatedAt: time.Now().UTC(),
			}); err != nil {
				return model.PRRef{}, false, err
			}
		}
	}
	ref, adopted, err := deliver.OpenOrAdoptPR(ctx, fixtureGH{d: f.d}, ledger, deliver.PRIn{
		Repo: in.Repo, Head: in.Head, Base: in.Base, Title: in.Title, Body: in.Body,
		DeliveryID: in.DeliveryID,
	})
	if err == nil && ref.Number > 0 && f.d.e.prs != nil {
		now := time.Now().UTC()
		_ = f.d.e.prs.Add(runs.PendingPR{
			Repo: in.Repo, Number: ref.Number, URL: ref.URL, DeliveryID: in.DeliveryID,
			CreatedAt: now, MergeBy: now.Add(f.d.e.automergeWindow()),
		})
	}
	return ref, adopted, err
}

func (f fixtureDeliver) EnsureProtection(ctx context.Context, repo string) error {
	_, err := deliver.EnsureProtection(ctx, fixtureGH{d: f.d}, repo)
	return err
}

// ── the deploy seam: the host edge (root), the repository read for real ───────────────────────

type fixtureDeploy struct{ d *fixtureDeps }

// Detect reads the REAL repository through the shipped detector: a skip must rest on an attested
// repo property (K-4/REQ-031.3), which only the repository itself can attest. The override exists
// for the one case a repository cannot produce — a detector answer WITHOUT evidence.
func (f fixtureDeploy) Detect(_ context.Context, repo string) (executor.Detection, error) {
	r := f.d.repo(repo)
	f.d.mu.Lock()
	override, oerr := r.detection, r.detectErr
	f.d.mu.Unlock()
	if oerr != nil {
		return executor.Detection{}, oerr
	}
	if override != nil {
		return *override, nil
	}
	gr, err := f.d.world.get(repo)
	if err != nil {
		return executor.Detection{}, err
	}
	det, err := deploy.Detect(gr.wt)
	if err != nil {
		return executor.Detection{}, err
	}
	return executor.Detection{Kind: string(det.Kind), Evidence: det.Evidence}, nil
}

// DeliverDev is the root edge: building and installing on a host is what a test machine cannot do.
func (f fixtureDeploy) DeliverDev(_ context.Context, repo string) (executor.DeployOutcome, error) {
	r := f.d.repo(repo)
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if r.deployErr != nil {
		return executor.DeployOutcome{}, r.deployErr
	}
	return r.deployOut, nil
}

// runIDOf resolves the run behind an execution id (the ledger record needs it).
func (e *env) runIDOf(execID string) string {
	if d, ok, err := e.docs.Get(execID); err == nil && ok {
		return d.RunID
	}
	return ""
}
