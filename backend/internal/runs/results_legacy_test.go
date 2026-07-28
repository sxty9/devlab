package runs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// The pre-rebuild archive must stay VIEWABLE (REQ-027.3): every legacy result — including one
// with an empty step list, a legacy ok-bool step and a still-"running" live repo — maps onto a
// defined new-format document. Legacy states are display-only and are never produced anew.
func TestLegacyResultsReadTolerantly(t *testing.T) {
	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "runs-results", "run_legacy1")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "legacy-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "2026-07-20T02-00-00.000Z.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	// A torn file must be skipped, never fatal.
	if err := os.WriteFile(filepath.Join(legacyDir, "torn.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "runs-results"))
	rs := NewResultStore(&statepath.Paths{Root: dir})

	all, err := rs.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List = %d results, want exactly the one parsable legacy document", len(all))
	}
	res := all[0]
	if !res.Legacy {
		t.Error("a document from the archive must carry the legacy provenance flag")
	}
	if res.ID != "2026-07-20T02-00-00.000Z" || res.RunID != "run_legacy1" || res.Kind != model.KindAuto {
		t.Errorf("identity mapped wrong: %+v", res)
	}
	if res.Requested.Created.Autonomous != true || res.Requested.Created.OnBehalfOf != "owner" {
		t.Errorf("legacy trigger must map to autonomous authorship, got %+v", res.Requested.Created)
	}
	if res.EndedAt == nil || !res.EndedAt.Equal(time.Date(2026, 7, 20, 2, 41, 12, 0, time.UTC)) {
		t.Errorf("finishedAt must map to EndedAt, got %v", res.EndedAt)
	}
	if len(res.Repos) != 3 {
		t.Fatalf("repos = %d, want 3 (two completed + the live one)", len(res.Repos))
	}

	// Repo svc-a: legacy statuses map onto the four terminal states; the legacy ok-bool step
	// ("push") maps to executed; not-applicable keeps its reason.
	a := res.Repos[0]
	wantStates := []model.StepState{model.StepExecuted, model.StepExecuted, model.StepNotApplicable, model.StepExecuted, model.StepExecuted}
	for i, want := range wantStates {
		if a.Stages[i].State != want {
			t.Errorf("svc-a stage %d = %s, want %s", i, a.Stages[i].State, want)
		}
	}
	if !a.Done || !a.Succeeded {
		t.Error("svc-a with no failed/not-executed stage must derive done+succeeded")
	}

	// Repo svc-b: failed before any step ran — still a defined, renderable state (no black box).
	b := res.Repos[1]
	if len(b.Stages) == 0 || b.Stages[0].State != model.StepFailed || b.Stages[0].Reason == "" {
		t.Errorf("svc-b must render a defined failed state with its reason, got %+v", b.Stages)
	}
	if b.Succeeded {
		t.Error("svc-b must not read as succeeded")
	}

	// Live repo svc-c: the running step stays visible, and the pipeline is honestly not done.
	c := res.Repos[2]
	if c.Stages[0].State != model.StepRunning || c.Done {
		t.Errorf("svc-c must map the live running step, not terminalize it: %+v", c)
	}

	// Get by id finds the legacy document too.
	got, ok, err := rs.Get("2026-07-20T02-00-00.000Z")
	if err != nil || !ok || got.RunID != "run_legacy1" {
		t.Fatalf("Get(legacy id) = %+v %v %v", got, ok, err)
	}
}

// New-format documents and the transcript journal round-trip through the store.
func TestResultStoreNewFormatAndTranscript(t *testing.T) {
	rs := NewResultStore(&statepath.Paths{Root: t.TempDir()})
	res := Result{
		ID: "exec_x", RunID: "run_1", Kind: model.KindTodo, StartedAt: time.Now().UTC(),
		Repos: []model.RepoPipeline{{
			Repo: "svc-a",
			Stages: []model.StageView{
				{Stage: model.StagePreflight, State: model.StepExecuted},
				{Stage: model.StageImplement, State: model.StepRunning},
			},
		}},
	}
	if err := rs.Put(res); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := rs.Get("exec_x")
	if err != nil || !ok {
		t.Fatalf("Get: %v %v", ok, err)
	}
	if got.Repos[0].Done {
		t.Error("a pipeline with a running stage must not derive done")
	}
	forRun, err := rs.ForRun("run_1")
	if err != nil || len(forRun) != 1 {
		t.Fatalf("ForRun = %v, %v", forRun, err)
	}
	if err := rs.AppendTranscript("exec_x", []byte(`{"t":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := rs.AppendTranscript("exec_x", []byte(`{"t":2}`)); err != nil {
		t.Fatal(err)
	}
	// The file is 16 bytes; a 10-byte window cuts mid-line, and the torn first line is dropped
	// so the tail starts at a line boundary.
	tail, err := rs.ReadTranscriptTail("exec_x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if tail != "{\"t\":2}\n" {
		t.Errorf("tail = %q, want the last full journal line", tail)
	}
}
