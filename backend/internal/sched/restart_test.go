package sched

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"devlab/backend/internal/execstate"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
)

// ── REQ-018: restart ⊕ start exclude each other atomically ───────────────────────────────

// The marker rides under exactly ONE name: statepath's restart.json (B-13, REQ-039.3).
func TestRestartMarkerHasExactlyOneName(t *testing.T) {
	h := newHarness(t, Config{})
	st, err := h.sch.RequestRestart(model.Actor{User: "chain"})
	if err != nil || !st.Pending {
		t.Fatalf("request: %v %+v", err, st)
	}
	want := h.paths.RestartMarker()
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("marker must live at statepath.RestartMarker(): %v", err)
	}
	entries, _ := os.ReadDir(h.paths.Mercury())
	for _, e := range entries {
		if e.Name() != "restart.json" && !e.IsDir() {
			t.Fatalf("no second marker-like file may appear, found %s", e.Name())
		}
	}
}

// The first requester sets the marker; a second request never resets it; starts during the
// pending restart are queued ON the marker and the caller learns about the restart.
func TestRestartFirstSetsAndStartsQueue(t *testing.T) {
	h := newHarness(t, Config{})
	st1, err := h.sch.RequestRestart(model.Actor{User: "chain"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	st2, err := h.sch.RequestRestart(model.Actor{User: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	if !st2.RequestedAt.Equal(st1.RequestedAt) || st2.RequestedBy.User != "chain" {
		t.Fatalf("the FIRST requester holds the marker: %+v vs %+v", st1, st2)
	}
	if st1.Deadline.IsZero() || !st1.Deadline.After(st1.RequestedAt) {
		t.Fatalf("the drain-out must be time-bounded on the record: %+v", st1)
	}

	h.addTodo("run_a", "A", "alpha")
	out := h.submit("run_a", nil)
	if out.Started || !out.Queued || !out.RestartPending || out.NotStarted == "" {
		t.Fatalf("a start during a pending restart queues and says so: %+v", out)
	}
	st := h.sch.RestartState()
	if len(st.QueuedStarts) != 1 || st.QueuedStarts[0].RunID != "run_a" || st.QueuedStarts[0].By.User != "ada" {
		t.Fatalf("the queued start must ride the marker with its requester: %+v", st.QueuedStarts)
	}
	if !h.pub.has(live.TopicRestart) {
		t.Fatal("the restart tick must be published")
	}
}

// Race: a concurrent restart request and start attempt never kill a run — whichever wins the
// one mutex goes first, the loser sees the other's truth (REQ-018.1).
func TestRestartStartRaceIsAtomic(t *testing.T) {
	for i := 0; i < 20; i++ {
		h := newHarness(t, Config{})
		h.addTodo("run_a", "A", "alpha")
		var out model.StartOutcome
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			out = h.submit("run_a", nil)
		}()
		go func() {
			defer wg.Done()
			if _, err := h.sch.RequestRestart(model.Actor{User: "chain"}); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()

		st := h.sch.RestartState()
		if !st.Pending {
			t.Fatal("the restart request must always land")
		}
		switch {
		case out.Started:
			// The start won the mutex: its execution LIVES and drains out; it is never killed.
			d, ok, err := h.docs.Get(out.ExecutionID)
			if err != nil || !ok || d.Phase != model.PhaseRunning {
				t.Fatalf("round %d: the admitted run must keep running through the drain: %+v", i, d)
			}
			if h.sch.RestartReady() {
				t.Fatalf("round %d: not free while an execution runs", i)
			}
			h.exec.releaseExec(out.ExecutionID)
			h.waitPhase(out.ExecutionID, model.PhaseCompleted)
		case out.Queued && out.RestartPending:
			// The restart won: the start is recorded, no document runs.
			if n := h.runningCount(); n != 0 {
				t.Fatalf("round %d: queued start must not run, %d running", i, n)
			}
			if len(st.QueuedStarts) != 1 {
				t.Fatalf("round %d: queued start must ride the marker: %+v", i, st.QueuedStarts)
			}
		default:
			t.Fatalf("round %d: outcome neither started nor queued: %+v", i, out)
		}
	}
}

// The marker counts the running executions: the first request sets it, the LAST finishing
// execution flips the free signal (REQ-018.4 — "erster setzt, letzter löst").
func TestRestartReadyFlipsWithLastRunningExecution(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "beta")
	outA := h.submit("run_a", nil)
	outB := h.submit("run_b", nil)

	if _, err := h.sch.RequestRestart(model.Actor{User: "chain"}); err != nil {
		t.Fatal(err)
	}
	if h.sch.RestartReady() {
		t.Fatal("two running executions: not free")
	}
	h.exec.releaseExec(outA.ExecutionID)
	h.waitPhase(outA.ExecutionID, model.PhaseCompleted)
	if h.sch.RestartReady() {
		t.Fatal("one execution still running: not free")
	}
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
	h.waitIdle()
	if !h.sch.RestartReady() {
		t.Fatal("the LAST finisher must flip the free signal")
	}
	// Queued and paused executions do NOT hold the restart: only running counts.
	h.addTodo("run_c", "C", "gamma")
	_ = h.submit("run_c", nil) // restart pending → queued on the marker
	if !h.sch.RestartReady() {
		t.Fatal("a queued start must not hold the restart")
	}
}

// Boot completion (reconcile step 4): the marker means THIS boot is the restart — notice to
// the requester, marker deleted, queued starts released through normal admission.
func TestCompleteBootRestartReleasesQueuedStarts(t *testing.T) {
	h := newHarness(t, Config{})
	if _, err := h.sch.RequestRestart(model.Actor{User: "chain"}); err != nil {
		t.Fatal(err)
	}
	h.addTodo("run_a", "A", "alpha")
	_ = h.submit("run_a", nil)

	var notices []string
	h.sch.SetNoticeFunc(func(kind, text string) { notices = append(notices, kind) })

	n, err := h.sch.CompleteBootRestart(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("released %d queued starts (err %v), want 1", n, err)
	}
	if h.sch.RestartState().Pending {
		t.Fatal("the marker must be gone after boot completion")
	}
	live, _ := h.docs.Live()
	if len(live) != 1 || live[0].RunID != "run_a" {
		t.Fatalf("the queued start must fire after the restart: %+v", live)
	}
	found := false
	for _, k := range notices {
		if k == "restart-completed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the requester must learn the restart completed: %v", notices)
	}
	// Idempotent.
	if n, err := h.sch.CompleteBootRestart(context.Background()); err != nil || n != 0 {
		t.Fatalf("second completion must be a no-op: %d %v", n, err)
	}
	h.exec.releaseExec(live[0].ID)
	h.waitPhase(live[0].ID, model.PhaseCompleted)
}

// ── REQ-018 · a queued start is a promise: it survives a restart, whatever it waits on ───

// hasNotice reports whether any collected notice line starts with kind.
func hasNotice(lines []string, kind string) bool {
	for _, l := range lines {
		if len(l) >= len(kind) && l[:len(kind)] == kind {
			return true
		}
	}
	return false
}

// A start that waits because its target repository is BUSY is held in a persisted queued
// document — so it survives a restart of the daemon and fires afterwards, without a human having
// to notice it went missing (the measured loss of 31.07./01.08.2026). This is the NACHWEIS the
// task demands: enqueue (repo busy), restart, prove it runs.
func TestQueuedStartRepoBusySurvivesRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.cap.Store(2)
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "alpha") // SAME repo — exclusivity makes B wait on A

	outA := h.submit("run_a", nil)
	if !outA.Started {
		t.Fatalf("A must start (its repo is free): %+v", outA)
	}
	outB := h.submit("run_b", &Placement{Kind: PlacementQueue})
	if !outB.Queued || outB.ExecutionID == "" {
		t.Fatalf("B must be held as a queued document while alpha is busy: %+v", outB)
	}
	// The waiting start is durable BEFORE any restart: a document on disk, not a process fact.
	if d, ok, _ := h.docs.Get(outB.ExecutionID); !ok || d.Phase != model.PhaseQueued {
		t.Fatalf("the queued start must be a persisted document: ok=%v %+v", ok, d)
	}

	// Restart the daemon over the same state root.
	h.reboot()

	// The queued start is STILL there, and still visible while it waits.
	d, ok, _ := h.docs.Get(outB.ExecutionID)
	if !ok || !d.Live() {
		t.Fatalf("the queued start must survive the restart: ok=%v %+v", ok, d)
	}
	ov, err := h.sch.SlotOverview()
	if err != nil {
		t.Fatal(err)
	}
	sawB := false
	for _, v := range ov.Active {
		if v.RunID == "run_b" {
			sawB = true
		}
	}
	if !sawB {
		t.Fatalf("the surviving start must be visible in the active view: %+v", ov.Active)
	}

	// And it actually FIRES after the restart (alpha is free now — A drained out at the restart).
	h.sch.pass(context.Background(), false)
	fired := h.waitPhase(outB.ExecutionID, model.PhaseRunning)
	if fired.ID != outB.ExecutionID {
		t.Fatalf("the SAME queued start must run, under its own id: %+v", fired)
	}
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
}

