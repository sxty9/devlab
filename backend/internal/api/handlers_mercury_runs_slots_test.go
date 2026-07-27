package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// slotTestExec is a runs.Executor for the slot API tests. Each Execute writes a result document reflecting
// a per-run plan (repos already done, an optional in-flight repo) so the overview and the defer suggestion
// have real progress to read, announces it started, then blocks until released — or, on a defer cancel,
// re-arms as a deferred suspension exactly as the real executor does.
type slotTestExec struct {
	results *runs.Results
	started chan string
	mu      sync.Mutex
	gates   map[string]chan struct{}
	closed  map[string]bool
	plan    map[string]slotPlan
}

type slotPlan struct {
	done     []string // repos to record as completed
	liveRepo string   // repo published as in-flight ("" = cleanly between repos)
}

func newSlotTestExec(results *runs.Results) *slotTestExec {
	return &slotTestExec{results: results, started: make(chan string, 16), gates: map[string]chan struct{}{}, closed: map[string]bool{}, plan: map[string]slotPlan{}}
}

func (e *slotTestExec) gate(id string) chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.gates[id]
	if !ok {
		c = make(chan struct{})
		e.gates[id] = c
	}
	return c
}

// release lets a held run finish. Idempotent: releasing a run already released (or drained) is a no-op.
func (e *slotTestExec) release(id string) {
	c := e.gate(id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed[id] {
		e.closed[id] = true
		close(c)
	}
}

// releaseAll frees every held run — the drain hook so no run goroutine outlives the test and writes into
// the temp dir during cleanup.
func (e *slotTestExec) releaseAll() {
	e.mu.Lock()
	ids := make([]string, 0, len(e.gates))
	for id := range e.gates {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	for _, id := range ids {
		e.release(id)
	}
}
func (e *slotTestExec) Maintain(context.Context) {}

func (e *slotTestExec) Execute(ctx context.Context, run runs.Run, report func(string)) (runs.ResultRef, error) {
	resID := "res_" + run.ID
	res := runs.Result{RunID: run.ID, ResultID: resID, StartedAt: time.Now()}
	p := e.plan[run.ID]
	for _, r := range p.done {
		res.Repos = append(res.Repos, runs.RepoResult{Repo: r, OK: true})
	}
	if p.liveRepo != "" {
		res.Live = &runs.RepoResult{Repo: p.liveRepo, Running: true}
	}
	_ = e.results.Save(res)
	report(resID)
	e.started <- run.ID
	select {
	case <-e.gate(run.ID):
		res.FinishedAt = time.Now()
		res.OK = true
		_ = e.results.Save(res)
		return runs.ResultRef{ResultID: resID, OK: true}, nil
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), runs.ErrRunDeferred) {
			now := time.Now()
			res.Suspended, res.ResumeAt = true, &now
			_ = e.results.Save(res)
			return runs.ResultRef{ResultID: resID, Suspended: true, ResumeAt: &now, Reason: runs.ReasonDeferred}, nil
		}
		return runs.ResultRef{ResultID: resID, OK: false}, ctx.Err()
	}
}

// ── helpers ────────────────────────────────────────────────────────────────────────────────────────

func newSlotServer(t *testing.T, cap string, runsIn []runs.Run) (*Server, *slotTestExec) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "res"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_RUNS_MAX_CONCURRENCY", cap)
	store, results := runs.NewStore(), runs.NewResults()
	if _, err := store.Mutate("seed", "t", func([]runs.Run) ([]runs.Run, error) { return runsIn, nil }); err != nil {
		t.Fatal(err)
	}
	exec := newSlotTestExec(results)
	sched := runs.NewScheduler(store, exec, time.Hour) // long tick: the test drives firing via the handlers
	// Drain every run goroutine before the temp dir is torn down, so none writes into it during cleanup.
	t.Cleanup(func() {
		exec.releaseAll()
		deadline := time.Now().Add(2 * time.Second)
		for sched.ActiveCount() > 0 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
	})
	return &Server{runs: store, runResults: results, scheduler: sched}, exec
}

func slotTodo(id string, repo string) runs.Run {
	return runs.Run{ID: id, Type: runs.TypeTodo, Enabled: true, Targets: []runs.Target{{Repo: repo}}, Task: "do it"}
}

