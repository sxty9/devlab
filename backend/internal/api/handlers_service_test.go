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
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
	"devlab/backend/internal/telemetry"
)

// serviceFixture wires the configuration interface over the REAL settings pool and a REAL
// scheduler, so "the change takes effect immediately" is observed the way a caller observes it:
// through the slot overview, with nothing restarted in between.
type serviceFixture struct {
	t      *testing.T
	srv    *Server
	paths  *statepath.Paths
	broker *live.Broker
}

func newServiceFixture(t *testing.T, seed runs.Settings) *serviceFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_SETTINGS", filepath.Join(dir, "settings.json"))
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "state", "mercury", "executions"))
	paths := &statepath.Paths{Root: filepath.Join(dir, "state")}

	docs, err := execstate.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	set := runs.NewSettingsStore(nil, seed)
	rs := runs.NewStore(nil)
	scheduler := sched.New(sched.Config{}, docs, rs, runs.NewResultStore(paths), set, nil,
		func(ctx context.Context, _ execstate.Doc, _ runs.Run) error { <-ctx.Done(); return nil }, nil, nil)

	broker := live.NewBroker()
	srv := &Server{paths: paths, usage: telemetry.OpenUsage(paths)}
	srv.SetSettings(set)
	srv.SetExecution(docs, scheduler)
	srv.SetBroker(broker)
	return &serviceFixture{t: t, srv: srv, paths: paths, broker: broker}
}

func (f *serviceFixture) req(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	u := &auth.User{Username: "ada", IsAdmin: true}
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
}

func (f *serviceFixture) config() model.ServiceConfig {
	f.t.Helper()
	w := httptest.NewRecorder()
	f.srv.serviceConfigGet(w, f.req("GET", "/api/service/config", ""))
	if w.Code != http.StatusOK {
		f.t.Fatalf("GET config = %d: %s", w.Code, w.Body.String())
	}
	var got model.ServiceConfig
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		f.t.Fatal(err)
	}
	return got
}

func (f *serviceFixture) putConfig(body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.srv.serviceConfigPut(w, f.req("PUT", "/api/service/config", body))
	return w
}

func (f *serviceFixture) capacity() int {
	f.t.Helper()
	w := httptest.NewRecorder()
	f.srv.runSlots(w, f.req("GET", "/api/mercury/runs/slots", ""))
	if w.Code != http.StatusOK {
		f.t.Fatalf("GET slots = %d: %s", w.Code, w.Body.String())
	}
	var ov model.SlotOverview
	if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil {
		f.t.Fatal(err)
	}
	return ov.Capacity
}

