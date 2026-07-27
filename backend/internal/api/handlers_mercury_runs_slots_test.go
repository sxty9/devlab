package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// blockingSlotExec holds each run mid-flight so a test can build up a live slot picture and exercise the
// slot read-model + start decision against it. A deferred run re-arms as a deferred suspension (mirroring
// the real executor), so the deferred-state overview can be checked too.
type blockingSlotExec struct {
	started chan string
	gate    chan struct{}
}

func newBlockingSlotExec() *blockingSlotExec {
	return &blockingSlotExec{started: make(chan string, 16), gate: make(chan struct{})}
}
func (b *blockingSlotExec) Maintain(context.Context)          {}
func (b *blockingSlotExec) PlanResume(runs.Run, bool) runs.ResumePlan {
	return runs.ResumePlan{Action: runs.ResumeFresh}
}
func (b *blockingSlotExec) Execute(ctx context.Context, run runs.Run, report func(string)) (runs.ResultRef, error) {
	report("res_" + run.ID)
	b.started <- run.ID
	select {
	case <-b.gate:
		return runs.ResultRef{ResultID: "res_" + run.ID, OK: true}, nil
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), runs.ErrRunDeferred) {
			now := time.Now()
			return runs.ResultRef{ResultID: "res_" + run.ID, Suspended: true, ResumeAt: &now, Reason: runs.ReasonDeferred}, nil
		}
		return runs.ResultRef{ResultID: "res_" + run.ID, OK: false}, ctx.Err()
	}
}

func slotWaitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition never held: %s", msg)
}

func seedSlotServer(t *testing.T, cap string, runsIn []runs.Run) (*Server, *blockingSlotExec) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "res"))
	t.Setenv("DEVLAB_MERCURY_BUSY", filepath.Join(dir, "run-active"))
	t.Setenv("DEVLAB_MERCURY_RESTART_PENDING", filepath.Join(dir, "restart-pending"))
	t.Setenv("DEVLAB_RUNS_MAX_CONCURRENCY", cap)
	store, results := runs.NewStore(), runs.NewResults()
	if _, err := store.Mutate("seed", "t", func([]runs.Run) ([]runs.Run, error) { return runsIn, nil }); err != nil {
		t.Fatal(err)
	}
	be := newBlockingSlotExec()
	sched := runs.NewScheduler(store, be, time.Second)
	s := &Server{runs: store, runResults: results, scheduler: sched}
	return s, be
}

func todoRun(id string, repo string) runs.Run {
	past := time.Now().Add(-time.Minute)
	return runs.Run{ID: id, Type: runs.TypeTodo, Enabled: true, DueAt: &past, Targets: []runs.Target{{Repo: repo}}, Task: "do it"}
}

// TestSlotOverviewPortionsUsedFree pins the read-model (task point 8): capacity, used and free slots, and
// the enriched inflight list, reflect the live runs.
func TestSlotOverviewPortionsUsedFree(t *testing.T) {
	s, be := seedSlotServer(t, "2", []runs.Run{todoRun("a", "repo-a"), todoRun("b", "repo-b")})

	ov := s.slotOverview()
	if ov.Capacity != 2 || ov.Used != 0 || ov.Free != 2 || len(ov.Active) != 0 {
		t.Fatalf("idle overview wrong: %+v", ov)
	}

	if _, ok := s.scheduler.FireNow("a", "user", false); !ok {
		t.Fatal("a must start")
	}
	<-be.started
	slotWaitFor(t, time.Second, func() bool { return s.scheduler.ActiveCount() == 1 }, "a active")

	ov = s.slotOverview()
	if ov.Capacity != 2 || ov.Used != 1 || ov.Free != 1 || ov.Overload != 0 {
		t.Fatalf("one-run overview wrong: %+v", ov)
	}
	if len(ov.Active) != 1 || ov.Active[0].RunID != "a" {
		t.Fatalf("active list wrong: %+v", ov.Active)
	}
	if len(ov.Inflight) != 1 || ov.Inflight[0].State != "executing" {
		t.Fatalf("inflight list wrong: %+v", ov.Inflight)
	}

	close(be.gate)
	slotWaitFor(t, 2*time.Second, func() bool { return s.scheduler.ActiveCount() == 0 }, "a to finish")
}

