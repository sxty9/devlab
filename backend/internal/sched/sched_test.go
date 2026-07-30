package sched

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/preflight"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/statepath"
)

// ── Harness ──────────────────────────────────────────────────────────────────────────────

type fakePub struct {
	mu     sync.Mutex
	topics []live.Topic
}

func (p *fakePub) Publish(t live.Topic) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, t)
}

func (p *fakePub) has(t live.Topic) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.topics {
		if x == t {
			return true
		}
	}
	return false
}

// fakeExec is a controllable ExecFunc: it signals each start and blocks until released (or the
// context ends). ignoreCtx simulates a stuck agent that outlives its cancellation.
type fakeExec struct {
	mu     sync.Mutex
	starts []string // execution ids in start order
	// docs holds EVERY handover per execution id, in order — a resume is a second handover of the
	// same id, and a test that asks what the executor received must be able to name which one.
	docs      map[string][]execstate.Doc
	release   map[string]chan struct{}
	ignoreCtx bool
	// drained latches "release everything, now and from now on" — the teardown. Without the latch a
	// goroutine that has not yet ENTERED fn would create a fresh, open channel afterwards and block
	// past its test.
	drained bool
}

func newFakeExec() *fakeExec {
	return &fakeExec{docs: map[string][]execstate.Doc{}, release: map[string]chan struct{}{}}
}

func (f *fakeExec) fn(ctx context.Context, doc execstate.Doc, run runs.Run) error {
	f.mu.Lock()
	f.starts = append(f.starts, doc.ID)
	f.docs[doc.ID] = append(f.docs[doc.ID], doc)
	if f.drained {
		f.mu.Unlock()
		return nil // teardown: a start that arrives now finishes at once
	}
	ch, ok := f.release[doc.ID]
	if !ok {
		ch = make(chan struct{})
		f.release[doc.ID] = ch
	}
	f.mu.Unlock()
	if f.ignoreCtx {
		<-ch
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return nil
	}
}

func (f *fakeExec) releaseExec(id string) {
	f.mu.Lock()
	ch, ok := f.release[id]
	if !ok {
		ch = make(chan struct{})
		f.release[id] = ch
	}
	f.mu.Unlock()
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// releaseAll releases every execution the fake is holding AND latches the release, so a start that
// only reaches the fake afterwards finishes immediately — the shutdown a test cleanup performs.
func (f *fakeExec) releaseAll() {
	f.mu.Lock()
	f.drained = true
	ids := make([]string, 0, len(f.release))
	for id := range f.release {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.releaseExec(id)
	}
}

func (f *fakeExec) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

// startedDoc returns the nth (1-based) document the executor was handed for id.
func (f *fakeExec) startedDoc(id string, nth int) (execstate.Doc, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.docs[id]
	if nth < 1 || len(list) < nth {
		return execstate.Doc{}, false
	}
	return list[nth-1], true
}

type harness struct {
	t     *testing.T
	paths *statepath.Paths
	docs  *execstate.Store
	runs  *runs.Store
	res   *runs.ResultStore
	sch   *Scheduler
	pub   *fakePub
	exec  *fakeExec
	cap   atomic.Int64
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "state", "mercury", "executions"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", "")
	paths := &statepath.Paths{Root: filepath.Join(dir, "state")}
	docs, err := execstate.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		t:     t,
		paths: paths,
		docs:  docs,
		runs:  runs.NewStore(nil),
		res:   runs.NewResultStore(paths),
		pub:   &fakePub{},
		exec:  newFakeExec(),
	}
	h.cap.Store(2)
	h.sch = New(cfg, docs, h.runs, h.res, nil, nil, h.exec.fn, nil, h.pub)
	// The settings seam: capacity adjustable at runtime without a restart (REQ-013.2) — the
	// runtime store is B8's; the scheduler consumes whatever it currently says.
	h.sch.settings = func() (runs.Settings, error) {
		return runs.Settings{MaxConcurrency: int(h.cap.Load())}, nil
	}
	// No execution goroutine may outlive its test: it would still be writing documents while Go
	// removes the temporary state root ("directory not empty"). Release every blocked fake and wait
	// for the goroutines to wind down — the same order SIGTERM uses.
	t.Cleanup(func() {
		h.exec.releaseAll()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			h.sch.mu.Lock()
			n := len(h.sch.running)
			h.sch.mu.Unlock()
			if n == 0 {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Error("execution goroutines outlived their test — the state root is torn down under them")
	})
	return h
}

func (h *harness) addTodo(id, title string, repos ...string) runs.Run {
	h.t.Helper()
	r := runs.Run{ID: id, Kind: model.KindTodo, Title: title}
	for _, repo := range repos {
		r.Targets = append(r.Targets, runs.Target{Repo: repo})
	}
	if err := h.runs.Put(r); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) submit(runID string, placement *Placement) model.StartOutcome {
	h.t.Helper()
	out, err := h.sch.Submit(context.Background(), StartRequest{RunID: runID, By: model.Actor{User: "ada"}, Placement: placement})
	if err != nil {
		h.t.Fatalf("submit %s: %v", runID, err)
	}
	return out
}

func (h *harness) waitPhase(execID string, want model.ExecPhase) execstate.Doc {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, ok, err := h.docs.Get(execID)
		if err != nil {
			h.t.Fatal(err)
		}
		if ok && d.Phase == want {
			return d
		}
		time.Sleep(2 * time.Millisecond)
	}
	d, _, _ := h.docs.Get(execID)
	h.t.Fatalf("execution %s never reached %s (is %s)", execID, want, d.Phase)
	return execstate.Doc{}
}