// The configuration interface serves the settings pool, and a PUT takes effect AT ONCE: the slot
// overview reports the new capacity on the very next read, because the scheduler reads the same
// pool instead of a cached copy (REQ-013.2 / cross-cutting 5).
func TestServiceConfigPutTakesEffectImmediately(t *testing.T) {
	f := newServiceFixture(t, runs.Settings{MaxConcurrency: 2, DefaultTimeBudget: 3 * time.Hour, AutomergeWindow: 24 * time.Hour})

	got := f.config()
	if got.MaxConcurrency != 2 || time.Duration(got.DefaultTimeBudget) != 3*time.Hour {
		t.Fatalf("seeded config = %+v", got)
	}
	if f.capacity() != 2 {
		t.Fatalf("slot capacity before = %d, want the seed 2", f.capacity())
	}

	sub, cancel := f.broker.Subscribe(context.Background())
	defer cancel()

	w := f.putConfig(`{"maxConcurrency":10,"defaultTimeBudget":"90m","automergeWindow":"720h"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT config = %d: %s", w.Code, w.Body.String())
	}

	if got := f.capacity(); got != 10 {
		t.Errorf("slot capacity right after the PUT = %d, want 10", got)
	}
	after := f.config()
	if after.MaxConcurrency != 10 || time.Duration(after.DefaultTimeBudget) != 90*time.Minute ||
		time.Duration(after.AutomergeWindow) != 720*time.Hour {
		t.Errorf("config after PUT = %+v", after)
	}

	// The change reaches the open surfaces over the one stream — after the successful write.
	seen := map[live.Topic]bool{}
	for len(sub) > 0 {
		seen[<-sub] = true
	}
	if !seen[live.TopicSlots] {
		t.Errorf("a capacity change must tick the slots topic, got %v", seen)
	}

	// It survives a restart: a fresh pool handle over the same file reads the stored values, and
	// the env seed no longer wins (REQ-013.2).
	reopened := runs.NewSettingsStore(nil, runs.Settings{MaxConcurrency: 2})
	stored, err := reopened.Get()
	if err != nil {
		t.Fatal(err)
	}
	if stored.MaxConcurrency != 10 {
		t.Errorf("stored capacity = %d, want the runtime choice 10", stored.MaxConcurrency)
	}
}

// A rejected configuration changes nothing: the named reason comes back and the pool keeps the
// values it had.
func TestServiceConfigPutRefusesNonsense(t *testing.T) {
	f := newServiceFixture(t, runs.Settings{MaxConcurrency: 3, DefaultTimeBudget: time.Hour})

	cases := []struct{ name, body string }{
		{"no slots", `{"maxConcurrency":0,"defaultTimeBudget":"1h","automergeWindow":"1h"}`},
		{"absurd slots", `{"maxConcurrency":9999,"defaultTimeBudget":"1h","automergeWindow":"1h"}`},
		{"negative budget", `{"maxConcurrency":3,"defaultTimeBudget":"-1h","automergeWindow":"1h"}`},
		{"negative automerge", `{"maxConcurrency":3,"defaultTimeBudget":"1h","automergeWindow":"-5m"}`},
		{"not a duration", `{"maxConcurrency":3,"defaultTimeBudget":"soon","automergeWindow":"1h"}`},
	}
	for _, c := range cases {
		w := f.putConfig(c.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", c.name, w.Code, w.Body.String())
		}
		if w.Body.Len() == 0 || !strings.Contains(w.Body.String(), "detail") {
			t.Errorf("%s: the refusal must name its reason, got %q", c.name, w.Body.String())
		}
	}
	if got := f.config(); got.MaxConcurrency != 3 || time.Duration(got.DefaultTimeBudget) != time.Hour {
		t.Errorf("a refused PUT must change nothing, got %+v", got)
	}
	// Zero IS allowed for the budget: "no budget" is a legitimate choice (REQ-010.3).
	if w := f.putConfig(`{"maxConcurrency":3,"defaultTimeBudget":"0s","automergeWindow":"1h"}`); w.Code != http.StatusOK {
		t.Errorf(`"no budget" must be accepted, got %d: %s`, w.Code, w.Body.String())
	}
}

// Without a settings pool the configuration route says so instead of pretending a default.
func TestServiceConfigWithoutPoolIsHonest(t *testing.T) {
	srv := &Server{}
	w := httptest.NewRecorder()
	srv.serviceConfigGet(w, httptest.NewRequest("GET", "/api/service/config", nil))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", w.Code)
	}
}

// The three telemetry interfaces report — and never judge: the process's own load, the portioned
// pool occupancy, and the ONE AI-usage pool aggregated over a window.
func TestServiceTelemetryStorageAndUsage(t *testing.T) {
	f := newServiceFixture(t, runs.Settings{MaxConcurrency: 2})

	w := httptest.NewRecorder()
	f.srv.serviceTelemetry(w, f.req("GET", "/api/service/telemetry", ""))
	var load model.LoadView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &load) != nil {
		t.Fatalf("telemetry = %d %s", w.Code, w.Body.String())
	}
	if load.Goroutines <= 0 {
		t.Errorf("load must report the goroutine count, got %+v", load)
	}

	// Two samples through the ONE recording path — the same one the assistant, the agent and the
	// chain use.
	f.srv.RecordAiUsage(telemetry.UsageSample{Source: "assistant", User: "ada", In: 100, Out: 10, At: time.Now().UTC()})
	f.srv.RecordAiUsage(telemetry.UsageSample{Source: "run", Repo: "o/x", In: 900, Out: 90, At: time.Now().UTC()})

	w = httptest.NewRecorder()
	f.srv.serviceAiUsage(w, f.req("GET", "/api/service/ai-usage", ""))
	var usage model.AiUsageView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &usage) != nil {
		t.Fatalf("ai-usage = %d %s", w.Code, w.Body.String())
	}
	if usage.Samples != 2 || usage.Totals.InputTokens != 1000 {
		t.Errorf("usage = %+v, want 2 samples / 1000 in", usage)
	}
	if usage.BySource["assistant"].InputTokens != 100 || usage.BySource["run"].InputTokens != 900 {
		t.Errorf("usage per source = %+v", usage.BySource)
	}
	if usage.WindowHours != 24 {
		t.Errorf("default window = %dh, want 24h", usage.WindowHours)
	}

	// The window is a parameter, and nonsense in it is refused rather than guessed.
	w = httptest.NewRecorder()
	f.srv.serviceAiUsage(w, f.req("GET", "/api/service/ai-usage?hours=0", ""))
	if err := json.Unmarshal(w.Body.Bytes(), &usage); err != nil || usage.Samples != 2 {
		t.Errorf("hours=0 reports the whole pool, got %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	f.srv.serviceAiUsage(w, f.req("GET", "/api/service/ai-usage?hours=lots", ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("hours=lots must be refused, got %d", w.Code)
	}

	// Storage: one row per POOL — the AI-usage pool the samples above landed in is among them.
	w = httptest.NewRecorder()
	f.srv.serviceStorage(w, f.req("GET", "/api/service/storage", ""))
	var storage model.StorageView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &storage) != nil {
		t.Fatalf("storage = %d %s", w.Code, w.Body.String())
	}
	if len(storage.Pools) == 0 || storage.TotalBytes <= 0 {
		t.Errorf("storage must report the pools that exist, got %+v", storage)
	}
	found := false
	for _, p := range storage.Pools {
		if p.Name == "" {
			t.Errorf("every reported pool must be named, got %+v", storage.Pools)
		}
		if p.Name == "ai-usage" {
			found = true
			if p.Bytes <= 0 || p.Files != 1 {
				t.Errorf("the ai-usage pool must report what it holds, got %+v", p)
			}
		}
	}
	if !found {
		t.Errorf("the AI-usage pool must appear in the storage report, got %+v", storage.Pools)
	}
}
