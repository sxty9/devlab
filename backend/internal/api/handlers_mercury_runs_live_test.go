package api

import (
	"strings"
	"testing"

	"devlab/backend/internal/runs"
)

func TestLiveSaverThrottleAndForce(t *testing.T) {
	n := 0
	ls := &liveSaver{do: func() { n++ }}
	ls.throttled() // last is the zero time → far past the interval → fires
	if n != 1 {
		t.Fatalf("first throttled should fire, n=%d", n)
	}
	ls.throttled() // within the interval → coalesced away
	ls.throttled()
	if n != 1 {
		t.Fatalf("throttled within the interval should coalesce, n=%d", n)
	}
	ls.force() // force always persists (a boundary event)
	if n != 2 {
		t.Fatalf("force should fire, n=%d", n)
	}
}

// The agent step is what the UI follows live: a running step whose log grows with the transcript, then
// finalizes to the report. This pins that lifecycle down.
func TestAgentStepLifecycle(t *testing.T) {
	rr := &runs.RepoResult{Repo: "r"}
	saves := 0
	ls := &liveSaver{do: func() { saves++ }}

	ag := beginAgentStep(rr, ls, "implement")
	if len(rr.Steps) != 1 || !rr.Steps[0].Running || rr.Steps[0].Name != "implement" {
		t.Fatalf("begin did not add a running step: %+v", rr.Steps)
	}
	if saves != 1 {
		t.Fatalf("begin should force a save, saves=%d", saves)
	}

	// A streamed assistant text line updates the running step's log in place.
	ag.onProgress([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}`))
	if !strings.Contains(rr.Steps[0].Log, "working on it") {
		t.Fatalf("onProgress did not update the live log: %q", rr.Steps[0].Log)
	}
	if !rr.Steps[0].Running {
		t.Fatal("step must still be running mid-stream")
	}

	ag.finish("Done: implemented X.")
	s := rr.Steps[0]
	if s.Running || !s.OK || s.Log != "Done: implemented X." {
		t.Fatalf("finish did not finalize the step: %+v", s)
	}
}

func TestAgentStepFail(t *testing.T) {
	rr := &runs.RepoResult{Repo: "r"}
	ag := beginAgentStep(rr, &liveSaver{do: func() {}}, "analyze")
	ag.fail("boom")
	s := rr.Steps[0]
	if s.Running || s.OK || s.Log != "boom" {
		t.Fatalf("fail did not finalize the step as failed: %+v", s)
	}
}
