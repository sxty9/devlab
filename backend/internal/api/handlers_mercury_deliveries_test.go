package api

// Delivery-surface tests (S10). These adopt the coverage of the pre-rebuild
// handlers_mercury_runs_rollback_test.go: the counter-booking DECISION logic itself now lives
// (and is tested) in package deliver — here the API surface is pinned: ledger stages, the
// execution link the surface reads openness from, the rollback endpoint's outcomes (conflict →
// todo, open → closed PR), the reversal-PR tracking for the auto-merge window, the deliberate dev
// reset over the repository name the LEDGER states, and the guards.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/deliver"
	"devlab/backend/internal/links"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workbench"
	"devlab/backend/internal/workspace"
)

// fakeDeliverOps is the fixture GitHubOps + GitSide the handler seam injects.
type fakeDeliverOps struct {
	prState  map[string]deliver.PRState
	cb       deliver.CounterBookResult
	cbCalls  int
	closed   []int
	created  []string
	statuses []string
}

func (f *fakeDeliverOps) CreatePullRequest(_ context.Context, repo, head, base, _, _ string) (model.PRRef, error) {
	f.created = append(f.created, repo+"|"+head+"|"+base)
	n := 500 + len(f.created)
	return model.PRRef{Number: n, URL: "https://github.example/" + repo + "/pull/" + itoa(n), HeadBranch: head}, nil
}
func (f *fakeDeliverOps) FindOpenPRByHead(context.Context, string, string) (*model.PRRef, error) {
	return nil, nil
}
func (f *fakeDeliverOps) GetPullRequest(_ context.Context, repo string, number int) (deliver.PRState, error) {
	if st, ok := f.prState[repo+"|"+itoa(number)]; ok {
		return st, nil
	}
	return deliver.PRState{Number: number, State: "open"}, nil
}
func (f *fakeDeliverOps) ListOpenPullRequests(context.Context, string) ([]deliver.PRState, error) {
	return nil, nil
}
func (f *fakeDeliverOps) MergePullRequest(context.Context, string, int, string) error { return nil }
func (f *fakeDeliverOps) ClosePullRequest(_ context.Context, _ string, number int, _ string) error {
	f.closed = append(f.closed, number)
	return nil
}
func (f *fakeDeliverOps) DeleteBranch(context.Context, string, string) error { return nil }
func (f *fakeDeliverOps) RetargetPullRequest(context.Context, string, int, string) error {
	return nil
}
func (f *fakeDeliverOps) CreateBranch(context.Context, string, string, string) error { return nil }
func (f *fakeDeliverOps) ReopenPullRequest(context.Context, string, int) error       { return nil }
func (f *fakeDeliverOps) BranchTip(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeDeliverOps) CreateRepo(_ context.Context, name string, _ bool) (string, error) {
	return "org/" + name, nil
}
func (f *fakeDeliverOps) ProtectDefaultBranch(context.Context, string, string) error { return nil }
func (f *fakeDeliverOps) GetProtection(context.Context, string) (deliver.Protection, error) {
	return deliver.Protection{}, nil
}
func (f *fakeDeliverOps) PostCommitStatus(_ context.Context, repo, sha, _, state, _ string) error {
	f.statuses = append(f.statuses, repo+"|"+sha+"|"+state)
	return nil
}
func (f *fakeDeliverOps) DefaultBranch(context.Context, string) (string, error) { return "main", nil }
func (f *fakeDeliverOps) CounterBook(_ context.Context, _ runs.Delivery, _ string) (deliver.CounterBookResult, error) {
	f.cbCalls++
	cb := f.cb
	if cb.DefaultBranch == "" {
		cb.DefaultBranch = "main"
	}
	return cb, nil
}
func (f *fakeDeliverOps) RedeliverDev(context.Context, string) error { return nil }
func (f *fakeDeliverOps) RateBudget() deliver.RateBudget             { return deliver.RateBudget{} }

func itoa(n int) string { return strconv.Itoa(n) }