// The SLOTS-FULL wait reason takes the very same durable path: a queued document that survives
// the restart and fires when a slot opens.
func TestQueuedStartSlotsFullSurvivesRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.cap.Store(1)
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "beta") // different repo — only the single slot makes B wait

	outA := h.submit("run_a", nil)
	outB := h.submit("run_b", &Placement{Kind: PlacementQueue})
	if !outA.Started || !outB.Queued {
		t.Fatalf("A runs, B queues on the full slot: %+v %+v", outA, outB)
	}

	h.reboot()

	if d, ok, _ := h.docs.Get(outB.ExecutionID); !ok || !d.Live() {
		t.Fatalf("the slots-full start must survive the restart: ok=%v %+v", ok, d)
	}
	h.sch.pass(context.Background(), false)
	h.waitPhase(outB.ExecutionID, model.PhaseRunning)
	h.exec.releaseExec(outB.ExecutionID)
	h.waitPhase(outB.ExecutionID, model.PhaseCompleted)
}

// Losing a queued start is allowed — silence is not. A start whose run definition vanished while
// it waited is retired VISIBLY at the next promotion: the document turns discarded with its
// reason, and an operator notice names which start and why (the restart-queued-start-failed path
// must not be the only case anyone ever learns anything).
func TestQueuedStartVanishedRunIsNamed(t *testing.T) {
	h := newHarness(t, Config{})
	h.cap.Store(2)
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "alpha")

	_ = h.submit("run_a", nil)
	outB := h.submit("run_b", &Placement{Kind: PlacementQueue})
	if !outB.Queued {
		t.Fatalf("B must queue behind A on alpha: %+v", outB)
	}
	// The run definition disappears while its start waits.
	if err := h.runs.Delete("run_b"); err != nil {
		t.Fatal(err)
	}

	notices := h.reboot()
	h.sch.pass(context.Background(), false)

	d, ok, _ := h.docs.Get(outB.ExecutionID)
	if !ok || d.Phase != model.PhaseDiscarded {
		t.Fatalf("a queued start whose run vanished must be discarded, is %s", d.Phase)
	}
	if d.Reason == "" {
		t.Fatal("the discard must carry its reason")
	}
	if !hasNotice(*notices, "execution-abandoned") {
		t.Fatalf("the loss must be NAMED in a notice, got: %v", *notices)
	}
}

