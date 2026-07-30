package it

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"devlab/backend/internal/api"
	"devlab/backend/internal/auth"
	"devlab/backend/internal/execstate"
	"devlab/backend/internal/executor"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
	"devlab/backend/internal/telemetry"
)

// ── the composed system under test ───────────────────────────────────────────────────────────

// env is the whole daemon minus main(): one state root, the real stores, the real scheduler over
// the real executor motor, the real API route table on a real HTTP listener.
type env struct {
	t     *testing.T
	paths *statepath.Paths

	docs       *execstate.Store
	runStore   *runs.Store
	results    *runs.ResultStore
	notices    *runs.NoticeStore
	deliveries *runs.DeliveryStore
	settings   *runs.SettingsStore
	usage      *telemetry.UsageLedger

	broker *live.Broker
	sch    *sched.Scheduler
	srv    *api.Server
	ts     *httptest.Server

	deps    *fixtureDeps
	gate    func(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error)
	secret  string
	user    string
	csrf    string
	adminGr string

	// ctx is the daemon's root context: cancelled on cleanup, which drains the scheduler so no
	// execution goroutine outlives its test (and its temporary state root).
	ctx    context.Context
	cancel context.CancelFunc
}

// newEnv composes the system over a fresh state root. cfg tunes the scheduler; the zero value is
// the production default.
func newEnv(t *testing.T, cfg sched.Config) *env {
	t.Helper()
	return newEnvAt(t, cfg, filepath.Join(t.TempDir(), "state"))
}

// newEnvWithConstitution composes the system over a REAL constitution repository (a local bare
// remote plus its working clone), so the axiom catalogue, the prompt composition and the automatic
// run kind are exercised for real instead of being stubbed out.
func newEnvWithConstitution(t *testing.T, cfg sched.Config) *env {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	seedConstitution(t, filepath.Dir(root))
	return newEnvAt(t, cfg, root)
}