// deliveriesServer builds a Server with temp delivery/PR/run stores and a linked runner token.
func deliveriesServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS_DELIVERIES", filepath.Join(dir, "deliveries.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_PRS", filepath.Join(dir, "prs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "executions"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "legacy"))
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(dir, "notices.json"))

	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_LINKS", filepath.Join(dir, "links"))
	t.Setenv("DEVLAB_LINK_ENC_KEY_FILE", keyPath)
	t.Setenv("DEVLAB_RUNS_USER", "runner")
	t.Setenv("DEVLAB_RUNS_TOKEN_USER", "runner")

	lstore, err := links.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lstore.Save("runner", "runner-gh", 7, "tok-runner", "repo", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	return &Server{
		v:          auth.New(),
		links:      lstore,
		runs:       runs.NewStore(nil),
		results:    runs.NewResultStore(nil),
		runPRs:     runs.NewPRStore(nil),
		deliveries: runs.NewDeliveryStore(nil),
		runNotices: runs.NewNoticeStore(nil),
	}
}

// injectDeliverOps swaps the production ops seam for the fixture for one test.
func injectDeliverOps(t *testing.T, f *fakeDeliverOps) {
	t.Helper()
	old := deliverOps
	deliverOps = func(*Server, string) deliver.GitHubOps { return f }
	t.Cleanup(func() { deliverOps = old })
}

func authedReq(method, target string, body any, user string) *http.Request {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, rd)
	return req.WithContext(context.WithValue(req.Context(), userCtxKey, &auth.User{Username: user}))
}

var tD = time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)

