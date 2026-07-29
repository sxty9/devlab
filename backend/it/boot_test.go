package it

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/fsatomic"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/sched"
)

// The whole chain, end to end, over the real motor and the real route table: a todo starts
// through HTTP, the five stages of the ONE chain run, every stage ends in a terminal state, the
// work is published, a pull request exists, the ledger carries the delivery, and the history
// serves the same result document the live view served (REQ-027, REQ-030, K-6).
func TestBootChainEndToEnd(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 30 * time.Millisecond})
	ctx := e.ctx
	e.deps.repo("alpha")
	e.addTodo("run_alpha", "Add the honest stage array", "alpha")
	e.boot(ctx)

	code, body := e.post("/api/mercury/runs/run_alpha/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	if !out.Started || out.ExecutionID == "" {
		t.Fatalf("the start was not honest: %+v", out)
	}
	// The B-4 gate reports the observed task state per target — never a guess.
	if out.TaskStates["alpha"] != model.TaskNotImplemented {
		t.Errorf("gate observation = %q, want not-implemented", out.TaskStates["alpha"])
	}

	e.waitPhase(out.ExecutionID, model.PhaseCompleted)

	stages := e.stagesOf(out.ExecutionID, "alpha")
	assertAllTerminal(t, "alpha", stages)
	if len(stages) != len(model.ChainStages()) {
		t.Fatalf("the chain recorded %d stages, want %d (%s)", len(stages), len(model.ChainStages()), stageNames(stages))
	}
	for i, want := range model.ChainStages() {
		if stages[i].Stage != want {
			t.Errorf("stage %d = %s, want %s (the chain order is data, not a switch)", i, stages[i].Stage, want)
		}
	}

	// The agent's committed work was published (K-1: publish after commit) and a PR exists on
	// the ONE PR path.
	r := e.deps.repo("alpha")
	if len(r.published) == 0 {
		t.Error("nothing was published — committed work must be secured the moment it exists")
	}
	if len(e.deps.prs) != 1 {
		t.Errorf("pull requests opened: %d, want exactly 1", len(e.deps.prs))
	}
	dels, err := e.deliveries.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(dels) != 1 || dels[0].Repo != "alpha" || dels[0].PRNumber == 0 {
		t.Fatalf("the delivery ledger does not carry the delivery: %+v", dels)
	}
	if dels[0].ExecutionID != out.ExecutionID {
		t.Errorf("ledger execution = %q, want %q", dels[0].ExecutionID, out.ExecutionID)
	}

	// Consumption climbed and is informative only — the result carries figures, never a cap.
	res, ok, err := e.results.Get(out.ExecutionID)
	if err != nil || !ok {
		t.Fatalf("result document: ok=%v err=%v", ok, err)
	}
	if res.Usage.InputTokens == 0 || res.Usage.OutputTokens == 0 {
		t.Errorf("usage did not climb: %+v", res.Usage)
	}
	if res.Report == "" {
		t.Error("the execution report is empty")
	}

	// The transcript survived as a journal (one JSON object per line).
	tail, err := e.results.ReadTranscriptTail(out.ExecutionID, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tail, "implemented the change") {
		t.Errorf("the transcript journal lost the agent's output: %q", tail)
	}
}