func startAndWait(t *testing.T, s *Server, exec *slotTestExec, id string) {
	t.Helper()
	if !s.scheduler.FireNow(id, "t") {
		t.Fatalf("FireNow(%s) returned false", id)
	}
	select {
	case got := <-exec.started:
		if got != id {
			t.Fatalf("expected %s to start, got %s", id, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s never started", id)
	}
}

func activeCountWait(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.scheduler.ActiveCount() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("active count never reached %d (is %d)", want, s.scheduler.ActiveCount())
}

// runNowReq calls the runNow handler directly with an optional JSON body.
func runNowReq(t *testing.T, s *Server, id, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/api/mercury/runs/"+id+"/run", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/mercury/runs/"+id+"/run", strings.NewReader(body))
	}
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	s.runNow(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// ── tests ─────────────────────────────────────────────────────────────────────────────────────────

func TestSlotOverviewCountsUsedFreeOverloadAndDeferred(t *testing.T) {
	s, exec := newSlotServer(t, "2", []runs.Run{
		slotTodo("a", "repo-a"), slotTodo("b", "repo-b"), slotTodo("c", "repo-c"),
	})
	exec.plan["a"] = slotPlan{done: []string{"r1", "r2"}} // a is between repos with 2 done

	startAndWait(t, s, exec, "a")
	startAndWait(t, s, exec, "b")
	activeCountWait(t, s, 2)

	rec := httptest.NewRecorder()
	s.runActive(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/active", nil))
	var ov SlotOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if ov.Capacity != 2 || ov.Used != 2 || ov.Free != 0 || ov.Overload != 0 || len(ov.Active) != 2 {
		t.Fatalf("full-but-no-overload overview wrong: %+v", ov)
	}

	// Overload c past the cap.
	if !s.scheduler.StartOverload("c", "t") {
		t.Fatal("overload of c must start")
	}
	<-exec.started
	activeCountWait(t, s, 3)
	rec = httptest.NewRecorder()
	s.runActive(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/active", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &ov)
	if ov.Used != 2 || ov.Free != 0 || ov.Overload != 1 || len(ov.Active) != 3 {
		t.Fatalf("overload not reflected: %+v", ov)
	}

	// Defer a → it leaves the active set and shows as deferred with its resume point.
	if !s.scheduler.Defer("a") {
		t.Fatal("defer a")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, _, _ := s.runs.Get("a")
		if r.Suspended.IsDeferred() {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	rec = httptest.NewRecorder()
	s.runActive(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/active", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &ov)
	if len(ov.Deferred) != 1 || ov.Deferred[0].RunID != "a" {
		t.Fatalf("deferred list must contain a: %+v", ov.Deferred)
	}
	if ov.Deferred[0].Done != 2 || ov.Deferred[0].Total != 1 {
		// Total = len(TodoTargets) = 1; Done = repos recorded = 2 (the plan's completed repos).
		t.Fatalf("deferred resume point counts wrong: %+v", ov.Deferred[0])
	}
	if !strings.Contains(ov.Deferred[0].ResumePoint, "freien Platz") {
		t.Fatalf("resume point should read as a portioned sentence: %q", ov.Deferred[0].ResumePoint)
	}

	exec.release("b")
	exec.release("c")
}

func TestRunNowBlockedByCapReturnsDecisionWithOverloadAndSuggestion(t *testing.T) {
	s, exec := newSlotServer(t, "1", []runs.Run{slotTodo("a", "repo-a"), slotTodo("b", "repo-b")})
	exec.plan["a"] = slotPlan{done: []string{"r1"}} // a is cleanly between repos → the ideal defer target

	startAndWait(t, s, exec, "a")

	code, out := runNowReq(t, s, "b", "")
	if code != http.StatusOK {
		t.Fatalf("blocked start should be 200 with a decision, got %d", code)
	}
	if out["started"] != false {
		t.Fatalf("b must not have started: %+v", out)
	}
	dec, _ := out["decision"].(map[string]any)
	if dec == nil || dec["blocked"] != runs.AdmitCap {
		t.Fatalf("expected a cap decision, got %+v", out["decision"])
	}
	opts := toStrings(dec["options"])
	if !contains(opts, "queue") || !contains(opts, "defer") || !contains(opts, "overload") {
		t.Fatalf("a cap block must offer queue+defer+overload, got %v", opts)
	}
	sug, _ := dec["suggestion"].(map[string]any)
	if sug == nil || sug["runId"] != "a" {
		t.Fatalf("suggestion must point at a (the only active run), got %+v", dec["suggestion"])
	}
	if reason, _ := sug["reason"].(string); !strings.Contains(reason, "zwischen zwei") {
		t.Fatalf("a between-repos run's reason must say so, got %q", reason)
	}
	exec.release("a")
}

func TestRunNowBlockedByRepoBusyOffersNoOverload(t *testing.T) {
	s, exec := newSlotServer(t, "2", []runs.Run{slotTodo("a", "repo-x"), slotTodo("b", "repo-x")})
	startAndWait(t, s, exec, "a")

	code, out := runNowReq(t, s, "b", "")
	if code != http.StatusOK {
		t.Fatalf("blocked start should be 200, got %d", code)
	}
	dec, _ := out["decision"].(map[string]any)
	if dec == nil || dec["blocked"] != runs.AdmitRepoBusy {
		t.Fatalf("b shares repo-x with a → repo-busy, got %+v", out["decision"])
	}
	opts := toStrings(dec["options"])
	if contains(opts, "overload") {
		t.Fatalf("overload must NOT be offered when a repo is busy (task point 5): %v", opts)
	}
	sug, _ := dec["suggestion"].(map[string]any)
	if sug == nil || sug["runId"] != "a" {
		t.Fatalf("suggestion must be a (holds the needed repo), got %+v", dec["suggestion"])
	}
	exec.release("a")
}

func TestRunNowOverloadStrategyStartsPastCap(t *testing.T) {
	s, exec := newSlotServer(t, "1", []runs.Run{slotTodo("a", "repo-a"), slotTodo("b", "repo-b")})
	startAndWait(t, s, exec, "a")

	code, out := runNowReq(t, s, "b", `{"strategy":"overload"}`)
	if code != http.StatusOK || out["started"] != true || out["overloaded"] != true {
		t.Fatalf("overload strategy should start b past the cap: code=%d out=%+v", code, out)
	}
	<-exec.started
	activeCountWait(t, s, 2)
	over := false
	for _, a := range s.scheduler.Active() {
		if a.RunID == "b" && a.Overload {
			over = true
		}
	}
	if !over {
		t.Fatal("b must be flagged as an overload")
	}
	exec.release("a")
	exec.release("b")
}

func TestRunNowOverloadStrategyRefusedOnRepoConflict(t *testing.T) {
	s, exec := newSlotServer(t, "1", []runs.Run{slotTodo("a", "repo-x"), slotTodo("b", "repo-x")})
	startAndWait(t, s, exec, "a")

	code, _ := runNowReq(t, s, "b", `{"strategy":"overload"}`)
	if code != http.StatusConflict {
		t.Fatalf("overload onto a busy repo must be refused with 409, got %d", code)
	}
	exec.release("a")
}

func TestRunNowQueueStrategyMarksDue(t *testing.T) {
	s, exec := newSlotServer(t, "1", []runs.Run{slotTodo("a", "repo-a"), slotTodo("b", "repo-b")})
	startAndWait(t, s, exec, "a")

	code, out := runNowReq(t, s, "b", `{"strategy":"queue"}`)
	if code != http.StatusOK || out["queued"] != true {
		t.Fatalf("queue strategy should report queued: code=%d out=%+v", code, out)
	}
	b, _, _ := s.runs.Get("b")
	if b.DueAt == nil || b.DueAt.After(time.Now()) {
		t.Fatalf("queued ToDo must be due now, got DueAt=%v", b.DueAt)
	}
	exec.release("a")
}

func TestRunNowDeferStrategyDefersNamedRun(t *testing.T) {
	s, exec := newSlotServer(t, "1", []runs.Run{slotTodo("a", "repo-a"), slotTodo("b", "repo-b")})
	startAndWait(t, s, exec, "a")

	code, out := runNowReq(t, s, "b", `{"strategy":"defer","deferRunId":"a"}`)
	if code != http.StatusOK || out["deferred"] != "a" {
		t.Fatalf("defer strategy should report deferred=a: code=%d out=%+v", code, out)
	}
	// a stands down as a deferred suspension; b is handled (started now, or queued for the freeing slot).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, _, _ := s.runs.Get("a")
		if r.Suspended.IsDeferred() {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	a, _, _ := s.runs.Get("a")
	if !a.Suspended.IsDeferred() {
		t.Fatalf("a must be a deferred suspension, got %+v", a.Suspended)
	}
	b, _, _ := s.runs.Get("b")
	bActive := false
	for _, act := range s.scheduler.Active() {
		if act.RunID == "b" {
			bActive = true
		}
	}
	if !bActive && b.DueAt == nil {
		t.Fatal("b must be active or queued (due) after deferring a for it")
	}
}

func TestRunDeferEndpointStandsRunDown(t *testing.T) {
	s, exec := newSlotServer(t, "1", []runs.Run{slotTodo("a", "repo-a")})
	startAndWait(t, s, exec, "a")

	r := httptest.NewRequest(http.MethodPost, "/api/mercury/runs/a/defer", nil)
	r.SetPathValue("id", "a")
	rec := httptest.NewRecorder()
	s.runDefer(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("defer of an active run should be 200, got %d", rec.Code)
	}
	activeCountWait(t, s, 0)
	a, _, _ := s.runs.Get("a")
	if !a.Suspended.IsDeferred() {
		t.Fatalf("a must be re-armed as a deferred suspension, got %+v", a.Suspended)
	}

	// Deferring a run that is not active → 409.
	r2 := httptest.NewRequest(http.MethodPost, "/api/mercury/runs/a/defer", nil)
	r2.SetPathValue("id", "a")
	rec2 := httptest.NewRecorder()
	s.runDefer(rec2, r2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("deferring a non-active run should be 409, got %d", rec2.Code)
	}
}

// ── small assertion helpers ───────────────────────────────────────────────────────────────────────

func toStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