// A queued start older than the resume window is reliably abandoned — also visibly, also named.
func TestQueuedStartBeyondResumeWindowIsNamed(t *testing.T) {
	h := newHarness(t, Config{ResumeWindow: time.Hour})
	h.cap.Store(2)
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "alpha")

	_ = h.submit("run_a", nil)
	outB := h.submit("run_b", &Placement{Kind: PlacementQueue})
	if !outB.Queued {
		t.Fatalf("B must queue behind A on alpha: %+v", outB)
	}

	notices := h.reboot()
	// The clock moves past the resume window while B still waits.
	h.sch.now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }
	h.sch.pass(context.Background(), false)

	d, ok, _ := h.docs.Get(outB.ExecutionID)
	if !ok || d.Phase != model.PhaseDiscarded {
		t.Fatalf("an ancient queued start must be abandoned, is %s", d.Phase)
	}
	if !hasNotice(*notices, "execution-abandoned") {
		t.Fatalf("the abandonment must be NAMED in a notice, got: %v", *notices)
	}
}

// ── K-2 drain (B-01 share): SIGTERM persists, never blocks the stop budget ───────────────

// A cooperative execution drains: SIGTERM → interrupted with continuation, well within grace.
func TestDrainPersistsRunningAsInterrupted(t *testing.T) {
	h := newHarness(t, Config{DrainGrace: 2 * time.Second})
	h.addTodo("run_a", "A", "alpha")
	out := h.submit("run_a", nil)
	h.sch.DocSink(out.ExecutionID).Continuation(model.ContinuationView{Repo: "alpha", Stage: model.StageImplement})

	start := time.Now()
	if err := h.sch.DrainAndPersist(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("drain took %s — must stay within the grace budget", elapsed)
	}
	d := h.waitPhase(out.ExecutionID, model.PhaseInterrupted)
	if d.Continuation == nil || d.Continuation.Repo != "alpha" {
		t.Fatalf("the continuation must be persisted for the resume: %+v", d.Continuation)
	}
	// Admission is gated during the drain: a submit queues for the next boot.
	h.addTodo("run_b", "B", "beta")
	outB := h.submit("run_b", nil)
	if outB.Started {
		t.Fatal("no start may slip in during the drain")
	}
}

