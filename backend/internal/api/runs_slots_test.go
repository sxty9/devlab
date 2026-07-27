package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/runs"
)

// slotGate is a blocking Executor: it reserves the slot and holds it until released, so a test can fill the
// floor and observe the slot API.
type slotGate struct {
	mu      sync.Mutex
	started []string
	release chan struct{}
}

func (g *slotGate) Execute(_ context.Context, run runs.Run, report func(string)) (runs.ResultRef, error) {
	g.mu.Lock()
	g.started = append(g.started, run.ID)
	g.mu.Unlock()
	report("res_" + run.ID)
	<-g.release
	return runs.ResultRef{ResultID: "res_" + run.ID, OK: true}, nil
}
func (g *slotGate) Maintain(context.Context)                  {}
func (g *slotGate) PlanResume(runs.Run, bool) runs.ResumePlan { return runs.ResumePlan{Action: runs.ResumeFresh} }

func seedRunsStore(t *testing.T, in []runs.Run) *runs.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	st := runs.NewStore()
	if _, err := st.Mutate("seed", "t", func([]runs.Run) ([]runs.Run, error) { return in, nil }); err != nil {
		t.Fatal(err)
	}
	return st
}

func runNowReq(t *testing.T, id, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, "/api/mercury/runs/"+id+"/run", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "/api/mercury/runs/"+id+"/run", strings.NewReader(body))
	}
	r.SetPathValue("id", id)
	return r
}