// waitStarted waits until the EXECUTOR was handed this execution for the nth time, and returns the
// document it received then. The document's phase flips to running BEFORE the goroutine launches (the
// document is the truth, the goroutine follows it), so a test that inspects what the executor got
// must wait for the executor — waiting for the phase alone is a race, and after a resume the FIRST
// handover is the stale one.
func (h *harness) waitStarted(execID string, nth int) execstate.Doc {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d, ok := h.exec.startedDoc(execID, nth); ok {
			return d
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("the executor never received handover %d of execution %s", nth, execID)
	return execstate.Doc{}
}

// waitIdle waits until every execution goroutine has wound down (the doc phase flips first;
// the goroutine — and with it the slot handle — exits shortly after).
func (h *harness) waitIdle() {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.sch.mu.Lock()
		n := len(h.sch.running)
		h.sch.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatal("execution goroutines never wound down")
}

func (h *harness) runningCount() int {
	h.t.Helper()
	docs, err := h.docs.Live()
	if err != nil {
		h.t.Fatal(err)
	}
	n := 0
	for _, d := range docs {
		if d.Phase == model.PhaseRunning {
			n++
		}
	}
	return n
}

// ── REQ-013: concurrency, the list, runtime capacity, load, targeted abort ───────────────

// Two runs in two repos execute in parallel, and the answer about active executions is a LIST.
func TestTwoRunsInTwoReposRunConcurrently(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "beta")

	outA := h.submit("run_a", nil)
	outB := h.submit("run_b", nil)
	if !outA.Started || !outB.Started {
		t.Fatalf("both must start: %+v %+v", outA, outB)
	}
	if h.runningCount() != 2 {
		t.Fatalf("want 2 running, got %d", h.runningCount())
	}
	list, err := h.sch.ActiveList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("the active answer is a LIST of 2, got %d", len(list))
	}
	ov, err := h.sch.SlotOverview()
	if err != nil {
		t.Fatal(err)
	}
	if ov.Occupied != 2 || ov.Capacity != 2 {
		t.Fatalf("overview: %+v", ov)
	}
	h.exec.releaseExec(outA.ExecutionID)
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outA.ExecutionID, model.PhaseCompleted)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
}