// A STUCK execution (ignores its cancellation) cannot hold the stop: the document is forced
// to interrupted and the drain returns within its grace (a long run never blocks the stop).
func TestDrainForcesStuckExecutionWithinGrace(t *testing.T) {
	h := newHarness(t, Config{DrainGrace: 50 * time.Millisecond})
	h.exec.ignoreCtx = true
	h.addTodo("run_a", "A", "alpha")
	out := h.submit("run_a", nil)

	start := time.Now()
	if err := h.sch.DrainAndPersist(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("drain must respect its budget even with a stuck agent, took %s", elapsed)
	}
	d, _, _ := h.docs.Get(out.ExecutionID)
	if d.Phase != model.PhaseInterrupted {
		t.Fatalf("the stuck execution must be persisted interrupted, is %s", d.Phase)
	}
	h.exec.releaseExec(out.ExecutionID) // let the goroutine die
}

// ── REQ-039.1 + REQ-018.6: kill ⇒ interrupted at boot ⇒ auto-resume, SAME id ─────────────

func TestKillThenBootInterruptsAndAutoResumes(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha", "beta")
	// Simulate the predecessor process: a document left "running" mid-work by a kill.
	doc, err := h.docs.Create("run_a", model.KindTodo, []string{"alpha", "beta"}, false, model.Actor{User: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhaseRunning, "", model.Actor{}, time.Now())
		d.Repos[0].State = execstate.RepoDone
		d.Repos[1].State = execstate.RepoActive
		d.Continuation = &model.ContinuationView{Repo: "beta", Stage: model.StagePublish}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Boot: ghosts are exorcised BEFORE anything serves — never an eternal "running".
	n, err := h.docs.MarkInterruptedAtBoot(time.Now())
	if err != nil || n != 1 {
		t.Fatalf("boot reconcile: %v n=%d", err, n)
	}
	got, _, _ := h.docs.Get(doc.ID)
	if got.Phase != model.PhaseInterrupted {
		t.Fatalf("killed execution must read interrupted, is %s", got.Phase)
	}

	// Boot step 7: the interrupted execution re-queues and continues — SAME id, done stays done.
	if err := h.sch.enqueueInterruptedAtBoot(); err != nil {
		t.Fatal(err)
	}
	h.sch.pass(context.Background(), false)
	h.waitPhase(doc.ID, model.PhaseRunning)
	// The executor must receive the SAME execution id (REQ-019.1) — waited for, because the
	// document's phase flips before the goroutine that hands it over even starts.
	seen := h.waitStarted(doc.ID, 1) // the predecessor process was simulated: this is the first handover
	if seen.Continuation == nil || seen.Continuation.Repo != "beta" || seen.Continuation.Stage != model.StagePublish {
		t.Fatalf("resume must continue at the same spot: %+v", seen.Continuation)
	}
	if seen.Repo("alpha").State != execstate.RepoDone {
		t.Fatal("finished repositories stay finished across the restart")
	}
	h.exec.releaseExec(doc.ID)
	h.waitPhase(doc.ID, model.PhaseCompleted)
}

// ── REQ-019: resume same id / age limit / explicit fresh marks discarded ─────────────────

// A renewed trigger CONTINUES the unfinished execution under its own id, and the caller
// learns it resumed.
func TestSubmitResumesInterruptedUnderSameID(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	doc, _ := h.docs.Create("run_a", model.KindTodo, []string{"alpha"}, false, model.Actor{User: "ada"})
	if _, err := h.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhaseInterrupted, "process death", model.Actor{Autonomous: true}, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	out := h.submit("run_a", nil)
	if !out.Resumed || out.ExecutionID != doc.ID {
		t.Fatalf("must resume the SAME execution id: %+v", out)
	}
	if !out.Started {
		t.Fatalf("slot free — the resume starts at once: %+v", out)
	}
	h.exec.releaseExec(doc.ID)
	h.waitPhase(doc.ID, model.PhaseCompleted)
}

// Executions beyond the resume window count as reliably abandoned: the old document is
// discarded VISIBLY and a fresh one starts.
func TestResumeWindowAbandonsOldExecutions(t *testing.T) {
	h := newHarness(t, Config{ResumeWindow: time.Hour})
	h.addTodo("run_a", "A", "alpha")
	doc, _ := h.docs.Create("run_a", model.KindTodo, []string{"alpha"}, false, model.Actor{User: "ada"})
	if _, err := h.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhaseInterrupted, "process death", model.Actor{Autonomous: true}, time.Now())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Age the document past the window (the clock is a seam).
	h.sch.now = func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }

	out := h.submit("run_a", nil)
	if out.Resumed || out.ExecutionID == doc.ID {
		t.Fatalf("an abandoned execution must not resume: %+v", out)
	}
	if !out.Started {
		t.Fatalf("a fresh start takes its place: %+v", out)
	}
	old, _, _ := h.docs.Get(doc.ID)
	if old.Phase != model.PhaseDiscarded || old.Reason == "" {
		t.Fatalf("the abandoned document must be visibly discarded with a reason: %s %q", old.Phase, old.Reason)
	}
	h.exec.releaseExec(out.ExecutionID)
	h.waitPhase(out.ExecutionID, model.PhaseCompleted)
}