// TestRunNowSlotStrategies exercises the blocked-start decision and the overload strategy against a full
// floor: a cap block offers overload + names a defer suggestion; overload starts past the cap.
func TestRunNowSlotStrategies(t *testing.T) {
	store := seedRunsStore(t, []runs.Run{
		{ID: "a", Type: runs.TypeTodo, Enabled: true, Targets: []runs.Target{{Repo: "r1"}}},
		{ID: "b", Type: runs.TypeTodo, Enabled: true, Targets: []runs.Target{{Repo: "r2"}}},
	})
	gate := &slotGate{release: make(chan struct{})}
	sched := runs.NewScheduler(store, gate, time.Second, 1) // cap 1
	s := &Server{runs: store, runResults: runs.NewResults(), scheduler: sched}

	if _, ok := sched.FireNow("a", "t", false); !ok {
		t.Fatal("a should take the single slot")
	}
	if sched.ActiveCount() != 1 {
		t.Fatalf("expected the slot filled, ActiveCount=%d", sched.ActiveCount())
	}

	// A blocked start returns a decision — blocked by the cap, offering overload, suggesting the run to defer.
	rec := httptest.NewRecorder()
	s.runNow(rec, runNowReq(t, "b", `{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("blocked start status = %d", rec.Code)
	}
	var out struct {
		Started  bool `json:"started"`
		Decision struct {
			Blocked    string   `json:"blocked"`
			Options    []string `json:"options"`
			Suggestion *struct {
				RunID string `json:"runId"`
			} `json:"suggestion"`
			Slots struct {
				Capacity, Used, Free int
			} `json:"slots"`
		} `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Started {
		t.Error("a cap-blocked start must not report started")
	}
	if out.Decision.Blocked != runs.AdmitCap {
		t.Errorf("blocked reason = %q, want cap", out.Decision.Blocked)
	}
	if !contains(out.Decision.Options, "overload") || !contains(out.Decision.Options, "queue") || !contains(out.Decision.Options, "defer") {
		t.Errorf("options should include queue/defer/overload, got %v", out.Decision.Options)
	}
	if out.Decision.Suggestion == nil || out.Decision.Suggestion.RunID != "a" {
		t.Errorf("the only active run 'a' should be the defer suggestion, got %+v", out.Decision.Suggestion)
	}
	if out.Decision.Slots.Capacity != 1 || out.Decision.Slots.Used != 1 || out.Decision.Slots.Free != 0 {
		t.Errorf("slots should read 1/1/0, got %+v", out.Decision.Slots)
	}

	// Overload starts b past the cap.
	rec = httptest.NewRecorder()
	s.runNow(rec, runNowReq(t, "b", `{"strategy":"overload"}`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "\"overloaded\":true") {
		t.Fatalf("overload should start b past the cap, got %d %s", rec.Code, rec.Body.String())
	}
	if sched.ActiveCount() != 2 {
		t.Fatalf("overload should raise the live count to 2, got %d", sched.ActiveCount())
	}

	close(gate.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sched.ActiveCount() > 0 {
		time.Sleep(2 * time.Millisecond)
	}
}

// TestRunDeferEndpoint: POST /runs/{id}/defer stands an active run down; a non-active run is a conflict.
func TestRunDeferEndpoint(t *testing.T) {
	store := seedRunsStore(t, []runs.Run{
		{ID: "a", Type: runs.TypeTodo, Enabled: true, Targets: []runs.Target{{Repo: "r1"}}},
	})
	gate := &slotGate{release: make(chan struct{})}
	sched := runs.NewScheduler(store, gate, time.Second, 2)
	s := &Server{runs: store, runResults: runs.NewResults(), scheduler: sched}

	sched.FireNow("a", "t", false)
	if sched.ActiveCount() != 1 {
		t.Fatalf("a should be live, ActiveCount=%d", sched.ActiveCount())
	}

	defReq := func(id string) int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/mercury/runs/"+id+"/defer", nil)
		r.SetPathValue("id", id)
		s.runDefer(rec, r)
		return rec.Code
	}
	if code := defReq("zzz"); code != http.StatusConflict {
		t.Errorf("deferring a non-active run = %d, want 409", code)
	}
	if code := defReq("a"); code != http.StatusOK {
		t.Errorf("deferring the active run a = %d, want 200", code)
	}

	close(gate.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sched.ActiveCount() > 0 {
		time.Sleep(2 * time.Millisecond)
	}
}

// TestRunConfigSetGetLive: setting the slot count persists it AND applies it to the running scheduler at
// once (no restart); clearing it reverts to the env/default seed.
func TestRunConfigSetGetLive(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	store := seedRunsStore(t, nil)
	sched := runs.NewScheduler(store, &slotGate{release: make(chan struct{})}, time.Second, 2)
	s := &Server{runs: store, runSettings: runs.NewSettingsStore(), scheduler: sched}

	put := func(body string) int {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/api/mercury/runs/config", strings.NewReader(body))
		s.runSetConfig(rec, r)
		return rec.Code
	}
	if code := put(`{"maxConcurrent":5}`); code != http.StatusOK {
		t.Fatalf("set config = %d, want 200", code)
	}
	if sched.Capacity() != 5 {
		t.Errorf("the cap must be applied live, Capacity=%d", sched.Capacity())
	}
	if rs, _ := s.runSettings.Get(); rs.MaxConcurrent != 5 {
		t.Errorf("the setting must persist, got %d", rs.MaxConcurrent)
	}

	rec := httptest.NewRecorder()
	s.runConfig(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/config", nil))
	if !strings.Contains(rec.Body.String(), "\"maxConcurrent\":5") || !strings.Contains(rec.Body.String(), "\"configured\":true") {
		t.Errorf("GET config should reflect 5 + configured, got %s", rec.Body.String())
	}

	// Clearing reverts to the env/default seed (env unset here → default 2).
	if code := put(`{"maxConcurrent":0}`); code != http.StatusOK {
		t.Fatalf("clear config = %d, want 200", code)
	}
	if sched.Capacity() != runs.DefaultMaxConcurrent {
		t.Errorf("clearing must revert to the seed (%d), got %d", runs.DefaultMaxConcurrent, sched.Capacity())
	}
	if code := put(`{"maxConcurrent":9999}`); code != http.StatusBadRequest {
		t.Errorf("an absurd slot count must be rejected, got %d", code)
	}
}

// TestRunConfigTimeBudgetDefault: the per-repo time-budget DEFAULT is set/read at the same central config
// surface as the slot cap. Setting it persists (canonicalized) and GET reports it as the default in force;
// an empty value clears it back to the env/built-in seed; a no-cap "off" is accepted; garbage is rejected;
// and editing one knob never clobbers the other.
func TestRunConfigTimeBudgetDefault(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	// env seed unset → the built-in 3h default is the seed.
	store := seedRunsStore(t, nil)
	sched := runs.NewScheduler(store, &slotGate{release: make(chan struct{})}, time.Second, 2)
	s := &Server{runs: store, runSettings: runs.NewSettingsStore(), scheduler: sched}

	put := func(body string) int {
		rec := httptest.NewRecorder()
		s.runSetConfig(rec, httptest.NewRequest(http.MethodPut, "/api/mercury/runs/config", strings.NewReader(body)))
		return rec.Code
	}
	get := func() string {
		rec := httptest.NewRecorder()
		s.runConfig(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/config", nil))
		return rec.Body.String()
	}

	// Nothing configured → the seed (3h) is the default in force, not marked as a UI override.
	if body := get(); !strings.Contains(body, "\"timeBudget\":\"3h\"") ||
		!strings.Contains(body, "\"timeBudgetSeed\":\"3h\"") || !strings.Contains(body, "\"timeBudgetConfigured\":false") {
		t.Errorf("fresh config should show the 3h seed default, got %s", body)
	}

	// Set a 90m default → persisted canonical ("90m"), reported as the humanized default in force ("1h30m").
	if code := put(`{"timeBudget":"90m"}`); code != http.StatusOK {
		t.Fatalf("set time budget = %d, want 200", code)
	}
	if rs, _ := s.runSettings.Get(); rs.AgentTimeout != "90m" {
		t.Errorf("the default must persist canonical, got %q", rs.AgentTimeout)
	}
	if body := get(); !strings.Contains(body, "\"timeBudget\":\"1h30m\"") || !strings.Contains(body, "\"timeBudgetConfigured\":true") {
		t.Errorf("GET should reflect the 1h30m default + configured, got %s", body)
	}

	// A no-cap default is accepted and read back as "off".
	if code := put(`{"timeBudget":"off"}`); code != http.StatusOK {
		t.Fatalf("set no-cap = %d, want 200", code)
	}
	if body := get(); !strings.Contains(body, "\"timeBudget\":\"off\"") {
		t.Errorf("GET should reflect the no-cap default, got %s", body)
	}

	// Clearing reverts to the seed.
	if code := put(`{"timeBudget":""}`); code != http.StatusOK {
		t.Fatalf("clear = %d, want 200", code)
	}
	if body := get(); !strings.Contains(body, "\"timeBudget\":\"3h\"") || !strings.Contains(body, "\"timeBudgetConfigured\":false") {
		t.Errorf("clearing should revert to the seed, got %s", body)
	}

	// Garbage is rejected, never silently defaulted.
	if code := put(`{"timeBudget":"soon"}`); code != http.StatusBadRequest {
		t.Errorf("an unparseable budget must be rejected, got %d", code)
	}

	// The two knobs are independent: setting the budget must not disturb a configured slot count.
	if code := put(`{"maxConcurrent":5}`); code != http.StatusOK {
		t.Fatalf("set slots = %d, want 200", code)
	}
	if code := put(`{"timeBudget":"2h"}`); code != http.StatusOK {
		t.Fatalf("set budget = %d, want 200", code)
	}
	if rs, _ := s.runSettings.Get(); rs.MaxConcurrent != 5 || rs.AgentTimeout != "2h" {
		t.Errorf("editing one knob must not clobber the other, got slots=%d budget=%q", rs.MaxConcurrent, rs.AgentTimeout)
	}
	if sched.Capacity() != 5 {
		t.Errorf("the slot cap must survive a budget-only edit, Capacity=%d", sched.Capacity())
	}
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