// Boot step 3 (REQ-039.1): a document left in "running" by process death is interrupted BEFORE
// the first request is served — the UI never sees a state that does not exist.
func TestBootExorcisesGhostsBeforeServing(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: time.Hour}) // no tick: the boot steps alone must do it
	ctx := e.ctx
	e.addTodo("run_ghost", "left running by a crash", "alpha")

	// A ghost exactly as a killed process leaves it behind.
	doc, err := e.docs.Create("run_ghost", model.KindTodo, []string{"alpha"}, false, model.Actor{User: e.user})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.docs.Update(doc.ID, func(d *execstate.Doc) error {
		now := time.Now().UTC()
		d.StartedAt = &now
		d.Continuation = &model.ContinuationView{Repo: "alpha", Stage: model.StageImplement}
		d.SetPhase(model.PhaseRunning, "", model.Actor{Autonomous: true}, now)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Block the agent so the auto-resume does not immediately finish the execution: the
	// assertion is about the state right after boot.
	e.deps.hold("alpha")
	rep := e.boot(ctx)
	if rep.Ghosts != 1 {
		t.Fatalf("ghosts exorcised = %d, want 1", rep.Ghosts)
	}

	// The transition protocol carries the honest reason, and the continuation is preserved.
	after, _, err := e.docs.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Continuation == nil || after.Continuation.Stage != model.StageImplement {
		t.Errorf("the continuation point was lost: %+v", after.Continuation)
	}
	sawInterrupted := false
	for _, tr := range after.Transitions {
		if tr.To == model.PhaseInterrupted && tr.Reason == "process death" && tr.By.Autonomous {
			sawInterrupted = true
		}
	}
	if !sawInterrupted {
		t.Errorf("no autonomous interruption on the transition protocol: %+v", after.Transitions)
	}

	// Whatever the auto-resume does next, the execution is never reported as running-since-crash.
	active, _ := e.activeList()
	if len(active) != 1 {
		t.Fatalf("active list = %d entries, want 1", len(active))
	}
	if active[0].Phase == model.PhaseRunning && active[0].StartedAt.Equal(*after.StartedAt) {
		t.Error("the crashed execution is still presented as the run that never stopped")
	}
	e.deps.release("alpha")
}

// Boot step 4 (REQ-018.3/4): a present restart marker means THIS boot is the restart — the
// marker is consumed, the requester is informed through the notice pool, and the starts recorded
// while the restart was pending fire by themselves.
func TestBootCompletesRestartAndReleasesQueuedStarts(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond})
	ctx := e.ctx
	e.deps.repo("alpha")
	e.addTodo("run_queued", "queued behind the restart", "alpha")

	marker := model.RestartState{
		Pending: true, RequestedBy: model.Actor{User: e.user},
		RequestedAt: time.Now().UTC().Add(-time.Minute), Deadline: time.Now().UTC().Add(time.Hour),
		QueuedStarts: []model.QueuedStart{{RunID: "run_queued", Title: "queued behind the restart", By: model.Actor{User: e.user}, At: time.Now().UTC()}},
	}
	if err := fsatomic.WriteJSON(e.paths.RestartMarker(), marker); err != nil {
		t.Fatal(err)
	}

	rep := e.boot(ctx)
	if rep.Released != 1 {
		t.Fatalf("queued starts released = %d, want 1", rep.Released)
	}
	if _, err := os.Stat(e.paths.RestartMarker()); !os.IsNotExist(err) {
		t.Errorf("the restart marker survived the boot it completed: %v", err)
	}
	if st := e.sch.RestartState(); st.Pending {
		t.Error("a restart still reads as pending after it completed")
	}

	notices, err := e.notices.List()
	if err != nil {
		t.Fatal(err)
	}
	told := false
	for _, n := range notices {
		if n.Kind == "restart-completed" && strings.Contains(n.Message(), e.user) {
			told = true
		}
	}
	if !told {
		t.Errorf("the requester was never told the restart completed: %+v", notices)
	}
	e.waitFor("the released start to run", func() bool {
		docs, err := e.docs.List()
		return err == nil && len(docs) == 1
	})
}

// The ready socket is the restart wrapper's only question, and it lives ONLY on the Unix socket:
// 423 while an execution runs, 204 once it drains — and never on the TCP mux (A2-7).
func TestReadySocketIsTheOnlyRestartGate(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: 20 * time.Millisecond})
	ctx := e.ctx
	e.deps.hold("alpha")
	e.addTodo("run_busy", "holds a slot", "alpha")
	e.boot(ctx)
	e.serveReady(ctx)

	if code, err := e.readyProbe(); err != nil || code != http.StatusNoContent {
		t.Fatalf("idle probe = %d (%v), want 204", code, err)
	}

	code, body := e.post("/api/mercury/runs/run_busy/run", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}
	var out model.StartOutcome
	mustJSON(t, body, &out)
	e.waitPhase(out.ExecutionID, model.PhaseRunning)

	e.waitFor("the ready socket to report busy", func() bool {
		c, err := e.readyProbe()
		return err == nil && c == http.StatusLocked
	})

	// The same question over the public mux must not exist at all.
	rcode, _ := e.do(e.request(http.MethodGet, "/ready", nil))
	if rcode != http.StatusNotFound {
		t.Errorf("GET /ready over TCP = %d, want 404 — readiness lives on the socket only", rcode)
	}

	e.deps.release("alpha")
	e.waitPhase(out.ExecutionID, model.PhaseCompleted)
	e.waitFor("the ready socket to report free again", func() bool {
		c, err := e.readyProbe()
		return err == nil && c == http.StatusNoContent
	})
}

// REQ-034: ONE live stream carries topic NAMES only; a change made through the API reaches a
// subscriber without any poll.
func TestLiveStreamCarriesTopicNamesOnly(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: time.Hour})
	ctx := e.ctx
	e.deps.repo("alpha")
	e.addTodo("run_stream", "watched live", "alpha")
	e.boot(ctx)

	req := e.request(http.MethodGet, "/api/events", nil)
	req = req.WithContext(ctx)
	res, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/events = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}

	seen := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			line := sc.Text()
			if payload, ok := strings.CutPrefix(line, "data: "); ok {
				seen <- payload
			}
		}
		close(seen)
	}()

	e.waitFor("the subscription to be registered", func() bool { return e.broker.Subscribers() == 1 })
	if code, body := e.post("/api/mercury/runs/run_stream/run", map[string]any{}); code != http.StatusOK {
		t.Fatalf("POST run: %d %s", code, body)
	}

	known := map[string]bool{}
	for _, tp := range live.Topics() {
		known[string(tp)] = true
	}
	deadline := time.After(5 * time.Second)
	got := 0
	for got == 0 {
		select {
		case payload, ok := <-seen:
			if !ok {
				t.Fatal("the stream closed before any tick arrived")
			}
			if !known[payload] {
				t.Fatalf("the stream carried %q — only the eight topic names are allowed, never data", payload)
			}
			got++
		case <-deadline:
			t.Fatal("no topic tick arrived — the ONE live mechanism did not reach the subscriber")
		}
	}
}

// mustJSON decodes body into out or fails.
func mustJSON(t *testing.T, body []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}