// An EXPLICIT fresh start marks the old execution discarded — by the requesting person.
func TestExplicitFreshMarksOldDiscarded(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	doc, _ := h.docs.Create("run_a", model.KindTodo, []string{"alpha"}, false, model.Actor{User: "ada"})
	if _, err := h.docs.Update(doc.ID, func(d *execstate.Doc) error {
		d.SetPhase(model.PhasePaused, "deferred", model.Actor{User: "ada"}, time.Now())
		d.Pause = &model.PauseView{Reason: model.PauseDeferredByUser}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := h.sch.Submit(context.Background(), StartRequest{RunID: "run_a", By: model.Actor{User: "ada"}, Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Started || !out.Fresh || out.ExecutionID == doc.ID {
		t.Fatalf("fresh start must mint a NEW execution: %+v", out)
	}
	old, _, _ := h.docs.Get(doc.ID)
	if old.Phase != model.PhaseDiscarded {
		t.Fatalf("the old execution must be marked discarded, is %s", old.Phase)
	}
	lastT := old.Transitions[len(old.Transitions)-1]
	if lastT.By.User != "ada" {
		t.Fatalf("the discard carries who requested it: %+v", lastT)
	}
	h.exec.releaseExec(out.ExecutionID)
	h.waitPhase(out.ExecutionID, model.PhaseCompleted)
}

// A submit against an already queued/running execution answers honestly and never doubles.
func TestSubmitOnActiveRunNeverDoubles(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	out := h.submit("run_a", nil)
	again := h.submit("run_a", nil)
	if again.Started || again.Queued {
		t.Fatalf("no second execution for a live run: %+v", again)
	}
	if again.ExecutionID != out.ExecutionID || again.NotStarted == "" {
		t.Fatalf("the answer names the live execution: %+v", again)
	}
	docs, _ := h.docs.List()
	if len(docs) != 1 {
		t.Fatalf("exactly one document, got %d", len(docs))
	}
	h.exec.releaseExec(out.ExecutionID)
	h.waitPhase(out.ExecutionID, model.PhaseCompleted)
}

// Boot abandonment: at boot, interrupted docs beyond the window are discarded with a notice
// (REQ-019.3), younger ones re-queue.
func TestBootAbandonsOnlyOldInterrupted(t *testing.T) {
	h := newHarness(t, Config{ResumeWindow: time.Hour})
	h.addTodo("run_old", "Old", "alpha")
	h.addTodo("run_new", "New", "beta")
	oldDoc, _ := h.docs.Create("run_old", model.KindTodo, []string{"alpha"}, false, model.Actor{})
	newDoc, _ := h.docs.Create("run_new", model.KindTodo, []string{"beta"}, false, model.Actor{})
	for _, id := range []string{oldDoc.ID, newDoc.ID} {
		if _, err := h.docs.Update(id, func(d *execstate.Doc) error {
			d.SetPhase(model.PhaseInterrupted, "process death", model.Actor{Autonomous: true}, time.Now())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Age ONLY the old one: rewrite its UpdatedAt far into the past via the raw file (the
	// store stamps UpdatedAt itself — a direct write mimics a genuinely old document).
	agePast := time.Now().UTC().Add(-3 * time.Hour)
	raw, _ := os.ReadFile(h.paths.Execution(oldDoc.ID) + "/state.json")
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["updatedAt"] = agePast.Format(time.RFC3339Nano)
	b, _ := json.Marshal(m)
	if err := os.WriteFile(h.paths.Execution(oldDoc.ID)+"/state.json", b, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := h.sch.enqueueInterruptedAtBoot(); err != nil {
		t.Fatal(err)
	}
	gotOld, _, _ := h.docs.Get(oldDoc.ID)
	gotNew, _, _ := h.docs.Get(newDoc.ID)
	if gotOld.Phase != model.PhaseDiscarded {
		t.Fatalf("old interrupted must be abandoned, is %s", gotOld.Phase)
	}
	if gotNew.Phase != model.PhaseQueued {
		t.Fatalf("young interrupted must re-queue, is %s", gotNew.Phase)
	}
}