// Capacity changes at runtime, no restart: a raise starts the waiting execution at once, a
// lowering kills nothing and drains by completion (REQ-013.2/013.3).
func TestCapacityChangesAtRuntime(t *testing.T) {
	h := newHarness(t, Config{})
	h.cap.Store(1)
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "beta")

	outA := h.submit("run_a", nil)
	outB := h.submit("run_b", &Placement{Kind: PlacementQueue})
	if !outA.Started || !outB.Queued {
		t.Fatalf("expected started+queued: %+v %+v", outA, outB)
	}

	// Raise → the queued execution is admitted on the next pass, without any restart.
	h.cap.Store(2)
	h.sch.pass(context.Background(), false)
	h.waitPhase(outB.ExecutionID, model.PhaseRunning)

	// Lower → nothing is killed; both keep running; the floor drains by completion.
	h.cap.Store(1)
	h.sch.pass(context.Background(), false)
	if h.runningCount() != 2 {
		t.Fatalf("lowering must not kill: got %d running", h.runningCount())
	}
	// A third start now queues (over the lowered cap).
	h.addTodo("run_c", "C", "gamma")
	outC := h.submit("run_c", &Placement{Kind: PlacementQueue})
	if !outC.Queued {
		t.Fatalf("expected queued over the lowered cap: %+v", outC)
	}
	h.exec.releaseExec(outA.ExecutionID)
	h.waitPhase(outA.ExecutionID, model.PhaseCompleted)
	h.sch.pass(context.Background(), false)
	// Still over the cap of 1 (B running) — C stays queued.
	if d, _, _ := h.docs.Get(outC.ExecutionID); d.Phase != model.PhaseQueued {
		t.Fatalf("C must stay queued at cap 1, is %s", d.Phase)
	}
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
	h.sch.pass(context.Background(), false)
	h.waitPhase(outC.ExecutionID, model.PhaseRunning)
	h.exec.releaseExec(outC.ExecutionID)
	h.waitPhase(outC.ExecutionID, model.PhaseCompleted)
}

// Load: ten slots verifiably carry ten concurrent executions (REQ-013.4).
func TestTenSlotsCarryTenConcurrentExecutions(t *testing.T) {
	h := newHarness(t, Config{})
	h.cap.Store(10)
	ids := []string{}
	for i := 0; i < 10; i++ {
		runID := "run_" + string(rune('a'+i))
		h.addTodo(runID, runID, "repo-"+string(rune('a'+i)))
		out := h.submit(runID, nil)
		if !out.Started {
			t.Fatalf("start %d refused: %+v", i, out)
		}
		ids = append(ids, out.ExecutionID)
	}
	if h.runningCount() != 10 {
		t.Fatalf("10 slots must carry 10, got %d", h.runningCount())
	}
	for _, id := range ids {
		h.exec.releaseExec(id)
	}
	for _, id := range ids {
		h.waitPhase(id, model.PhaseCompleted)
	}
}

// Abort hits exactly one chosen execution; the second keeps running (REQ-013.5; replacement
// for the deleted runs/abortcause_test.go — the cause is sched.ErrAborted now).
func TestCancelHitsExactlyOneRun(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "beta")
	outA := h.submit("run_a", nil)
	outB := h.submit("run_b", nil)

	if err := h.sch.Cancel("run_a", model.Actor{User: "ada"}); err != nil {
		t.Fatal(err)
	}
	d := h.waitPhase(outA.ExecutionID, model.PhaseFailed)
	if d.Reason != "aborted by user" {
		t.Fatalf("abort reason: %q", d.Reason)
	}
	last := d.Transitions[len(d.Transitions)-1]
	if last.By.User != "ada" {
		t.Fatalf("abort must carry its actor: %+v", last)
	}
	if got, _, _ := h.docs.Get(outB.ExecutionID); got.Phase != model.PhaseRunning {
		t.Fatalf("the OTHER run must keep running, is %s", got.Phase)
	}
	// Cancel with nothing live is a named condition.
	if err := h.sch.Cancel("run_a", model.Actor{}); err == nil {
		t.Fatal("second cancel must report no live execution")
	}
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
}