// TestStartDecisionOptionsAndSuggestion pins the start decision (task points 5, 6): when the cap is full
// a free-repo run is offered queue/defer/overload with an automatic suggestion; a repo-busy run is NOT
// offered overload (it must not cross the per-repo limit — task point 7).
func TestStartDecisionOptionsAndSuggestion(t *testing.T) {
	s, be := seedSlotServer(t, "1", []runs.Run{
		todoRun("a", "repo-x"), todoRun("b", "repo-y"), todoRun("c", "repo-x"),
	})
	if _, ok := s.scheduler.FireNow("a", "user", false); !ok {
		t.Fatal("a must start")
	}
	<-be.started
	slotWaitFor(t, time.Second, func() bool { return s.scheduler.ActiveCount() == 1 }, "a active")

	// b: free repo, cap full → cap block → queue/defer/overload, suggests deferring a.
	bRun, _, _ := s.runs.Get("b")
	dec := s.buildStartDecision(bRun, s.scheduler.Admissibility(bRun))
	if dec.Blocked != runs.AdmitCap {
		t.Fatalf("b should be a cap block, got %q", dec.Blocked)
	}
	if !hasOption(dec.Options, "overload") || !hasOption(dec.Options, "queue") || !hasOption(dec.Options, "defer") {
		t.Fatalf("a cap block must offer queue/defer/overload, got %v", dec.Options)
	}
	if dec.Suggestion == nil || dec.Suggestion.RunID != "a" {
		t.Fatalf("suggestion should point at the only defer candidate a, got %+v", dec.Suggestion)
	}

	// c: shares repo-x with a → repo-busy block → NO overload (task point 7).
	cRun, _, _ := s.runs.Get("c")
	dec = s.buildStartDecision(cRun, s.scheduler.Admissibility(cRun))
	if dec.Blocked != runs.AdmitRepoBusy {
		t.Fatalf("c should be a repo-busy block, got %q", dec.Blocked)
	}
	if hasOption(dec.Options, "overload") {
		t.Fatalf("a repo-busy block must NOT offer overload, got %v", dec.Options)
	}
	if dec.Suggestion == nil || dec.Suggestion.RunID != "a" {
		t.Fatalf("suggestion should be a (holds the needed repo), got %+v", dec.Suggestion)
	}

	close(be.gate)
	slotWaitFor(t, 2*time.Second, func() bool { return s.scheduler.ActiveCount() == 0 }, "a to finish")
}

// TestRunNowStrategiesOverAndDefer pins the HTTP strategy routing: a plain start on a full floor returns
// the decision (200, started:false); overload starts past the cap; defer stands the named run down.
func TestRunNowStrategiesOverAndDefer(t *testing.T) {
	s, be := seedSlotServer(t, "1", []runs.Run{todoRun("a", "repo-a"), todoRun("b", "repo-b")})
	if _, ok := s.scheduler.FireNow("a", "user", false); !ok {
		t.Fatal("a must start")
	}
	<-be.started
	slotWaitFor(t, time.Second, func() bool { return s.scheduler.ActiveCount() == 1 }, "a active")

	// Plain start of b on a full floor → a decision, not a bare refusal.
	rec := runNowReq(t, s, "b", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("plain start status %d", rec.Code)
	}
	var body struct {
		Started  bool `json:"started"`
		Decision *struct {
			Blocked string   `json:"blocked"`
			Options []string `json:"options"`
		} `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Started || body.Decision == nil || body.Decision.Blocked != runs.AdmitCap {
		t.Fatalf("expected a cap decision, got %s", rec.Body.String())
	}

	// Overload starts b in a temporary extra slot past the cap.
	rec = runNowReq(t, s, "b", `{"strategy":"overload"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"overloaded":true`) {
		t.Fatalf("overload should start b past the cap, got %d %s", rec.Code, rec.Body.String())
	}
	if id := <-be.started; id != "b" {
		t.Fatalf("expected b to overload-start, got %s", id)
	}
	slotWaitFor(t, time.Second, func() bool { return s.scheduler.ActiveCount() == 2 }, "a + overloaded b")

	// Defer a via the standalone endpoint.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mercury/runs/a/defer", nil)
	req.SetPathValue("id", "a")
	s.runDefer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("defer a status %d: %s", rec.Code, rec.Body.String())
	}
	slotWaitFor(t, 2*time.Second, func() bool {
		r, _, _ := s.runs.Get("a")
		return r.Suspended != nil && r.Suspended.IsDeferred()
	}, "a to become a deferred suspension")

	// The deferred run surfaces in the overview with a deferred state (task point 8).
	ov := s.slotOverview()
	deferredSeen := false
	for _, e := range ov.Inflight {
		if e.RunID == "a" && e.State == "deferred" {
			deferredSeen = true
		}
	}
	if !deferredSeen {
		t.Fatalf("deferred run must appear in the overview as deferred, got %+v", ov.Inflight)
	}

	close(be.gate)
	slotWaitFor(t, 2*time.Second, func() bool { return s.scheduler.ActiveCount() == 0 }, "runs to finish")
}

// TestRunNowQueuesDuringRestartPending pins the NACHZUHOLEN: while a devlabd restart is pending the
// overview reports it and a start requested meanwhile is QUEUED (eingereiht), not actually started.
func TestRunNowQueuesDuringRestartPending(t *testing.T) {
	s, _ := seedSlotServer(t, "2", []runs.Run{todoRun("a", "repo-a")})
	pend := runs.RestartPendingPath()
	if err := os.WriteFile(pend, []byte(strconv.Itoa(os.Getpid())+" 2026-07-27T00:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(pend)

	if ov := s.slotOverview(); !ov.RestartPending || ov.Free != 0 {
		t.Fatalf("overview must report the pending restart and no free slot: %+v", ov)
	}

	rec := runNowReq(t, s, "a", `{}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"queued":true`) {
		t.Fatalf("a start during a pending restart must be queued, got %d %s", rec.Code, rec.Body.String())
	}
	if s.scheduler.ActiveCount() != 0 {
		t.Fatalf("nothing may actually start while a restart is pending, active=%d", s.scheduler.ActiveCount())
	}
}

func hasOption(opts []string, want string) bool {
	for _, o := range opts {
		if o == want {
			return true
		}
	}
	return false
}

func runNowReq(t *testing.T, s *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mercury/runs/"+id+"/run", strings.NewReader(body))
	req.SetPathValue("id", id)
	s.runNow(rec, req)
	return rec
}
