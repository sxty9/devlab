package it

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
)

// REQ-018.1 / K-2: "a restart is requested" and "an execution starts" exclude each other
// ATOMICALLY. Whoever wins, no running execution is ever killed and no start is ever lost — a
// start that loses the race is recorded on the marker and fires after the restart.
func TestRestartAndStartAreMutuallyExclusive(t *testing.T) {
	for attempt := 0; attempt < 25; attempt++ {
		t.Run(fmt.Sprintf("attempt-%02d", attempt), func(t *testing.T) {
			e := newEnv(t, sched.Config{Tick: time.Hour}) // no tick: only the race decides
			e.deps.hold("alpha")
			e.boot(e.ctx)
			e.addTodo("run_race", "races the restart", "alpha")

			var wg sync.WaitGroup
			var code int
			var body []byte
			wg.Add(2)
			go func() {
				defer wg.Done()
				code, body = e.post("/api/mercury/runs/run_race/run", map[string]any{})
			}()
			go func() {
				defer wg.Done()
				if _, err := e.sch.RequestRestart(model.Actor{Autonomous: true, OnBehalfOf: e.user}); err != nil {
					t.Errorf("restart request: %v", err)
				}
			}()
			wg.Wait()

			if code != http.StatusOK {
				t.Fatalf("POST run: %d %s", code, body)
			}
			var out model.StartOutcome
			mustJSON(t, body, &out)

			switch {
			case out.Started:
				// The start won: the execution runs to its end and the restart waits it out —
				// draining, never killing.
				e.waitPhase(out.ExecutionID, model.PhaseRunning)
				if !e.sch.RestartState().Pending {
					t.Error("the restart request was lost to the race")
				}
				if e.sch.RestartReady() {
					t.Error("the restart gate reads free while an execution runs")
				}
				e.deps.release("alpha")
				e.waitPhase(out.ExecutionID, model.PhaseCompleted)
				e.waitFor("the restart gate to open once the run drained", e.sch.RestartReady)

			case out.Queued && out.RestartPending:
				// The restart won: the start is RECORDED (never dropped) and fires in the next
				// process, and nothing was started here.
				st := e.sch.RestartState()
				found := false
				for _, q := range st.QueuedStarts {
					if q.RunID == "run_race" {
						found = true
					}
				}
				if !found {
					t.Fatalf("the queued start was dropped: %+v", st)
				}
				if out.NotStarted == "" {
					t.Error("the requester was not told why nothing started")
				}
				docs, err := e.docs.Live()
				if err != nil {
					t.Fatal(err)
				}
				if len(docs) != 0 {
					t.Errorf("a start slipped past the restart gate: %+v", docs)
				}

			default:
				t.Fatalf("neither outcome is honest: %+v", out)
			}
		})
	}
}

// REQ-013.4: ten slots carry ten concurrent executions — and the eleventh waits instead of being
// dropped. Capacity is a RUNTIME value: it is raised through the config interface, not a restart.
func TestTenSlotsCarryTenConcurrentExecutions(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 10 * time.Millisecond})
	e.boot(e.ctx)

	// The runtime capacity change goes through the service configuration — the env value was
	// only the first-start seed.
	code, body := e.do(e.request(http.MethodPut, "/api/service/config", map[string]any{
		"maxConcurrency": 10, "defaultTimeBudget": "3h", "automergeWindow": "720h",
	}))
	if code != http.StatusOK && code != http.StatusNoContent {
		// The config route belongs to another module; fall back to the store so the load
		// property itself is still proven.
		t.Logf("PUT /api/service/config = %d %s — setting capacity through the store instead", code, body)
		if err := e.settings.Put(runs.Settings{
			MaxConcurrency: 10, DefaultTimeBudget: 3 * time.Hour, AutomergeWindow: 720 * time.Hour,
		}, model.Actor{User: e.user}); err != nil {
			t.Fatal(err)
		}
	}

	const n = 11
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		repo := fmt.Sprintf("repo%02d", i)
		e.deps.hold(repo)
		e.addTodo(fmt.Sprintf("run_%02d", i), "load "+repo, repo)
	}
	for i := 0; i < n; i++ {
		code, body := e.post(fmt.Sprintf("/api/mercury/runs/run_%02d/run", i), map[string]any{"placement": map[string]any{"kind": "queue"}})
		if code != http.StatusOK {
			t.Fatalf("POST run %d: %d %s", i, code, body)
		}
		var out model.StartOutcome
		mustJSON(t, body, &out)
		if out.ExecutionID == "" {
			t.Fatalf("run %d got no execution document: %+v", i, out)
		}
		ids = append(ids, out.ExecutionID)
	}

	// Exactly ten run at once; the eleventh is queued — put to the back, never skipped.
	e.waitFor("ten executions to run at once", func() bool { return e.runningCount() == 10 })
	var slots model.SlotOverview
	e.getJSON("/api/mercury/runs/slots", &slots)
	if slots.Capacity != 10 {
		t.Errorf("slot capacity = %d, want the runtime value 10", slots.Capacity)
	}
	if slots.Occupied != 10 {
		t.Errorf("occupied slots = %d, want 10", slots.Occupied)
	}
	active, _ := e.activeList()
	if len(active) != n {
		t.Errorf("the active LIST holds %d entries, want all %d (queued work is visible too)", len(active), n)
	}
	queued := 0
	for _, v := range active {
		if v.Phase == model.PhaseQueued {
			queued++
		}
	}
	if queued != 1 {
		t.Errorf("queued executions = %d, want exactly 1 (the eleventh waits)", queued)
	}

	// Draining one slot admits the waiting execution by itself.
	e.deps.release("repo00")
	e.waitPhase(ids[0], model.PhaseCompleted)
	e.waitFor("the queued execution to be admitted", func() bool {
		for _, v := range mustActive(e) {
			if v.Phase == model.PhaseQueued {
				return false
			}
		}
		return true
	})
	for i := 1; i < n; i++ {
		e.deps.release(fmt.Sprintf("repo%02d", i))
	}
}