// ── REQ-014: repo exclusivity, back of the queue ─────────────────────────────────────────

// Two executions on the SAME repo run one after the other, never together.
func TestSameRepoRunsSequentially(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "shared")
	h.addTodo("run_b", "B", "shared")

	outA := h.submit("run_a", nil)
	outB := h.submit("run_b", nil)
	if outB.Started || outB.Queued {
		t.Fatalf("same-repo start without placement must be refused with a reason: %+v", outB)
	}
	if outB.NotStarted == "" {
		t.Fatal("refusal must be named")
	}
	// Queue it explicitly — it waits at the back, never skipped.
	outB = h.submit("run_b", &Placement{Kind: PlacementQueue})
	if !outB.Queued {
		t.Fatalf("queue placement must queue: %+v", outB)
	}
	h.sch.pass(context.Background(), false)
	if d, _, _ := h.docs.Get(outB.ExecutionID); d.Phase != model.PhaseQueued {
		t.Fatalf("B must wait while A holds the repo, is %s", d.Phase)
	}
	h.exec.releaseExec(outA.ExecutionID)
	h.waitPhase(outA.ExecutionID, model.PhaseCompleted)
	h.sch.pass(context.Background(), false)
	h.waitPhase(outB.ExecutionID, model.PhaseRunning)
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
}

