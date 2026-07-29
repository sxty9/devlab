package it

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
)

// REQ-027.3 / B-17 / REQ-037.4: a pre-rebuild result document stays READABLE through the normal
// history route, every one of its entries renders a DEFINED state, and the states the new chain
// no longer produces ("skipped because of a setting", a step left running) survive as display
// facts — never as a blank screen and never as a success.
func TestLegacyResultsStayReadableThroughTheHistoryRoute(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: time.Hour})
	e.boot(e.ctx)

	// The legacy archive lies where the state root says it does — nothing is ever written there
	// again; it is read only.
	raw, err := os.ReadFile(filepath.Join("testdata", "legacy-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.paths.LegacyResults(), "run_legacy_sweep")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-07-20T02-00-00.000Z.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// A torn husk beside it must not black out the history either.
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out struct {
		Executions []runs.Result `json:"executions"`
	}
	e.getJSON("/api/mercury/runs/executions", &out)
	if len(out.Executions) != 1 {
		t.Fatalf("history holds %d executions, want exactly the one legacy record (a torn file must be skipped, not fatal)", len(out.Executions))
	}
	res := out.Executions[0]
	if !res.Legacy {
		t.Error("the legacy record is not marked as legacy provenance")
	}
	if res.Kind != model.KindAuto {
		t.Errorf("kind = %q, want auto (an unstamped legacy type reads as auto)", res.Kind)
	}
	if res.Prompt == "" {
		t.Error("the prompt snapshot of the legacy execution was dropped — the history must show it")
	}
	if res.Usage.InputTokens != 123456 || res.Usage.CostUSD == 0 {
		t.Errorf("legacy consumption lost: %+v", res.Usage)
	}
	if len(res.Repos) != 3 {
		t.Fatalf("legacy repositories = %d, want 3 (two recorded plus the live one)", len(res.Repos))
	}

	byRepo := map[string]model.RepoPipeline{}
	for _, rp := range res.Repos {
		byRepo[rp.Repo] = rp
	}

	// Every entry renders a defined state: no repository is stageless, so nothing is a black screen.
	for repo, rp := range byRepo {
		if len(rp.Stages) == 0 {
			t.Errorf("%s renders no state at all — every history entry must render a defined one", repo)
		}
		for _, sv := range rp.Stages {
			if sv.State == "" {
				t.Errorf("%s: stage %q carries no state", repo, sv.Stage)
			}
		}
	}

	// The legacy step names are carried VERBATIM — displayable, never produced anew.
	a := byRepo["svc-a"]
	if len(a.Stages) != 5 || a.Stages[0].Stage != "analyze" || a.Stages[2].Stage != "dev-deploy" {
		t.Fatalf("legacy step names were rewritten: %s", stageNames(a.Stages))
	}
	if a.Stages[2].State != model.StepNotApplicable {
		t.Errorf("the legacy skip reads as %q, want not-applicable", a.Stages[2].State)
	}
	if !a.Succeeded {
		t.Errorf("svc-a should read as succeeded: %s", stageNames(a.Stages))
	}

	// "skipped because of a setting" is a FAILURE in the new reading — a skip is never a success.
	b := byRepo["svc-b"]
	if b.Succeeded {
		t.Error("a repository skipped by a setting reads as success — a skip is never green")
	}
	if b.Stages[0].State != model.StepFailed || b.Stages[0].Reason == "" {
		t.Errorf("the legacy skip is not named as a failure: %+v", b.Stages[0])
	}

	// A step the old system left "running" is NOT terminal — so the pipeline is not done, and the
	// entry renders as the unfinished thing it is instead of pretending completion.
	c := byRepo["svc-c"]
	if c.Done || c.Succeeded {
		t.Errorf("a legacy execution frozen mid-step reads as done=%v succeeded=%v", c.Done, c.Succeeded)
	}
	if c.Stages[0].State != model.StepRunning {
		t.Errorf("the frozen legacy step reads as %q, want running (display-only, never produced anew)", c.Stages[0].State)
	}

	// The detail route serves the same document — one data path for list and detail.
	var detail runs.Result
	e.getJSON("/api/mercury/runs/run_legacy_sweep/results/2026-07-20T02-00-00.000Z", &detail)
	if detail.ID != res.ID || len(detail.Repos) != len(res.Repos) {
		t.Errorf("the detail route serves a different document: %+v", detail)
	}
}