// seedConstitution creates the constitution remote with one axiom and one run rule and points the
// daemon at it. A filesystem path is recognised as a local remote, so no credential is involved.
func seedConstitution(t *testing.T, dir string) {
	t.Helper()
	remote := filepath.Join(dir, "axioms-remote.git")
	seed := filepath.Join(dir, "axioms-seed")
	gitRun(t, "", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	gitRun(t, "", "clone", "--quiet", remote, seed)
	write := func(rel, body string) {
		p := filepath.Join(seed, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("axiome/architecture/no-redundancy.md",
		"---\nid: ax_no_redundancy\ntitel: No redundancy\n---\nNo change may create redundancy.\n")
	write("laeufe/run-rules.md",
		"---\nid: lr_reporting\ntitel: Report honestly\n---\nEvery execution reports what it did and what it did not do.\n")
	gitRun(t, seed, "add", "-A")
	gitRun(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "seed")
	gitRun(t, seed, "push", "--quiet", "origin", "HEAD:main")

	t.Setenv("DEVLAB_AXIOMS_REPO", remote)
	t.Setenv("DEVLAB_AXIOMS_DIR", filepath.Join(dir, "axioms-clone"))
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// reboot stops this process and starts a NEW one over the SAME state root — the integration
// suite's process death. Everything the next daemon knows it must read off the documents.
func (e *env) reboot(cfg sched.Config) *env {
	e.t.Helper()
	e.stop()
	e.ts.Close()
	return newEnvAt(e.t, cfg, e.paths.Root)
}

// newEnvAt composes the system over an EXISTING state root (a fresh one on the first call).
func newEnvAt(t *testing.T, cfg sched.Config, root string) *env {
	t.Helper()
	paths := &statepath.Paths{Root: root}
	if err := paths.CheckWritable(); err != nil { // boot step 1
		t.Fatal(err)
	}

	// The session secret and the admin group are the only identity inputs: the admin group is
	// resolved from a group the CURRENT account really holds, so the guard matrix is decided by
	// the token, never by the machine's group layout.
	cur, err := user.Current()
	if err != nil {
		t.Skipf("no resolvable OS account: %v", err)
	}
	e := &env{t: t, paths: paths, secret: "integration-suite-secret", user: cur.Username, csrf: "csrf-token"}
	e.adminGr = firstGroupOf(t, cur)
	e.ctx, e.cancel = context.WithCancel(context.Background())
	t.Cleanup(e.stop)

	// The secret is handed over the FILE seam, which outranks both the env value and any secret
	// the host machine happens to carry — the suite must never authenticate against a real one.
	secretFile := filepath.Join(t.TempDir(), "jwt-secret")
	if err := os.WriteFile(secretFile, []byte(e.secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOLISTIC_SECRET", e.secret)
	t.Setenv("HOLISTIC_SECRET_FILE", secretFile)
	t.Setenv("DEVLAB_PREVIEW_SECRET_FILE", "")
	t.Setenv("HOLISTIC_ADMIN_GROUP", e.adminGr)
	t.Setenv("DEVLAB_STATE_DIR", root)
	t.Setenv("DEVLAB_DEV_BYPASS_AUTH", "")
	// Every per-store env seam is left unset on purpose: the whole system must agree on ONE
	// state root (statepath), which is exactly what the acceptance criteria demand.
	for _, k := range []string{
		"DEVLAB_MERCURY_RUNS", "DEVLAB_MERCURY_RUNS_HISTORY", "DEVLAB_MERCURY_EXECUTIONS",
		"DEVLAB_MERCURY_RUNS_RESULTS", "DEVLAB_MERCURY_SETTINGS", "DEVLAB_MERCURY_NOTICES",
		"DEVLAB_AXIOMS_TOKEN_USER", "DEVLAB_RUNS_TOKEN_USER", "DEVLAB_RUNS_USER",
	} {
		t.Setenv(k, "")
	}

	verifier := auth.New()
	if !verifier.HasSecret() {
		t.Fatal("the verifier read no secret — SSO would fail closed and the suite prove nothing")
	}

	e.docs, err = execstate.Open(paths) // boot step 2
	if err != nil {
		t.Fatal(err)
	}
	e.runStore = runs.NewStore(paths)
	e.results = runs.NewResultStore(paths)
	e.notices = runs.NewNoticeStore(paths)
	e.deliveries = runs.NewDeliveryStore(paths)
	e.settings = runs.NewSettingsStore(paths, runs.Settings{MaxConcurrency: 2, DefaultTimeBudget: 3 * time.Hour})
	e.usage = telemetry.OpenUsage(paths)
	e.broker = live.NewBroker()
	e.deps = newFixtureDeps(e)

	e.gate = func(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error) {
		return e.deps.Preflight(ctx, repo, run)
	}
	e.sch = sched.New(cfg, e.docs, e.runStore, e.results, e.settings,
		func(ctx context.Context, repo string, run runs.Run) (preflight.Finding, error) {
			return e.gate(ctx, repo, run)
		},
		e.execute, nil, e.broker)
	e.sch.SetNoticeFunc(func(kind, text string) {
		_ = e.notices.Add(runs.Notice{Kind: kind, Reason: text})
	})

	e.srv = api.New(verifier, paths)
	e.srv.SetExecution(e.docs, e.sch)
	e.srv.SetBroker(e.broker)
	e.srv.SetSettings(e.settings)
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	return e
}

// stop shuts the composed system down the way SIGTERM does, IN THAT ORDER: close the root context
// (the ticks stop), then drain — which cancels each running execution and persists it interrupted.
// A held agent must EXPERIENCE the kill; releasing it first would let it finish its repo, and the
// test's "killed mid-work" premise would silently become "finished just in time". The release
// afterwards is only the safety net for a fake that ignores its context, so that no goroutine
// outlives its test and writes into a state root that no longer exists.
func (e *env) stop() {
	e.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.sch.DrainAndPersist(ctx)
	e.deps.releaseAll()
}

// firstGroupOf returns a group the account really belongs to — the suite makes THAT the admin
// group, so "has the right" and "has not" are decided by the minted token alone.
func firstGroupOf(t *testing.T, u *user.User) string {
	t.Helper()
	gids, err := u.GroupIds()
	if err != nil || len(gids) == 0 {
		t.Skipf("no resolvable groups for %s", u.Username)
	}
	for _, gid := range gids {
		if g, err := user.LookupGroupId(gid); err == nil && g.Name != "" {
			return g.Name
		}
	}
	t.Skip("no nameable group for the current account")
	return ""
}

// ── boot (ARCHITEKTUR §6.2) ──────────────────────────────────────────────────────────────────

// boot runs the reconcile steps a fresh daemon runs, in the documented order, and returns what
// each step reconciled. Steps 1/2/5 already happened in newEnv (state root, stores, listener).
type bootReport struct {
	Ghosts   int // 3. running documents turned interrupted
	Released int // 4. starts released from the restart marker
	Synced   int // 6. todos checked off by the startup reconciliation
}

func (e *env) boot(ctx context.Context) bootReport {
	e.t.Helper()
	var r bootReport
	var err error
	if r.Ghosts, err = e.docs.MarkInterruptedAtBoot(time.Now().UTC()); err != nil { // 3
		e.t.Fatalf("boot step 3 (ghost exorcism): %v", err)
	}
	if r.Released, err = e.sch.CompleteBootRestart(ctx); err != nil { // 4
		e.t.Fatalf("boot step 4 (restart completion): %v", err)
	}
	if r.Synced, err = preflight.SyncStartupTodos(ctx, e.deps, e.runStore, e.results, e.notices, e.broker); err != nil { // 6
		e.t.Fatalf("boot step 6 (startup reconciliation): %v", err)
	}
	if err := e.sch.Start(ctx); err != nil { // 7 + 8
		e.t.Fatalf("boot steps 7/8 (resume enqueue + ticks): %v", err)
	}
	return r
}

// serveReady brings the ready socket up (boot step 5's second half) and waits for it.
func (e *env) serveReady(ctx context.Context) {
	e.t.Helper()
	go func() { _ = e.srv.ServeReadySocket(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if code, err := e.readyProbe(); err == nil && (code == http.StatusNoContent || code == http.StatusLocked) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.t.Fatal("the ready socket never answered")
}

// readyProbe asks the ready socket the way the restart wrapper does (curl --unix-socket).
func (e *env) readyProbe() (int, error) {
	sock := e.paths.ReadySocket()
	c := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
		Timeout: 2 * time.Second,
	}
	res, err := c.Get("http://ready/ready")
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	return res.StatusCode, nil
}

// ── HTTP client with a real session ──────────────────────────────────────────────────────────

// token mints an access token for sub — HS256 over the same secret the verifier read.
func (e *env) token(sub string, ttl time.Duration) string {
	e.t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub, "type": "access", "exp": time.Now().Add(ttl).Unix(),
	})
	s, err := tok.SignedString([]byte(e.secret))
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// request builds a request against the live server carrying a valid session (and, for mutating
// methods, the CSRF double submit).
func (e *env) request(method, path string, body any) *http.Request {
	return e.requestAs(e.user, method, path, body)
}

// requestAs is request under a different identity — the guard matrix needs a VALID session that
// carries no DevLab right.
func (e *env) requestAs(sub, method, path string, body any) *http.Request {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rdr)
	if err != nil {
		e.t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "h_access", Value: e.token(sub, 15*time.Minute)})
	req.AddCookie(&http.Cookie{Name: "h_csrf", Value: e.csrf})
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", e.csrf)
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// do sends a request and returns status plus body.
func (e *env) do(req *http.Request) (int, []byte) {
	e.t.Helper()
	res, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

// getJSON GETs path through the guarded route table and decodes into out.
func (e *env) getJSON(path string, out any) {
	e.t.Helper()
	code, body := e.do(e.request(http.MethodGet, path, nil))
	if code != http.StatusOK {
		e.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			e.t.Fatalf("GET %s: %v (%s)", path, err, body)
		}
	}
}

// post POSTs path and returns status plus body (the caller decides what is acceptable).
func (e *env) post(path string, body any) (int, []byte) {
	e.t.Helper()
	return e.do(e.request(http.MethodPost, path, body))
}

// ── run fixtures ─────────────────────────────────────────────────────────────────────────────

// addTodo stores a todo definition aimed at repos.
func (e *env) addTodo(id, title string, repos ...string) runs.Run {
	e.t.Helper()
	r := runs.Run{
		ID: id, Kind: model.KindTodo, Title: title, Task: "do the thing",
		PromptSnapshot: "# " + title + "\n\nconstitution snapshot",
		Authorship: model.Authorship{
			Created: model.Actor{User: e.user}, CreatedAt: time.Now().UTC(),
			Updated: model.Actor{User: e.user}, UpdatedAt: time.Now().UTC(),
		},
	}
	for _, repo := range repos {
		r.Targets = append(r.Targets, runs.Target{Repo: repo})
	}
	if err := e.runStore.Put(r); err != nil {
		e.t.Fatal(err)
	}
	return r
}

// waitPhase blocks until the execution reaches want (or the test fails).
func (e *env) waitPhase(execID string, want model.ExecPhase) execstate.Doc {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, ok, err := e.docs.Get(execID)
		if err != nil {
			e.t.Fatal(err)
		}
		if ok && d.Phase == want {
			return d
		}
		time.Sleep(2 * time.Millisecond)
	}
	d, _, _ := e.docs.Get(execID)
	e.t.Fatalf("execution %s never reached %s (is %q, reason %q)", execID, want, d.Phase, d.Reason)
	return execstate.Doc{}
}

// waitFor polls cond until it holds.
func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

// activeList reads the ACTIVE executions the way the UI does — through the HTTP route.
func (e *env) activeList() (active []model.ExecutionView, restart model.RestartState) {
	e.t.Helper()
	var out struct {
		Active  []model.ExecutionView `json:"active"`
		Restart model.RestartState    `json:"restart"`
	}
	e.getJSON("/api/mercury/runs/active", &out)
	return out.Active, out.Restart
}

// ── the execution seam: the motor plus the result recorder ────────────────────────────────────

// execute is the ExecFunc the scheduler drives — the very composition cmd/devlabd performs: the
// effective tuning, the sink composed out of the document-progress half (sched) and the result
// recorder (api), and the REAL motor. Only the DEPS are substituted (no GitHub, no agent process,
// no root); recorder, sink composition and motor are the shipped implementations, so the live
// transcript and the live consumption are PROVEN here rather than simulated by a stand-in.
func (e *env) execute(ctx context.Context, doc execstate.Doc, run runs.Run) error {
	set, err := e.settings.Get()
	if err != nil {
		return err
	}
	rec := e.srv.NewResultRecorder(doc, run)
	err = executor.Execute(ctx, e.deps, executor.Request{
		Run: run, Doc: doc, Budget: runs.EffectiveTuning(run, set),
	}, api.ExecutionSink(e.sch.DocSink(doc.ID), rec))
	rec.Finish(err)
	return err
}

// ── stage-array helpers the assertions share ─────────────────────────────────────────────────

// stagesOf returns the result document's stage array for one repo.
func (e *env) stagesOf(execID, repo string) []model.StageView {
	e.t.Helper()
	res, ok, err := e.results.Get(execID)
	if err != nil || !ok {
		e.t.Fatalf("result %s: ok=%v err=%v", execID, ok, err)
	}
	for _, rp := range res.Repos {
		if rp.Repo == repo {
			return rp.Stages
		}
	}
	e.t.Fatalf("result %s carries no pipeline for %s", execID, repo)
	return nil
}

// assertAllTerminal proves the normative rule that every stage ENDS in one of the four terminal
// states — pending and running are never a resting state (REQ-030).
func assertAllTerminal(t *testing.T, where string, stages []model.StageView) {
	t.Helper()
	if len(stages) == 0 {
		t.Fatalf("%s: no stage recorded at all", where)
	}
	for _, sv := range stages {
		if !sv.State.Terminal() {
			t.Errorf("%s: stage %s rests in %q — only the four terminal states are allowed", where, sv.Stage, sv.State)
		}
		if sv.State == model.StepNotApplicable && sv.Evidence == "" {
			t.Errorf("%s: stage %s is not-applicable without attested evidence", where, sv.Stage)
		}
		if (sv.State == model.StepFailed || sv.State == model.StepNotExecuted) && sv.Reason == "" {
			t.Errorf("%s: stage %s is %s without a reason", where, sv.Stage, sv.State)
		}
	}
}

func stageNames(stages []model.StageView) string {
	parts := make([]string, 0, len(stages))
	for _, sv := range stages {
		parts = append(parts, fmt.Sprintf("%s=%s", sv.Stage, sv.State))
	}
	return strings.Join(parts, " ")
}
