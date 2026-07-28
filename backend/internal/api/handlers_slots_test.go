package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/execstate"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
)

// slotsFixture wires a real scheduler over temp stores behind a bare Server — the handlers
// stay thin adapters; everything they answer derives from the persisted documents.
type slotsFixture struct {
	t     *testing.T
	srv   *Server
	sch   *sched.Scheduler
	docs  *execstate.Store
	runs  *runs.Store
	execs chan string
}

func newSlotsFixture(t *testing.T) *slotsFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "state", "mercury", "executions"))
	paths := &statepath.Paths{Root: filepath.Join(dir, "state")}
	docs, err := execstate.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	rs := runs.NewStore(nil)
	f := &slotsFixture{t: t, docs: docs, runs: rs, execs: make(chan string, 16)}
	exec := func(ctx context.Context, doc execstate.Doc, _ runs.Run) error {
		f.execs <- doc.ID
		<-ctx.Done()
		return nil
	}
	f.sch = sched.New(sched.Config{}, docs, rs, runs.NewResultStore(paths), nil, nil, exec, nil, nil)
	f.srv = &Server{paths: paths}
	f.srv.SetExecution(docs, f.sch)
	return f
}

func (f *slotsFixture) req(method, path, body string, pathID string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if pathID != "" {
		r.SetPathValue("id", pathID)
	}
	u := &auth.User{Username: "ada", IsAdmin: true}
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
}

func (f *slotsFixture) addTodo(id, repo string) {
	f.t.Helper()
	if err := f.runs.Put(runs.Run{ID: id, Kind: model.KindTodo, Title: id, Targets: []runs.Target{{Repo: repo}}}); err != nil {
		f.t.Fatal(err)
	}
}

// POST …/run answers the honest StartOutcome; the actor lands on the transition protocol
// (REQ-041); GET …/active answers a LIST plus the restart state.
func TestRunNowStartsAndActiveIsAList(t *testing.T) {
	f := newSlotsFixture(t)
	f.addTodo("run_a", "alpha")

	w := httptest.NewRecorder()
	f.srv.runNow(w, f.req("POST", "/api/mercury/runs/run_a/run", "", "run_a"))
	if w.Code != http.StatusOK {
		t.Fatalf("runNow: %d %s", w.Code, w.Body)
	}
	var out model.StartOutcome
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Started || out.ExecutionID == "" {
		t.Fatalf("outcome: %+v", out)
	}
	<-f.execs

	d, _, _ := f.docs.Get(out.ExecutionID)
	if d.Requested.Created.User != "ada" {
		t.Fatalf("the requester must be on the document: %+v", d.Requested)
	}

	w = httptest.NewRecorder()
	f.srv.runActive(w, f.req("GET", "/api/mercury/runs/active", "", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("active: %d", w.Code)
	}
	var act struct {
		Active  []model.ExecutionView `json:"active"`
		Restart model.RestartState    `json:"restart"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &act); err != nil {
		t.Fatal(err)
	}
	if len(act.Active) != 1 || act.Active[0].Phase != model.PhaseRunning {
		t.Fatalf("the active answer is a LIST: %+v", act.Active)
	}

	// Slot overview mirrors the same truth.
	w = httptest.NewRecorder()
	f.srv.runSlots(w, f.req("GET", "/api/mercury/runs/slots", "", ""))
	var ov model.SlotOverview
	if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if ov.Occupied != 1 || ov.Capacity < 1 {
		t.Fatalf("overview: %+v", ov)
	}
}

// The unknown run 404s; cancel/defer/resume map wrong states to 409 — named, never silent.
func TestExecActionStatusMapping(t *testing.T) {
	f := newSlotsFixture(t)
	f.addTodo("run_a", "alpha")

	w := httptest.NewRecorder()
	f.srv.runNow(w, f.req("POST", "/api/mercury/runs/run_missing/run", "", "run_missing"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown run must 404: %d", w.Code)
	}

	// Nothing live yet → cancel/defer/resume answer 409 with a reason.
	for _, h := range []http.HandlerFunc{f.srv.runCancel, f.srv.runDefer, f.srv.runResume} {
		w = httptest.NewRecorder()
		h(w, f.req("POST", "/api/mercury/runs/run_a/x", "", "run_a"))
		if w.Code != http.StatusConflict {
			t.Fatalf("no live execution must 409: %d %s", w.Code, w.Body)
		}
	}

	// Start, then cancel → 204; the document is honestly failed with its actor.
	w = httptest.NewRecorder()
	f.srv.runNow(w, f.req("POST", "/api/mercury/runs/run_a/run", "", "run_a"))
	var out model.StartOutcome
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	<-f.execs
	w = httptest.NewRecorder()
	f.srv.runCancel(w, f.req("POST", "/api/mercury/runs/run_a/cancel", "", "run_a"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		d, _, _ := f.docs.Get(out.ExecutionID)
		if d.Phase == model.PhaseFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("canceled execution never terminalized: %s", d.Phase)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The placement body reaches the scheduler: a queue placement queues instead of refusing.
func TestRunNowParsesPlacement(t *testing.T) {
	f := newSlotsFixture(t)
	f.addTodo("run_a", "shared")
	f.addTodo("run_b", "shared")

	w := httptest.NewRecorder()
	f.srv.runNow(w, f.req("POST", "/api/mercury/runs/run_a/run", "", "run_a"))
	<-f.execs

	// Same repo, no placement → honest refusal with a named reason.
	w = httptest.NewRecorder()
	f.srv.runNow(w, f.req("POST", "/api/mercury/runs/run_b/run", "", "run_b"))
	var out model.StartOutcome
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Started || out.Queued || out.NotStarted == "" {
		t.Fatalf("busy repo without placement: %+v", out)
	}

	// With placement queue → queued document.
	w = httptest.NewRecorder()
	f.srv.runNow(w, f.req("POST", "/api/mercury/runs/run_b/run", `{"placement":{"kind":"queue"}}`, "run_b"))
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Queued || out.ExecutionID == "" {
		t.Fatalf("queue placement must queue: %+v", out)
	}
}

// Without the execution machinery the routes answer 501 honestly — never a fake state.
func TestSlotsRoutesHonestWithoutScheduler(t *testing.T) {
	s := &Server{}
	for name, h := range map[string]http.HandlerFunc{
		"active": s.runActive, "slots": s.runSlots, "run": s.runNow,
		"cancel": s.runCancel, "defer": s.runDefer, "resume": s.runResume,
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/mercury/runs/x/"+name, nil)
		r.SetPathValue("id", "x")
		h(w, r)
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("%s without scheduler: %d", name, w.Code)
		}
	}
}