// TestDeliveriesListStages: the wire view derives the honest lifecycle stage per record —
// open, merged, closed, and "reverted" for a counter-booked one (its reversal carries
// reversalOf).
func TestDeliveriesListStages(t *testing.T) {
	s := deliveriesServer(t)
	m := tD.Add(time.Hour)
	c := tD.Add(2 * time.Hour)
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_open", Repo: "o/a", Branch: "fix/a-1", CreatedAt: tD})
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_merged", Repo: "o/b", Branch: "fix/b-1", CreatedAt: tD, MergedAt: &m})
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_closed", Repo: "o/c", Branch: "fix/c-1", CreatedAt: tD, ClosedAt: &c, ClosedReason: "pull request #9 was closed without merging"})
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_orig", Repo: "o/d", Branch: "fix/d-1", CreatedAt: tD, MergedAt: &m})
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_rev_orig", Repo: "o/d", Branch: "fix/revert_d-1", CreatedAt: c, ReversalOf: "dlv_orig"})

	rec := httptest.NewRecorder()
	s.runDeliveriesList(rec, authedReq(http.MethodGet, "/api/mercury/runs/deliveries", nil, "alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deliveries []model.Delivery `json:"deliveries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	stages := map[string]string{}
	for _, d := range out.Deliveries {
		stages[d.ID] = d.Stage
	}
	want := map[string]string{
		"dlv_open": "open", "dlv_merged": "merged", "dlv_closed": "closed",
		"dlv_orig": "reverted", "dlv_rev_orig": "open",
	}
	for id, stage := range want {
		if stages[id] != stage {
			t.Errorf("stage[%s] = %q, want %q", id, stages[id], stage)
		}
	}
}

// TestRollbackEndpointConflict: the conflict outcome surfaces the todo (adopted from the
// ported rollback test: no guess, nothing closed, todoId in the answer).
func TestRollbackEndpointConflict(t *testing.T) {
	s := deliveriesServer(t)
	f := &fakeDeliverOps{cb: deliver.CounterBookResult{Conflicted: true}, prState: map[string]deliver.PRState{}}
	injectDeliverOps(t, f)
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-a1", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: tD})
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_2", Repo: "o/x", Branch: "fix/two-b2", FromCommit: "c1", ToCommit: "c2", PRNumber: 6, CreatedAt: tD.Add(time.Minute)})

	req := authedReq(http.MethodPost, "/api/mercury/runs/deliveries/dlv_1/rollback", nil, "tester")
	req.SetPathValue("id", "dlv_1")
	rec := httptest.NewRecorder()
	s.runDeliveryRollback(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Outcome string `json:"outcome"`
		TodoID  string `json:"todoId"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.TodoID == "" || !strings.Contains(out.Outcome, "todo") {
		t.Fatalf("conflict must surface the todo, got %+v", out)
	}
	all, _ := s.runs.List()
	found := false
	for _, r := range all {
		if r.ID == out.TodoID && r.Kind == model.KindTodo {
			found = true
			if !strings.Contains(r.Task, "dlv_1") || !strings.Contains(r.Task, "dlv_2") {
				t.Errorf("the todo must name the delivery and the later work:\n%s", r.Task)
			}
		}
	}
	if !found {
		t.Fatalf("todo %s not created", out.TodoID)
	}
	if d, _, _ := s.deliveries.ByID("dlv_1"); d.ClosedAt != nil {
		t.Errorf("a conflicting rollback must not close the delivery: %+v", d)
	}
}

// TestRollbackEndpointOpenPR: the open direction over the API — PR closed, ledger closed,
// outcome names it (the ported test's open case).
func TestRollbackEndpointOpenPR(t *testing.T) {
	s := deliveriesServer(t)
	f := &fakeDeliverOps{
		cb:      deliver.CounterBookResult{Changed: true, Before: "c1", After: "c9"},
		prState: map[string]deliver.PRState{"o/x|5": {Number: 5, State: "open"}},
	}
	injectDeliverOps(t, f)
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-a1", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: tD})

	req := authedReq(http.MethodPost, "/api/mercury/runs/deliveries/dlv_1/rollback", nil, "tester")
	req.SetPathValue("id", "dlv_1")
	rec := httptest.NewRecorder()
	s.runDeliveryRollback(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Outcome string `json:"outcome"`
		TodoID  string `json:"todoId"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.TodoID != "" || !strings.Contains(out.Outcome, "closed PR #5") {
		t.Fatalf("open rollback outcome = %+v", out)
	}
	if len(f.closed) != 1 || f.closed[0] != 5 {
		t.Errorf("PR 5 must be closed, got %v", f.closed)
	}
	d, _, _ := s.deliveries.ByID("dlv_1")
	if d.ClosedAt == nil || !strings.Contains(d.ClosedReason, "rolled back by tester") {
		t.Errorf("ledger must record the rollback: %+v", d)
	}
}

// TestRollbackEndpointTracksReversalPR: the merged direction — the reversal PR is tracked for
// the auto-merge window with its delivery id.
func TestRollbackEndpointTracksReversalPR(t *testing.T) {
	s := deliveriesServer(t)
	m := tD.Add(time.Hour)
	f := &fakeDeliverOps{
		cb:      deliver.CounterBookResult{Changed: true, Before: "c1", After: "c9"},
		prState: map[string]deliver.PRState{"o/x|5": {Number: 5, State: "closed", Merged: true, MergedAt: &m}},
	}
	injectDeliverOps(t, f)
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-a1", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: tD, MergedAt: &m})

	req := authedReq(http.MethodPost, "/api/mercury/runs/deliveries/dlv_1/rollback", nil, "tester")
	req.SetPathValue("id", "dlv_1")
	rec := httptest.NewRecorder()
	s.runDeliveryRollback(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(f.created) != 1 {
		t.Fatalf("a merged rollback must open one reversal PR, got %v", f.created)
	}
	tracked, _ := s.runPRs.List()
	if len(tracked) != 1 {
		t.Fatalf("the reversal PR must be tracked for auto-merge, got %+v", tracked)
	}
	p := tracked[0]
	if p.Repo != "o/x" || p.DeliveryID == "" || !strings.HasPrefix(p.DeliveryID, "dlv_rev_") {
		t.Errorf("tracked PR must carry the reversal delivery id, got %+v", p)
	}
	if p.MergeBy.Before(time.Now().Add(24 * time.Hour)) {
		t.Errorf("the auto-merge window must apply (default 720h), MergeBy = %v", p.MergeBy)
	}
}

// TestRollbackEndpointGuards: unknown delivery → 404; no linked runner → an explained 400.
func TestRollbackEndpointGuards(t *testing.T) {
	s := deliveriesServer(t)
	injectDeliverOps(t, &fakeDeliverOps{prState: map[string]deliver.PRState{}})

	req := authedReq(http.MethodPost, "/api/mercury/runs/deliveries/dlv_missing/rollback", nil, "tester")
	req.SetPathValue("id", "dlv_missing")
	rec := httptest.NewRecorder()
	s.runDeliveryRollback(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown delivery: status = %d, want 404", rec.Code)
	}

	s.links = nil
	rec = httptest.NewRecorder()
	req = authedReq(http.MethodPost, "/api/mercury/runs/deliveries/dlv_1/rollback", nil, "tester")
	req.SetPathValue("id", "dlv_1")
	s.runDeliveryRollback(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing runner link: status = %d, want 400", rec.Code)
	}
}

// TestMaintainDeliveriesComposition: the composed tick runs deliver.Maintain over the seam —
// an overdue tracked PR of a recorded delivery is merged and untracked, and the ledger is
// mirrored (the endpoint the scheduler will drive).
func TestMaintainDeliveriesComposition(t *testing.T) {
	// The maintenance is HELD until the operator arms it (deliver.EnvMaintainEnforce); this test
	// drives the writing half, so it arms it explicitly.
	t.Setenv(deliver.EnvMaintainEnforce, "1")
	s := deliveriesServer(t)
	f := &fakeDeliverOps{prState: map[string]deliver.PRState{
		"o/x|5": {Number: 5, State: "closed", Merged: true, MergedAt: &tD, HeadRef: "fix/one-a1", HeadSHA: "c1"},
	}}
	injectDeliverOps(t, f)
	d := runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-a1", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: tD, ExecutionID: "exec_1"}
	_ = s.deliveries.Put(d)
	_ = s.runPRs.Add(runs.PendingPR{Repo: "o/x", Number: 5, DeliveryID: "dlv_1", CreatedAt: tD, MergeBy: tD})
	_ = s.results.Put(runs.Result{ID: "exec_1", RunID: "run_1", Kind: model.KindTodo, StartedAt: tD})

	if err := s.MaintainDeliveries(context.Background()); err != nil {
		t.Fatalf("MaintainDeliveries: %v", err)
	}
	if got, _, _ := s.deliveries.ByID("dlv_1"); got.MergedAt == nil {
		t.Errorf("the merge must be mirrored onto the ledger: %+v", got)
	}
	if left, _ := s.runPRs.List(); len(left) != 0 {
		t.Errorf("the tracked PR must be untracked, left %+v", left)
	}
	// WHAT-1: a merge is not the end — production is. After the merge the delivery owes its production
	// step, so the execution has NOT settled yet (the production pass, deliver.MaintainProd, is a no-op
	// here without a workspace manager). It settles only once production succeeds.
	if r, _, _ := s.results.Get("exec_1"); r.MergedAt != nil {
		t.Errorf("a merged delivery still owes production, so its execution must not settle on merge: %+v", r)
	}
}

// ── The deliberate dev reset over the name the LEDGER states ─────────────────────────────

// benchFixture is a hermetic workbench: a bare origin, a working tree cloned from it, and a
// workbench branch one commit ahead of the default branch. It records what the handler passed
// down, so the repository name that travels through the reset is provable.
type benchFixture struct {
	origin string
	wt     string
	repoID string
	full   string
	calls  int
}

// newBenchFixture builds the local repositories and substitutes the workbench seam with a bench
// over the working tree — no GitHub, no sudo, real git.
func newBenchFixture(t *testing.T) *benchFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Hermetic git: the operator's own config must not decide what this test observes.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	root := t.TempDir()
	f := &benchFixture{origin: filepath.Join(root, "origin.git"), wt: filepath.Join(root, "work")}
	gitCmd(t, "", "init", "--quiet", "--bare", "--initial-branch=main", f.origin)

	seed := filepath.Join(root, "seed")
	gitCmd(t, "", "clone", "--quiet", f.origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, seed, "add", "-A")
	gitCmd(t, seed, "commit", "-m", "seed")
	gitCmd(t, seed, "push", "--quiet", "origin", "main")
	// The workbench, one commit ahead — the state a reset must discard.
	gitCmd(t, seed, "checkout", "--quiet", "-b", workbench.LegacyShared)
	if err := os.WriteFile(filepath.Join(seed, "undelivered.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, seed, "add", "-A")
	gitCmd(t, seed, "commit", "-m", "undelivered work")
	gitCmd(t, seed, "push", "--quiet", "origin", workbench.LegacyShared)

	gitCmd(t, "", "clone", "--quiet", "--branch", workbench.LegacyShared, f.origin, f.wt)

	old := openRunnerBench
	openRunnerBench = func(_ *Server, _ context.Context, _, _, repoID, full string) (*workbench.Bench, string, func(), error) {
		f.calls++
		f.repoID, f.full = repoID, full
		// The hermetic executor form: no user identity, so git runs directly instead of through
		// the per-user sudo wrapper (workbench.New documents this form).
		b, err := workbench.New(&workspace.Executor{}, f.wt)
		if err == nil {
			b, err = b.On(workbench.LegacyShared)
		}
		return b, f.wt, func() {}, err
	}
	t.Cleanup(func() { openRunnerBench = old })
	return f
}

// tip resolves a ref in one of the fixture's repositories.
func (f *benchFixture) tip(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// fixtureRepoSet substitutes the instance repo set with what GitHub would report: the id and the
// short name are one path segment, the full name carries the owner.
func fixtureRepoSet(t *testing.T, repos ...model.Repo) {
	t.Helper()
	old := runnerRepoSet
	runnerRepoSet = func(context.Context, string, string) ([]model.Repo, error) { return repos, nil }
	t.Cleanup(func() { runnerRepoSet = old })
}

// TestRepoResetOverLedgerRepoName is the reset the surface actually triggers: the button hands over
// the repository string the DELIVERY LEDGER carries — the GitHub full name "owner/repo" — because
// that is what a delivery record stores and what the deliveries view groups by. Before, that name
// reached a lookup keyed by id/short name only and every reset answered 404; and the same string
// would have become a workspace directory name. The reset must resolve and actually move the
// workbench back onto the default branch.
func TestRepoResetOverLedgerRepoName(t *testing.T) {
	s := deliveriesServer(t)
	fixtureRepoSet(t, model.Repo{ID: "a", Name: "a", FullName: "o/a", Permission: "push"})
	f := newBenchFixture(t)
	// The ledger states the full name (exec_deps writes it that way), and so does the surface.
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_1", Repo: "o/a", Branch: "fix/a-1", CreatedAt: tD})

	mainTip := f.tip(t, f.wt, "refs/remotes/origin/main")
	if f.tip(t, f.wt, "refs/heads/"+workbench.LegacyShared) == mainTip {
		t.Fatal("precondition: the workbench must be ahead of the default branch")
	}

	rec := httptest.NewRecorder()
	s.runRepoReset(rec, authedReq(http.MethodPost, "/api/mercury/runs/reset", map[string]string{"repo": "o/a"}, "alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset over the ledger name: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if f.calls != 1 {
		t.Fatalf("the workbench must be opened exactly once, got %d", f.calls)
	}
	// The workspace is addressed by the repo ID (one path segment); the clone by the full name.
	if f.repoID != "a" || f.full != "o/a" {
		t.Errorf("workbench opened for repoID=%q full=%q, want \"a\" / \"o/a\"", f.repoID, f.full)
	}
	if got := f.tip(t, f.wt, "refs/heads/"+workbench.LegacyShared); got != mainTip {
		t.Errorf("workbench tip = %s, want the default tip %s — the reset did not happen", got, mainTip)
	}
	if got := f.tip(t, f.origin, "refs/heads/"+workbench.LegacyShared); got != mainTip {
		t.Errorf("origin workbench = %s, want %s — the reset was not published", got, mainTip)
	}
	var out struct {
		Reset bool   `json:"reset"`
		Repo  string `json:"repo"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Reset || out.Repo != "o/a" {
		t.Errorf("the answer must name the repository that was reset, got %+v", out)
	}
}

// TestRepoResetShortNameAndUnknown: the short name resolves just as well (one lookup, three
// accepted forms), and a repository outside the instance set is refused instead of guessed.
func TestRepoResetShortNameAndUnknown(t *testing.T) {
	s := deliveriesServer(t)
	fixtureRepoSet(t, model.Repo{ID: "a", Name: "a", FullName: "o/a", Permission: "push"})
	f := newBenchFixture(t)

	rec := httptest.NewRecorder()
	s.runRepoReset(rec, authedReq(http.MethodPost, "/api/mercury/runs/reset", map[string]string{"repo": "a"}, "alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset over the short name: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.runRepoReset(rec, authedReq(http.MethodPost, "/api/mercury/runs/reset", map[string]string{"repo": "o/elsewhere"}, "alice"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown repository: status = %d, want 404", rec.Code)
	}
	// A FOREIGN owner's repository of the same short name is not this instance's: resolving happens
	// before the name is reduced, so it is refused rather than quietly redirected.
	rec = httptest.NewRecorder()
	s.runRepoReset(rec, authedReq(http.MethodPost, "/api/mercury/runs/reset", map[string]string{"repo": "other-owner/a"}, "alice"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("foreign owner: status = %d, want 404", rec.Code)
	}
	if f.calls != 1 {
		t.Errorf("an unresolved repository must never open a workbench, opened %d times", f.calls)
	}
}

// TestDeliveriesListCarriesExecutionLink: the ledger's wire view names the execution a delivery
// arose from. That link is what lets a surface state which executions still hold an OPEN delivery
// by reading the two pools — the same B-8 rule runs.ExecutionCompleted applies — instead of
// re-deriving a chain stage from its name (B-35).
func TestDeliveriesListCarriesExecutionLink(t *testing.T) {
	s := deliveriesServer(t)
	m := tD.Add(time.Hour)
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_open", Repo: "o/a", Branch: "fix/a-1", CreatedAt: tD, ExecutionID: "exec_open"})
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_done", Repo: "o/a", Branch: "fix/a-2", CreatedAt: tD, MergedAt: &m, ExecutionID: "exec_done"})

	rec := httptest.NewRecorder()
	s.runDeliveriesList(rec, authedReq(http.MethodGet, "/api/mercury/runs/deliveries", nil, "alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deliveries []struct {
			ID          string `json:"id"`
			Stage       string `json:"stage"`
			ExecutionID string `json:"executionId"`
		} `json:"deliveries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, d := range out.Deliveries {
		got[d.ID] = [2]string{d.Stage, d.ExecutionID}
	}
	if got["dlv_open"] != [2]string{"open", "exec_open"} {
		t.Errorf("open delivery = %v, want stage open of exec_open", got["dlv_open"])
	}
	if got["dlv_done"] != [2]string{"merged", "exec_done"} {
		t.Errorf("settled delivery = %v, want stage merged of exec_done", got["dlv_done"])
	}
}

// The open list needs to say WHY a todo waits: whether its delivery is merely waiting out the
// auto-merge window (and until when) or blocked for a release (K-5). Those facts live on the tracked
// pull request, so the ledger wire joins them by delivery id — the ONE place a surface reads them.
func TestDeliveriesListCarriesMergeDeadlineAndBlockade(t *testing.T) {
	s := deliveriesServer(t)
	mergeBy := tD.Add(7 * 24 * time.Hour)
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_wait", Repo: "o/a", Branch: "fix/a-1", PRNumber: 11, CreatedAt: tD, ExecutionID: "exec_wait"})
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_block", Repo: "o/a", Branch: "fix/a-2", PRNumber: 12, CreatedAt: tD, ExecutionID: "exec_block"})
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_retry", Repo: "o/a", Branch: "fix/a-3", PRNumber: 13, CreatedAt: tD, ExecutionID: "exec_retry"})
	// One tracked PR waits out its window; one is blocked (a durable obstacle waits for a person); one
	// is being retried after a SELF-ENDING obstacle — visible, but never waiting for anyone.
	firstAt := tD.Add(-time.Hour)
	nextAt := tD.Add(15 * time.Minute)
	_ = s.runPRs.Add(runs.PendingPR{Repo: "o/a", Number: 11, DeliveryID: "dlv_wait", CreatedAt: tD, MergeBy: mergeBy})
	_ = s.runPRs.Add(runs.PendingPR{Repo: "o/a", Number: 12, DeliveryID: "dlv_block", CreatedAt: tD, MergeBy: mergeBy, Blocked: true, BlockedReason: "the pull request was deleted"})
	_ = s.runPRs.Add(runs.PendingPR{Repo: "o/a", Number: 13, DeliveryID: "dlv_retry", CreatedAt: tD, MergeBy: mergeBy, Backoff: &model.Backoff{
		Reason: "reading the pull request failed: connection reset", Class: "transient", Attempts: 7, FirstAt: firstAt, NextAt: nextAt,
	}})

	rec := httptest.NewRecorder()
	s.runDeliveriesList(rec, authedReq(http.MethodGet, "/api/mercury/runs/deliveries", nil, "alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deliveries []struct {
			ID            string     `json:"id"`
			MergeBy       *time.Time `json:"mergeBy"`
			Blocked       bool       `json:"blocked"`
			BlockedReason string     `json:"blockedReason"`
			Retrying      bool       `json:"retrying"`
			RetryReason   string     `json:"retryReason"`
			RetryAttempts int        `json:"retryAttempts"`
			RetrySince    *time.Time `json:"retrySince"`
			RetryNextAt   *time.Time `json:"retryNextAt"`
		} `json:"deliveries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		MergeBy       *time.Time
		Blocked       bool
		BlockedReason string
		Retrying      bool
		RetryReason   string
		RetryAttempts int
		RetrySince    *time.Time
		RetryNextAt   *time.Time
	}{}
	for _, d := range out.Deliveries {
		byID[d.ID] = struct {
			MergeBy       *time.Time
			Blocked       bool
			BlockedReason string
			Retrying      bool
			RetryReason   string
			RetryAttempts int
			RetrySince    *time.Time
			RetryNextAt   *time.Time
		}{d.MergeBy, d.Blocked, d.BlockedReason, d.Retrying, d.RetryReason, d.RetryAttempts, d.RetrySince, d.RetryNextAt}
	}
	if w := byID["dlv_wait"]; w.MergeBy == nil || !w.MergeBy.Equal(mergeBy) || w.Blocked || w.Retrying {
		t.Errorf("waiting delivery = %+v, want the merge deadline and no block/retry", w)
	}
	if b := byID["dlv_block"]; !b.Blocked || b.BlockedReason != "the pull request was deleted" || b.Retrying {
		t.Errorf("blocked delivery = %+v, want blocked with its reason and NOT retrying", b)
	}
	// The retry carries the four visible facts and is NOT blocked — a self-ending obstacle waits for
	// no one.
	r := byID["dlv_retry"]
	if r.Blocked || !r.Retrying || r.RetryAttempts != 7 || r.RetryReason == "" {
		t.Errorf("retrying delivery = %+v, want retrying with reason and attempts, not blocked", r)
	}
	if r.RetrySince == nil || !r.RetrySince.Equal(firstAt) || r.RetryNextAt == nil || !r.RetryNextAt.Equal(nextAt) {
		t.Errorf("retrying delivery must carry since=%v next=%v, got %+v", firstAt, nextAt, r)
	}
}

// BLOCKER: the standstill report must reach the configuration it was written for — the cutover's
// FIRST start (00-cutover.md step 6), which runs deliberately WITHOUT a runner identity so no pass
// can reach GitHub. The tick resolved the token first and returned on its absence, so in exactly
// that configuration the operator saw nothing: not the standstill, and not the pool waiting behind
// it. Unarmed, the pass needs no identity at all — it reads DevLab's own pools and reports.
func TestMaintainReportsTheStandstillWithoutARunnerIdentity(t *testing.T) {
	s := deliveriesServer(t)
	// The held start, exactly: neither identity is in the drop-in, and the maintenance is unarmed.
	t.Setenv("DEVLAB_RUNS_USER", "")
	t.Setenv("DEVLAB_RUNS_TOKEN_USER", "")
	t.Setenv(deliver.EnvMaintainEnforce, "")
	if _, err := s.runnerToken(); err == nil {
		t.Fatal("the premise of this test is that no runner identity resolves")
	}
	// Something IS waiting: without it the report stays silent by design.
	_ = s.deliveries.Put(runs.Delivery{ID: "dlv_1", Repo: "o/x", Branch: "fix/one-a1", FromCommit: "c0", ToCommit: "c1", PRNumber: 5, CreatedAt: tD, ExecutionID: "exec_1"})
	_ = s.runPRs.Add(runs.PendingPR{Repo: "o/x", Number: 5, DeliveryID: "dlv_1", CreatedAt: tD, MergeBy: tD})

	// No ops fixture is injected: a held pass must make no foreign call, so it needs none.
	if err := s.MaintainDeliveries(context.Background()); err != nil {
		t.Fatalf("the held pass must not fail on a missing identity it does not need: %v", err)
	}
	list, err := s.runNotices.List()
	if err != nil {
		t.Fatal(err)
	}
	held := 0
	for _, n := range list {
		if n.Kind == runs.NoticeDeliveryHeld {
			held++
		}
	}
	if held != 1 {
		t.Fatalf("the standstill must be reported exactly once, got %d of %d notices: %+v", held, len(list), list)
	}
	// And nothing moved: a standstill that quietly merged something would be no standstill.
	if left, _ := s.runPRs.List(); len(left) != 1 {
		t.Errorf("the held pass must leave the tracked pull request exactly as it is, got %+v", left)
	}
	if got, _, _ := s.deliveries.ByID("dlv_1"); got.MergedAt != nil {
		t.Errorf("the held pass must change no delivery record: %+v", got)
	}
}

// The other half of the same decision: ARMED, the pass writes into foreign repositories, so a
// missing identity is fatal and named — never a standstill report about work it never attempted.
func TestMaintainRefusesToWriteWithoutARunnerIdentity(t *testing.T) {
	s := deliveriesServer(t)
	t.Setenv("DEVLAB_RUNS_USER", "")
	t.Setenv("DEVLAB_RUNS_TOKEN_USER", "")
	t.Setenv(deliver.EnvMaintainEnforce, "1")
	_ = s.runPRs.Add(runs.PendingPR{Repo: "o/x", Number: 5, DeliveryID: "dlv_1", CreatedAt: tD, MergeBy: tD})

	err := s.MaintainDeliveries(context.Background())
	if err == nil {
		t.Fatal("armed without a runner identity must fail by name")
	}
	if !strings.Contains(err.Error(), "runner account") {
		t.Errorf("the failure must name the missing identity, got %v", err)
	}
	if list, _ := s.runNotices.List(); len(list) != 0 {
		t.Errorf("an armed pass that could not run reports no standstill, got %+v", list)
	}
}
