package api

// Delivery-surface tests (S10). These adopt the coverage of the pre-rebuild
// handlers_mercury_runs_rollback_test.go: the counter-booking DECISION logic itself now lives
// (and is tested) in package deliver — here the API surface is pinned: ledger stages, the
// rollback endpoint's outcomes (conflict → todo, open → closed PR), the reversal-PR tracking
// for the auto-merge window, and the guards.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	if r, _, _ := s.results.Get("exec_1"); r.MergedAt == nil {
		t.Errorf("B-8: the execution result must settle, got %+v", r)
	}
}
