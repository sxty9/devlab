package it

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/sched"
)

// K-2 / REQ-018.6 / REQ-019.1 / REQ-039.1 — the whole life cycle of a killed execution:
// SIGTERM while it runs ⇒ persisted as interrupted WITH its continuation ⇒ the next process
// resumes it under the SAME identity, at the same spot, and does NOT repeat the repository that
// already finished.
func TestKilledExecutionIsInterruptedAndResumesAtTheSameSpot(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond, DrainGrace: 2 * time.Second})
	e.deps.repo("alpha")
	e.deps.hold("beta") // beta's agent hangs — the drain has to interrupt it
	e.boot(e.ctx)
	e.addTodo("run_two", "two targets, one hangs", "alpha", "beta")

	code, body := e.post("/api/mercury/runs/run_two/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	execID := out.ExecutionID

	// Wait until alpha is done and beta is being worked.
	e.waitFor("alpha to finish and beta to start", func() bool {
		d, ok, err := e.docs.Get(execID)
		if err != nil || !ok {
			return false
		}
		a, b := d.Repo("alpha"), d.Repo("beta")
		return a != nil && b != nil && a.State == "done" && b.State == "active"
	})
	transcriptBefore, err := e.results.ReadTranscriptTail(execID, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(transcriptBefore, "implemented the change") {
		t.Fatalf("nothing was streamed before the kill: %q", transcriptBefore)
	}

	// ── the kill: SIGTERM ⇒ drain ⇒ the process dies ────────────────────────────────────────
	next := e.reboot(sched.Config{Tick: 20 * time.Millisecond})

	// The document survived the process as interrupted, with its continuation and its identity.
	killed, ok, err := next.docs.Get(execID)
	if err != nil || !ok {
		t.Fatalf("the execution document did not survive the process: ok=%v err=%v", ok, err)
	}
	if killed.Phase != model.PhaseInterrupted {
		t.Fatalf("phase after the kill = %q, want interrupted (never a lingering \"running\")", killed.Phase)
	}
	if a := killed.Repo("alpha"); a == nil || a.State != "done" {
		t.Errorf("the finished repository lost its completion: %+v", a)
	}

	// ── the next process: boot ⇒ auto-resume under the same identity ────────────────────────
	next.deps.repo("alpha")
	next.deps.repo("beta")
	next.boot(next.ctx)
	after := next.waitPhase(execID, model.PhaseCompleted)
	if after.ID != execID {
		t.Fatalf("resume minted a new identity %q instead of continuing %q", after.ID, execID)
	}

	// The completed repository was NOT worked again; the interrupted one was.
	if runs := next.deps.repo("alpha").agentRuns; runs != 0 {
		t.Errorf("the already-completed repository was worked again (%d agent runs) — resume must not repeat it", runs)
	}
	if runs := next.deps.repo("beta").agentRuns; runs != 1 {
		t.Errorf("the interrupted repository was worked %d times, want exactly 1", runs)
	}

	// The transcript of the first attempt survived the kill (it is a journal, not a summary).
	tail, err := next.results.ReadTranscriptTail(execID, 16384)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tail, "implemented the change") {
		t.Errorf("the transcript did not survive the kill: %q", tail)
	}

	// The transition protocol tells the whole story: running → interrupted → queued → running.
	var seq []model.ExecPhase
	for _, tr := range after.Transitions {
		seq = append(seq, tr.To)
	}
	if !containsSubsequence(seq, []model.ExecPhase{model.PhaseRunning, model.PhaseInterrupted, model.PhaseQueued, model.PhaseRunning, model.PhaseCompleted}) {
		t.Errorf("the transition protocol does not tell the kill-and-resume story: %v", seq)
	}
}

// REQ-019.3: an execution older than the resume window is reliably abandoned — visibly discarded,
// never silently resumed hours later.
func TestExecutionsOlderThanTheResumeWindowAreAbandonedVisibly(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond, DrainGrace: time.Second})
	e.deps.hold("alpha")
	e.boot(e.ctx)
	e.addTodo("run_old", "interrupted long ago", "alpha")
	code, body := e.post("/api/mercury/runs/run_old/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	e.waitPhase(out.ExecutionID, model.PhaseRunning)

	// The next process has a resume window of a nanosecond: everything is older than that.
	next := e.reboot(sched.Config{Tick: 20 * time.Millisecond, ResumeWindow: time.Nanosecond})
	next.deps.repo("alpha")
	next.boot(next.ctx)

	doc := next.waitPhase(out.ExecutionID, model.PhaseDiscarded)
	if !strings.Contains(doc.Reason, "resume window") {
		t.Errorf("the abandonment is not named: %q", doc.Reason)
	}
	if next.deps.repo("alpha").agentRuns != 0 {
		t.Error("an abandoned execution was resumed anyway")
	}
	notices, err := next.notices.List()
	if err != nil {
		t.Fatal(err)
	}
	told := false
	for _, n := range notices {
		if n.Kind == "execution-abandoned" {
			told = true
		}
	}
	if !told {
		t.Errorf("the abandonment reached nobody: %+v", notices)
	}
}

// containsSubsequence reports whether want appears in order (not necessarily contiguously) in got.
func containsSubsequence[T comparable](got, want []T) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}