// An all-repos auto run claims ONLY the repo it is actively working: a todo in another repo
// runs alongside it; the busy repo is revisited later, never skipped (REQ-014.2/014.3).
func TestAutoRunClaimsOnlyItsActiveRepo(t *testing.T) {
	h := newHarness(t, Config{})
	// The auto run's doc: working alpha right now (beta pending).
	if err := h.runs.Put(runs.Run{ID: "run_auto", Kind: model.KindAuto, Title: "Sweep", Active: true}); err != nil {
		t.Fatal(err)
	}
	doc, err := h.docs.Create("run_auto", model.KindAuto, []string{"alpha", "beta"}, false, model.Actor{Autonomous: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhaseRunning, "", model.Actor{Autonomous: true}, time.Now())
		d.Repos[0].State = execstate.RepoActive
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A live handle without a goroutine: the auto run is "being worked" as far as admission is
	// concerned. Written under the admission mutex — the scheduler reads that map from its own
	// goroutines.
	h.sch.mu.Lock()
	h.sch.running[doc.ID] = &liveExec{runID: "run_auto", cancel: func(error) {}, done: make(chan struct{})}
	h.sch.mu.Unlock()
	defer func() {
		h.sch.mu.Lock()
		delete(h.sch.running, doc.ID)
		h.sch.mu.Unlock()
	}()
	h.cap.Store(3)

	// A todo on the auto run's PENDING repo (beta) is admitted — beta is not claimed.
	h.addTodo("run_beta", "Beta", "beta")
	if out := h.submit("run_beta", nil); !out.Started {
		t.Fatalf("pending repo of an auto run must not block a todo: %+v", out)
	}
	// A todo on the auto run's ACTIVE repo (alpha) is not.
	h.addTodo("run_alpha", "Alpha", "alpha")
	if out := h.submit("run_alpha", nil); out.Started {
		t.Fatal("the actively worked repo is exclusively held")
	}
}

// The pure admission rule puts busy target repos at the BACK of the worklist (never dropped).
func TestAdmitOrdersBusyReposToTheBack(t *testing.T) {
	now := time.Now()
	running := execstate.Doc{
		ID: "exec_1", RunID: "run_1", Kind: model.KindTodo, Phase: model.PhaseRunning,
		Repos: []execstate.RepoProgress{{Repo: "beta", State: execstate.RepoActive}}, CreatedAt: now, StartedAt: &now,
	}
	dec, err := Admit([]execstate.Doc{running}, runs.Settings{MaxConcurrency: 5}, []string{"beta", "alpha"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Admit {
		t.Fatal("busy repo must block plain admission")
	}
	if len(dec.BusyRepos) != 1 || dec.BusyRepos[0] != "beta" {
		t.Fatalf("busy repos must be named: %+v", dec.BusyRepos)
	}
	if dec.Suggestion == nil || dec.Suggestion.ExecutionID != "exec_1" {
		t.Fatalf("the conflicting execution must be suggested for deferral: %+v", dec.Suggestion)
	}
}

// ── REQ-015: defer keeps progress, overload rules, reasoned suggestion ───────────────────

// Defer frees the slot and keeps progress; resume continues the SAME execution at the same
// spot; finished repositories stay finished.
func TestDeferKeepsProgressAndResumeContinues(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha", "beta")
	out := h.submit("run_a", nil)
	// The motor reports: alpha done, beta mid-work, continuation at beta/implement.
	sink := h.sch.DocSink(out.ExecutionID)
	sink.RepoDone(model.RepoPipeline{Repo: "alpha"})
	sink.StageUpdate("beta", model.StageView{Stage: model.StageImplement, State: model.StepRunning})
	sink.Continuation(model.ContinuationView{Repo: "beta", Stage: model.StageImplement})

	if err := h.sch.Defer("run_a", model.Actor{User: "ada"}); err != nil {
		t.Fatal(err)
	}
	d := h.waitPhase(out.ExecutionID, model.PhasePaused)
	if d.Pause == nil || d.Pause.Reason != model.PauseDeferredByUser {
		t.Fatalf("the ONE pause must say deferred-by-user: %+v", d.Pause)
	}
	if d.Continuation == nil || d.Continuation.Repo != "beta" {
		t.Fatalf("continuation lost: %+v", d.Continuation)
	}
	// The slot frees as the goroutine winds down.
	h.waitIdle()
	if h.runningCount() != 0 {
		t.Fatal("defer must free the slot")
	}
	ov, _ := h.sch.SlotOverview()
	if len(ov.Deferred) != 1 || ov.Deferred[0].ID != out.ExecutionID {
		t.Fatalf("overview must show the deferred execution with its continuation: %+v", ov.Deferred)
	}

	// Resume: SAME id, done repos stay done.
	if err := h.sch.Resume("run_a", model.Actor{User: "ada"}); err != nil {
		t.Fatal(err)
	}
	h.waitPhase(out.ExecutionID, model.PhaseRunning)
	got := h.waitStarted(out.ExecutionID, 2) // the resume is the SECOND handover of this id
	if got.Repo("alpha") == nil || got.Repo("alpha").State != execstate.RepoDone {
		t.Fatal("finished repository must STAY finished across defer/resume")
	}
	h.exec.releaseExec(out.ExecutionID)
	h.waitPhase(out.ExecutionID, model.PhaseCompleted)
}

// Overload: only on a cap block, at most one, never past repo exclusivity, gone with the end.
func TestOverloadRules(t *testing.T) {
	h := newHarness(t, Config{})
	h.cap.Store(1)
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "beta")
	h.addTodo("run_c", "C", "gamma")
	h.addTodo("run_d", "D", "alpha")

	outA := h.submit("run_a", nil)
	// Cap block → overload admits ONE extra.
	outB := h.submit("run_b", &Placement{Kind: PlacementOverload})
	if !outB.Started {
		t.Fatalf("overload on a cap block must start: %+v", outB)
	}
	ov, _ := h.sch.SlotOverview()
	if !ov.OverloadActive {
		t.Fatal("overview must show the overload")
	}
	// A second overload never sums.
	outC := h.submit("run_c", &Placement{Kind: PlacementOverload})
	if outC.Started {
		t.Fatal("overloads must never sum — one living overload at most")
	}
	if outC.NotStarted == "" {
		t.Fatal("the refusal must be named")
	}
	// Overload never crosses repo exclusivity.
	outD := h.submit("run_d", &Placement{Kind: PlacementOverload})
	if outD.Started {
		t.Fatal("overload must never cross repository exclusivity")
	}
	// The overload vanishes with the execution's end.
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
	ov, _ = h.sch.SlotOverview()
	if ov.OverloadActive {
		t.Fatal("overload must end with its execution")
	}
	h.exec.releaseExec(outA.ExecutionID)
	h.waitPhase(outA.ExecutionID, model.PhaseCompleted)
}

// A blocked start gets a REASONED defer suggestion; deferring mid-repo work scores worst.
func TestSuggestDeferIsReasoned(t *testing.T) {
	now := time.Now()
	between := execstate.Doc{
		ID: "exec_between", Phase: model.PhaseRunning, Kind: model.KindTodo, CreatedAt: now.Add(-time.Hour),
		Repos: []execstate.RepoProgress{{Repo: "alpha", State: execstate.RepoDone}, {Repo: "beta", State: execstate.RepoPending}},
	}
	midRepo := execstate.Doc{
		ID: "exec_mid", Phase: model.PhaseRunning, Kind: model.KindTodo, CreatedAt: now,
		Repos: []execstate.RepoProgress{{Repo: "gamma", State: execstate.RepoActive}},
	}
	sug := SuggestDefer([]execstate.Doc{midRepo, between})
	if sug == nil || sug.ExecutionID != "exec_between" {
		t.Fatalf("the between-repos execution loses least: %+v", sug)
	}
	if sug.Reason == "" {
		t.Fatal("the suggestion must carry its reason")
	}
	if s2 := SuggestDefer([]execstate.Doc{midRepo}); s2 == nil || s2.Score != 1000 {
		t.Fatalf("mid-repo deferral must score worst: %+v", s2)
	}
}

// ── REQ-016: the shared usage-limit pause ────────────────────────────────────────────────

// The limit pauses ALL running executions together (the limit binds in sum); reason and
// attempt count are visible; the resume probe brings them back together.
func TestUsageLimitPausesAllTogether(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "beta")
	outA := h.submit("run_a", nil)
	outB := h.submit("run_b", nil)

	notBefore := time.Now().Add(-time.Second) // already passed → the next pass may resume
	if err := h.sch.PauseAllUsageLimit("subscription limit reached", notBefore); err != nil {
		t.Fatal(err)
	}
	dA := h.waitPhase(outA.ExecutionID, model.PhasePaused)
	dB := h.waitPhase(outB.ExecutionID, model.PhasePaused)
	for _, d := range []execstate.Doc{dA, dB} {
		if d.Pause == nil || d.Pause.Reason != model.PauseUsageLimit || d.Pause.Message == "" {
			t.Fatalf("pause must carry the ONE reason + message: %+v", d.Pause)
		}
	}
	h.waitIdle()

	// The probe resumes BOTH; the attempt count is visible.
	h.sch.pass(context.Background(), false)
	h.waitPhase(outA.ExecutionID, model.PhaseRunning)
	h.waitPhase(outB.ExecutionID, model.PhaseRunning)
	gA, _, _ := h.docs.Get(outA.ExecutionID)
	if gA.Pause != nil && gA.Pause.ResumeAttempts < 1 {
		t.Fatalf("resume attempts must count: %+v", gA.Pause)
	}
	h.exec.releaseExec(outA.ExecutionID)
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outA.ExecutionID, model.PhaseCompleted)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
}

