package it

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"devlab/backend/internal/executor"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/telemetry"
)

// ── the fixture world ────────────────────────────────────────────────────────────────────────
//
// The integration suite substitutes exactly the three things a test machine cannot have: a real
// GitHub, a real agent process, and root. Everything between them — the motor, the chain, the
// documents, the stores, the routes — is the real implementation.

// fixtureRepo is one repository in the fixture world.
type fixtureRepo struct {
	head       string   // the workbench head
	commits    []string // commit messages in order
	published  []string // heads pushed to origin, in order
	branches   map[string]string
	files      map[string]string
	protection bool
	detection  executor.Detection
	deploy     executor.DeployOutcome
	deployErr  error
	prepared   int
	agentRuns  int
	// agentScript is the stream-json the fixture agent emits; empty ⇒ a default success stream.
	agentScript string
	agentErr    error
	// agentBlock, when non-nil, holds the agent until the channel is closed or the context ends
	// (the lever for kill, drain and slot tests).
	agentBlock chan struct{}
}

// fixtureDeps implements executor.Deps, preflight.Sources and the delivery/deploy slices over the
// fixture world.
type fixtureDeps struct {
	e  *env
	mu sync.Mutex

	repos map[string]*fixtureRepo
	prs   map[string]model.PRRef // repo+head → PR
	nextP int

	usage    []telemetry.UsageSample
	restarts int
	pauses   []string
	// findings pins the preflight observation per repo; unset ⇒ not-implemented.
	findings map[string]preflight.Finding
	// aigentic is the fixture AI service the API layer proxies to.
	aigentic *httptest.Server
}