// REQ-014: exclusivity is per REPOSITORY only — two executions never work the same repo, and a
// busy repo is queued behind, never skipped.
func TestRepositoryExclusivityQueuesInsteadOfSkipping(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 10 * time.Millisecond})
	e.boot(e.ctx)
	e.deps.hold("shared")
	e.addTodo("run_first", "first at the shared repo", "shared")
	e.addTodo("run_second", "second at the shared repo", "shared")

	code, body := e.post("/api/mercury/runs/run_first/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST first: %d %s", code, body)
	}
	var first model.StartOutcome
	mustJSON(t, body, &first)
	e.waitPhase(first.ExecutionID, model.PhaseRunning)

	code, body = e.post("/api/mercury/runs/run_second/run", map[string]any{"placement": map[string]any{"kind": "queue"}})
	if code != http.StatusOK {
		t.Fatalf("POST second: %d %s", code, body)
	}
	var second model.StartOutcome
	mustJSON(t, body, &second)
	if second.Started {
		t.Fatal("two executions were admitted to the same repository")
	}
	if second.ExecutionID == "" {
		t.Fatalf("the contended start was dropped instead of queued: %+v", second)
	}

	// The queued one is admitted the moment the repo frees up — later, but never skipped.
	e.deps.release("shared")
	e.waitPhase(first.ExecutionID, model.PhaseCompleted)
	e.waitPhase(second.ExecutionID, model.PhaseCompleted)

	// "Never skipped" is asked of the second execution's own pipeline: it VISITED the repository,
	// recorded every stage terminally — AND did its own task there.
	//
	// These are two DIFFERENT todos. mercury-dev is shared, so once the first commits, the workbench
	// runs ahead; that attests undelivered work EXISTS, never whose it is. Reading it as the second
	// task's work is the fault measured on 2026-07-31: the second run created nothing, every stage
	// still reported executed, a delivery of the FIRST run's commits opened a pull request, and the
	// work that was asked for never came into existence. All 23 workbenches were ahead at the time,
	// so it applied to every new task on every repository.
	//
	// So the second run observes not-implemented — the ahead commits are named as another run's —
	// and its agent runs. It re-implements nothing; it implements something else. A second agent run
	// over the SAME task is still the defect, and it is fenced where it belongs: by the run-scoped
	// rest path in preflight.Derive.
	stages := e.stagesOf(second.ExecutionID, "shared")
	assertAllTerminal(t, "shared (second execution)", stages)
	if stages[0].State != model.StepExecuted {
		t.Errorf("the queued execution never reached the repository: preflight = %s (%s)",
			stages[0].State, stageNames(stages))
	}
	if got := taskStateOf(t, e, second.ExecutionID, "shared"); got != model.TaskNotImplemented {
		t.Errorf("the second run observed %q at the shared repository, want not-implemented "+
			"(the ahead commits are the FIRST run's work; this task has none yet)", got)
	}
	if runs := e.deps.repo("shared").agentRuns; runs != 2 {
		t.Errorf("the agent ran %d times at the shared repository, want exactly 2 — a second, "+
			"different task must be implemented, not silently declared done", runs)
	}
}

// taskStateOf reads the observation the chain recorded for one repository of one execution — from
// the result document, which is where the derived task state is kept (never a stored flag, K-3).
func taskStateOf(t *testing.T, e *env, execID, repo string) model.TaskState {
	t.Helper()
	res, ok, err := e.results.Get(execID)
	if err != nil || !ok {
		t.Fatalf("result %s: ok=%v err=%v", execID, ok, err)
	}
	for _, rp := range res.Repos {
		if rp.Repo == repo {
			return rp.TaskState
		}
	}
	t.Fatalf("result %s carries no observation for %s", execID, repo)
	return ""
}

func (e *env) runningCount() int {
	docs, err := e.docs.Live()
	if err != nil {
		return -1
	}
	n := 0
	for _, d := range docs {
		if d.Phase == model.PhaseRunning {
			n++
		}
	}
	return n
}

func mustActive(e *env) []model.ExecutionView {
	a, _ := e.activeList()
	return a
}