// A foreign success pulls the resume forward — nobody waits out the deadline blindly.
func TestAgentSuccessPullsResumeForward(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	out := h.submit("run_a", nil)
	// Pause far into the future.
	if err := h.sch.PauseAllUsageLimit("limit", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.waitPhase(out.ExecutionID, model.PhasePaused)
	h.waitIdle()
	// A pass alone must NOT resume (NotBefore is in the future).
	h.sch.pass(context.Background(), false)
	if d, _, _ := h.docs.Get(out.ExecutionID); d.Phase != model.PhasePaused {
		t.Fatalf("must stay paused before NotBefore, is %s", d.Phase)
	}
	// The success signal resumes early.
	h.sch.NoteAgentSuccess()
	h.sch.pass(context.Background(), false)
	h.waitPhase(out.ExecutionID, model.PhaseRunning)
	h.exec.releaseExec(out.ExecutionID)
	h.waitPhase(out.ExecutionID, model.PhaseCompleted)
}

// Past the resume budget the pause turns into a visible block that waits for the UI.
func TestUsageLimitBlocksAfterMaxResumes(t *testing.T) {
	h := newHarness(t, Config{LimitMaxResumes: 1})
	h.addTodo("run_a", "A", "alpha")
	out := h.submit("run_a", nil)
	if err := h.sch.PauseAllUsageLimit("limit", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	h.waitPhase(out.ExecutionID, model.PhasePaused)
	h.waitIdle()
	// First probe: resume attempt 1 → running again.
	h.sch.pass(context.Background(), false)
	h.waitPhase(out.ExecutionID, model.PhaseRunning)
	// Limit strikes again.
	if err := h.sch.PauseAllUsageLimit("limit", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	h.waitPhase(out.ExecutionID, model.PhasePaused)
	h.waitIdle()
	// Second probe: the budget (1) is spent → blocked with a named reason, waiting for the UI.
	h.sch.pass(context.Background(), false)
	d := h.waitPhase(out.ExecutionID, model.PhaseBlocked)
	if d.Reason == "" {
		t.Fatal("the block must be named")
	}
	// UI resumption works.
	if err := h.sch.Resume("run_a", model.Actor{User: "ada"}); err != nil {
		t.Fatal(err)
	}
	h.waitPhase(out.ExecutionID, model.PhaseRunning)
	h.exec.releaseExec(out.ExecutionID)
	h.waitPhase(out.ExecutionID, model.PhaseCompleted)
}

// ── K-3 start ban + gate evidence (B-4) ──────────────────────────────────────────────────

// delivered ⇒ NO document is created; the answer names the reason with per-target evidence.
func TestDeliveredTargetsCreateNoDocument(t *testing.T) {
	h := newHarness(t, Config{})
	h.sch.gate = func(_ context.Context, repo string, _ runs.Run) (preflight.Finding, error) {
		return preflight.Finding{State: model.TaskDelivered, Evidence: []string{"contained in default branch"}}, nil
	}
	h.addTodo("run_a", "A", "alpha", "beta")
	out := h.submit("run_a", nil)
	if out.Started || out.Queued || out.ExecutionID != "" {
		t.Fatalf("all targets delivered ⇒ no document, ever: %+v", out)
	}
	if out.NotStarted == "" || out.TaskStates["alpha"] != model.TaskDelivered {
		t.Fatalf("the refusal must carry reason + evidence: %+v", out)
	}
	docs, _ := h.docs.List()
	if len(docs) != 0 {
		t.Fatalf("no document may exist, found %d", len(docs))
	}
}

// A partially delivered todo drops ONLY the delivered target; unknown is kept (never guessed).
func TestGateDropsOnlyDeliveredTargets(t *testing.T) {
	h := newHarness(t, Config{})
	h.sch.gate = func(_ context.Context, repo string, _ runs.Run) (preflight.Finding, error) {
		switch repo {
		case "alpha":
			return preflight.Finding{State: model.TaskDelivered}, nil
		case "beta":
			return preflight.Finding{}, context.DeadlineExceeded // unreachable source
		default:
			return preflight.Finding{State: model.TaskNotImplemented}, nil
		}
	}
	h.addTodo("run_a", "A", "alpha", "beta", "gamma")
	out := h.submit("run_a", nil)
	if !out.Started {
		t.Fatalf("undelivered targets must start: %+v", out)
	}
	if out.TaskStates["alpha"] != model.TaskDelivered || out.TaskStates["beta"] != model.TaskUnknown {
		t.Fatalf("evidence per target: %+v", out.TaskStates)
	}
	d, _, _ := h.docs.Get(out.ExecutionID)
	if len(d.Repos) != 2 || d.Repo("alpha") != nil {
		t.Fatalf("delivered target must drop out of the document: %+v", d.Repos)
	}
	h.exec.releaseExec(out.ExecutionID)
	h.waitPhase(out.ExecutionID, model.PhaseCompleted)
}

// ── IsDue: the ONE due decision ──────────────────────────────────────────────────────────

func TestIsDue(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	// Todos: due once; any earlier start suppresses the auto-fire.
	todo := runs.Run{Kind: model.KindTodo, DueAt: &past}
	if !IsDue(todo, nil, now) {
		t.Fatal("past-due todo must be due")
	}
	if IsDue(todo, &past, now) {
		t.Fatal("a started todo never auto-fires again")
	}
	if IsDue(runs.Run{Kind: model.KindTodo, DueAt: &future}, nil, now) {
		t.Fatal("future todo is not due")
	}
	if IsDue(runs.Run{Kind: model.KindTodo}, nil, now) {
		t.Fatal("a todo without a due date only ever runs manually")
	}

	// Autos: anchored at the last ACTUAL start; a missed window fires once (catch-up).
	spec := &runs.ScheduleSpec{Kind: runs.Daily, TimeOfDay: "03:00"}
	auto := runs.Run{Kind: model.KindAuto, Active: true, Schedule: spec,
		Authorship: model.Authorship{CreatedAt: now.Add(-48 * time.Hour)}}
	if !IsDue(auto, nil, now) {
		t.Fatal("never-ran active auto with a passed window must catch-up fire")
	}
	startedToday := time.Date(2026, 1, 10, 3, 0, 0, 0, time.UTC)
	if IsDue(auto, &startedToday, now) {
		t.Fatal("already fired today — next window is tomorrow")
	}
	yesterday := startedToday.Add(-24 * time.Hour)
	if !IsDue(auto, &yesterday, now) {
		t.Fatal("last start yesterday, window passed — due")
	}
	inactive := auto
	inactive.Active = false
	if IsDue(inactive, &yesterday, now) {
		t.Fatal("inactive auto never fires")
	}
}

// The tick fires a due run through the one submission path, and a LIVE document suppresses
// any second fire (no double start, no lost fire).
func TestFireDueSuppressedByLiveDoc(t *testing.T) {
	h := newHarness(t, Config{})
	due := time.Now().Add(-time.Minute)
	r := runs.Run{ID: "run_t", Kind: model.KindTodo, Title: "T", Targets: []runs.Target{{Repo: "alpha"}}, DueAt: &due}
	if err := h.runs.Put(r); err != nil {
		t.Fatal(err)
	}
	h.sch.fireDue(context.Background())
	docs, _ := h.docs.Live()
	if len(docs) != 1 {
		t.Fatalf("due todo must fire exactly once, got %d docs", len(docs))
	}
	// Second tick: the live document suppresses the fire.
	h.sch.fireDue(context.Background())
	docs, _ = h.docs.Live()
	if len(docs) != 1 {
		t.Fatalf("a live document must suppress any second fire, got %d docs", len(docs))
	}
	// And after completion the todo (already started once) never auto-fires again.
	h.exec.releaseExec(docs[0].ID)
	h.waitPhase(docs[0].ID, model.PhaseCompleted)
	h.sch.fireDue(context.Background())
	if l, _ := h.docs.Live(); len(l) != 0 {
		t.Fatalf("a started todo must not refire, got %d live docs", len(l))
	}
}