func newFixtureDeps(e *env) *fixtureDeps {
	d := &fixtureDeps{
		e: e, repos: map[string]*fixtureRepo{}, prs: map[string]model.PRRef{},
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

// repo returns (creating on demand) the fixture repository.
func (d *fixtureDeps) repo(name string) *fixtureRepo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.repoLocked(name)
}

func (d *fixtureDeps) repoLocked(name string) *fixtureRepo {
	r, ok := d.repos[name]
	if !ok {
		r = &fixtureRepo{
			head:      "c0000000",
			branches:  map[string]string{},
			files:     map[string]string{"CLAUDE.md": "# " + name + "\n"},
			detection: executor.Detection{Kind: "service", Evidence: "cmd/" + name + "d entrypoint and a service CLI"},
			deploy:    executor.DeployOutcome{Installed: true, Running: true, Port: 8123},
		}
		d.repos[name] = r
	}
	return r
}

// setFinding pins the preflight observation for one repo.
func (d *fixtureDeps) setFinding(repo string, f preflight.Finding) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.findings[repo] = f
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

func (d *fixtureDeps) Workbench(repo string) executor.WorkbenchOps {
	return &fixtureBench{d: d, name: repo}
}

func (d *fixtureDeps) Agent(ctx context.Context, repo, prompt string, _ runs.ResolvedTuning, resumeID string) (executor.AgentStream, error) {
	r := d.repo(repo)
	d.mu.Lock()
	r.agentRuns++
	script, aerr, block := r.agentScript, r.agentErr, r.agentBlock
	d.mu.Unlock()
	if script == "" {
		script = defaultAgentScript
	}
	return &fixtureAgent{ctx: ctx, out: strings.NewReader(script), err: aerr, block: block, prompt: prompt, resumeID: resumeID}, nil
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

func (d *fixtureDeps) GitHub() executor.GitHubOps { return fixtureGitHub{d: d} }
func (d *fixtureDeps) Deliver() executor.DeliverOps {
	return fixtureDeliver{d: d}
}
func (d *fixtureDeps) Deploy() executor.DeployOps { return fixtureDeploy{d: d} }

func (d *fixtureDeps) Preflight(_ context.Context, repo string, _ runs.Run) (preflight.Finding, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f, ok := d.findings[repo]; ok {
		if f.Err != "" {
			return f, errors.New(f.Err)
		}
		return f, nil
	}
	return preflight.Finding{
		State:      model.TaskNotImplemented,
		Evidence:   []string{"no delivery of this run at " + repo + ", workbench level with the default branch"},
		ObservedAt: time.Now().UTC(),
	}, nil
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

// ── preflight.Sources (the boot reconciliation reads through this) ────────────────────────────

func (d *fixtureDeps) WorkbenchState(_ context.Context, repo string) (bool, string, error) {
	r := d.repo(repo)
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(r.commits) > 0, r.head, nil
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

func (d *fixtureDeps) OpenPRByHead(_ context.Context, repo, head string) (*model.PRRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if pr, ok := d.prs[repo+"#"+head]; ok {
		return &pr, nil
	}
	return nil, nil
}

func (d *fixtureDeps) ContainedInDefault(_ context.Context, repo, commit string) (bool, error) {
	r := d.repo(repo)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, b := range r.branches {
		if b == commit {
			return true, nil
		}
	}
	return r.head == commit, nil
}

// ── the fixture workbench (K-1 invariants hold: nothing here ever discards a commit) ─────────

type fixtureBench struct {
	d    *fixtureDeps
	name string
}

func (b *fixtureBench) r() *fixtureRepo { return b.d.repo(b.name) }

func (b *fixtureBench) Prepare(context.Context) (executor.PrepareInfo, error) {
	r := b.r()
	b.d.mu.Lock()
	defer b.d.mu.Unlock()
	r.prepared++
	return executor.PrepareInfo{Created: r.prepared == 1, Head: r.head}, nil
}

func (b *fixtureBench) CleanUntracked(context.Context) error { return nil }

func (b *fixtureBench) Head(context.Context) (string, error) {
	r := b.r()
	b.d.mu.Lock()
	defer b.d.mu.Unlock()
	return r.head, nil
}

func (b *fixtureBench) CommitsAhead(_ context.Context, since string) (int, error) {
	r := b.r()
	b.d.mu.Lock()
	defer b.d.mu.Unlock()
	if since == r.head {
		return 0, nil
	}
	return len(r.commits), nil
}

func (b *fixtureBench) HasUncommitted(context.Context) (bool, error) { return false, nil }

func (b *fixtureBench) CommitAll(_ context.Context, message string) (string, error) {
	r := b.r()
	b.d.mu.Lock()
	defer b.d.mu.Unlock()
	r.commits = append(r.commits, message)
	r.head = fmt.Sprintf("c%07d", len(r.commits))
	return r.head, nil
}

func (b *fixtureBench) Publish(context.Context) error {
	r := b.r()
	b.d.mu.Lock()
	defer b.d.mu.Unlock()
	r.published = append(r.published, r.head)
	return nil
}

func (b *fixtureBench) ReadFile(_ context.Context, rel string) (string, bool, error) {
	r := b.r()
	b.d.mu.Lock()
	defer b.d.mu.Unlock()
	c, ok := r.files[rel]
	return c, ok, nil
}

func (b *fixtureBench) WriteFile(_ context.Context, rel string, data []byte) error {
	r := b.r()
	b.d.mu.Lock()
	defer b.d.mu.Unlock()
	r.files[rel] = string(data)
	return nil
}

func (b *fixtureBench) BranchAt(_ context.Context, name, at string) error {
	r := b.r()
	b.d.mu.Lock()
	defer b.d.mu.Unlock()
	r.branches[name] = at
	return nil
}

func (b *fixtureBench) PushBranch(context.Context, string) error { return nil }

func (b *fixtureBench) MergeBaseDefault(context.Context) (string, error) { return "c0000000", nil }

// ── the fixture agent ────────────────────────────────────────────────────────────────────────

type fixtureAgent struct {
	ctx      context.Context
	out      io.Reader
	err      error
	block    chan struct{}
	prompt   string
	resumeID string
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

// ── the fixture GitHub / delivery / deploy slices ─────────────────────────────────────────────

type fixtureGitHub struct{ d *fixtureDeps }

func (g fixtureGitHub) DefaultBranch(_ context.Context, repo string) (string, error) {
	g.d.mu.Lock()
	_, known := g.d.repos[repo]
	g.d.mu.Unlock()
	if !known {
		return "", &fixtureHTTPError{code: 404, msg: "repository not found"}
	}
	return "main", nil
}

func (g fixtureGitHub) CreateRepo(_ context.Context, repo string, _ bool) error {
	g.d.repo(repo)
	return nil
}

type fixtureDeliver struct{ d *fixtureDeps }

func (f fixtureDeliver) NextPRBase(_ context.Context, _ string) (string, error) { return "main", nil }

func (f fixtureDeliver) OpenOrAdoptPR(_ context.Context, in executor.DeliverPRIn) (model.PRRef, bool, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	key := in.Repo + "#" + in.Head
	if pr, ok := f.d.prs[key]; ok {
		return pr, true, nil // adoption, never a duplicate (K-6)
	}
	f.d.nextP++
	pr := model.PRRef{Number: f.d.nextP, URL: "https://github.example/" + in.Repo + "/pull/" + fmt.Sprint(f.d.nextP), HeadBranch: in.Head}
	f.d.prs[key] = pr
	// The ledger record of the delivery (REQ-024) — the intent the chain writes before the PR.
	_ = f.d.e.deliveries.Put(runs.Delivery{
		ID: in.DeliveryID, ExecutionID: in.ExecutionID,
		Repo: in.Repo, Branch: in.Head, FromCommit: in.FromCommit, ToCommit: in.ToCommit,
		PRNumber: pr.Number, PRURL: pr.URL, CreatedAt: time.Now().UTC(),
	})
	return pr, false, nil
}

func (f fixtureDeliver) EnsureProtection(_ context.Context, repo string) error {
	r := f.d.repo(repo)
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	r.protection = true
	return nil
}

type fixtureDeploy struct{ d *fixtureDeps }

func (f fixtureDeploy) Detect(_ context.Context, repo string) (executor.Detection, error) {
	r := f.d.repo(repo)
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	return r.detection, nil
}

func (f fixtureDeploy) DeliverDev(_ context.Context, repo string) (executor.DeployOutcome, error) {
	r := f.d.repo(repo)
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if r.deployErr != nil {
		return executor.DeployOutcome{}, r.deployErr
	}
	return r.deploy, nil
}

// fixtureHTTPError carries a GitHub status code so faultclass classifies it the way the real
// typed client errors are classified.
type fixtureHTTPError struct {
	code int
	msg  string
}

func (e *fixtureHTTPError) Error() string   { return fmt.Sprintf("github: %d: %s", e.code, e.msg) }
func (e *fixtureHTTPError) StatusCode() int { return e.code }

// runIDOf resolves the run behind an execution id (the ledger record needs it).
func (e *env) runIDOf(execID string) string {
	if d, ok, err := e.docs.Get(execID); err == nil && ok {
		return d.RunID
	}
	return ""
}
