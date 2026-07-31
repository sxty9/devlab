// devlabd — the DevLab daemon: one loopback port serves SPA and API from one process.
//
// Boot order (ARCHITEKTUR §6.2; steps 1–5 BEFORE ListenAndServe, 6–8 after, asynchronously):
//
//  1. read the env contract; statepath.CheckWritable (+ *.tmp hygiene); SSO fail-closed
//  2. open the stores (read/validate only; legacy results stay readable, REQ-027.3)
//  3. exorcise ghosts: execstate.MarkInterruptedAtBoot — every "running" document becomes
//     "interrupted"; running stages are terminalized honestly (B2/B3)
//  4. restart completion: restart.json present ⇒ THIS boot is the restart — notice, delete,
//     release the queued starts through normal admission (B2)
//  5. ready socket up FIRST (it is the interlock devlab-migrate takes; a start without it is
//     refused), then HTTP + SSE — the first answers are ghost-free
//  6. startup reconciliation: preflight.SyncStartupTodos (synthetic results, B-5/B3)
//  7. enqueue resumes: every interrupted execution, newest first, via normal admission (B2)
//  8. ticks: due ticker, PR maintenance (deliver.Maintain), protection verify, reporter,
//     self-check
//
// Steps 4, 7 and 8 are what sched.Start performs; step 4 additionally runs BEFORE the listener so
// the very first answer already reflects the completed restart.
//
// SIGTERM ⇒ sched.DrainAndPersist (≤ DEVLAB_RUNS_DRAIN_GRACE) ⇒ shutdown (K-2). A running
// execution is persisted as interrupted with its continuation — a long run never blocks the
// stop.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devlab/backend/internal/api"
	"devlab/backend/internal/auth"
	"devlab/backend/internal/deliver"
	"devlab/backend/internal/execstate"
	"devlab/backend/internal/executor"
	"devlab/backend/internal/live"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
)

func main() {
	// ── 1. Env contract + state root + SSO (fail-closed) ─────────────────────────────
	paths, err := statepath.FromEnv()
	if err != nil {
		log.Fatalf("devlabd: %v", err)
	}
	if err := paths.CheckWritable(); err != nil {
		log.Fatalf("devlabd: %v", err)
	}
	verifier := auth.New()
	if !verifier.HasSecret() && !verifier.DevBypass() {
		// SSO is fail-closed: without a readable secret the daemon must not serve as if
		// authenticated (B-05).
		log.Fatalf("devlabd: no session secret readable and dev-bypass off — refusing to start")
	}

	// ── 2. Stores (the api.Server owns the passive pools) ────────────────────────────
	server := api.New(verifier, paths)

	// The settings pool: the env values seed the FIRST start only — from the first runtime write
	// on, the stored values win (REQ-013.2).
	settings := runs.NewSettingsStore(paths, runs.Settings{
		MaxConcurrency:    envInt("DEVLAB_RUNS_MAX_CONCURRENCY", sched.DefaultCapacity),
		DefaultTimeBudget: envDuration("DEVLAB_RUNS_TIME_BUDGET", 3*time.Hour),
		AutomergeWindow:   envDuration("DEVLAB_RUNS_AUTOMERGE_WINDOW", 720*time.Hour),
	})
	server.SetSettings(settings)

	// The ONE live stream's broker (S12): every publisher is a caller after its own write.
	broker := live.NewBroker()
	server.SetBroker(broker)

	// The execution documents ARE the concurrency state (S7) — opened read/validate only.
	docs, err := execstate.Open(paths)
	if err != nil {
		log.Fatalf("devlabd: execution documents: %v", err)
	}

	// ── 3. Exorcise ghosts BEFORE the first answer (REQ-039.1, K-4) ──────────────────
	ghosts, err := docs.MarkInterruptedAtBoot(time.Now().UTC())
	if err != nil {
		log.Fatalf("devlabd: boot reconcile (ghost exorcism): %v", err)
	}
	if ghosts > 0 {
		log.Printf("devlabd: boot reconcile: %d execution(s) left running by a dead process marked interrupted", ghosts)
	}

	// The scheduler over the documents. The executor arrives as an injected function (B-3), so the
	// two never import each other; `scheduler` is captured by that closure and therefore declared
	// before it is built.
	var scheduler *sched.Scheduler
	scheduler = sched.New(
		schedConfig(),
		docs, runsStore(server), resultsStore(server), settings,
		server.PreflightGate(),
		func(ctx context.Context, doc execstate.Doc, run runs.Run) error {
			return execute(ctx, server, scheduler, doc, run)
		},
		server.MaintainDeliveries,
		broker,
	)
	if notice := server.NoticeFunc(); notice != nil {
		scheduler.SetNoticeFunc(notice)
	}
	server.SetExecution(docs, scheduler)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// ── 4. Restart completion — the first answer already reflects it ─────────────────
	if released, err := scheduler.CompleteBootRestart(rootCtx); err != nil {
		log.Printf("devlabd: boot reconcile (restart completion): %v", err)
	} else if released > 0 {
		log.Printf("devlabd: boot reconcile: %d start(s) queued behind the restart released", released)
	}

	// ── 5. HTTP (+ SSE via the same mux) + ready socket ──────────────────────────────
	addr := os.Getenv("DEVLAB_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	httpServer := &http.Server{Addr: addr, Handler: server.Handler()}

	// The Unix-socket readiness endpoint (statepath.ReadySocket; 204 free / 423 busy; a dead
	// daemon reads as free on the wrapper's side). NEVER on the TCP mux (A2-7).
	//
	// This socket is NOT a status line: it is the interlock devlab-migrate takes. The migration asks
	// it whether an execution is running and refuses (exit 10) while one is — and a daemon whose
	// socket never came up reads as "dead, nothing running", so the migration would proceed against a
	// LIVE daemon and two writers would work one state tree. A daemon must not serve what it cannot
	// protect, so the gate is a boot precondition, exactly like the writable state root and the
	// readable session secret above: it goes up BEFORE the first answer, and its absence refuses the
	// start by name instead of leaving a log line nobody reads.
	gateDown := make(chan error, 1)
	go func() {
		// nil = the gate served until shutdown; that is not a failure.
		if err := server.ServeReadySocket(rootCtx); err != nil {
			gateDown <- err
		}
	}()
	if err := awaitGate(rootCtx, paths.ReadySocket(), gateDown, envDuration("DEVLAB_READY_GATE_TIMEOUT", 10*time.Second)); err != nil {
		log.Fatalf("devlabd: the restart gate (%s) did not come up — refusing to serve without the interlock that keeps a migration off a live state tree: %v",
			paths.ReadySocket(), err)
	}

	go func() {
		log.Printf("devlabd: listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("devlabd: %v", err)
		}
	}()

	// ── 6.–8. Asynchronous after listen: startup reconcile, resumes, ticks ───────────
	// The reconciliation talks to git and GitHub, so it runs off the boot path: a slow or
	// unreachable source delays a check-off, never the service.
	go func() {
		if n, err := server.SyncStartupTodos(rootCtx); err != nil {
			log.Printf("devlabd: startup reconciliation deferred: %v", err)
		} else if n > 0 {
			log.Printf("devlabd: startup reconciliation checked off %d todo(s) already in a default branch", n)
		}
	}()
	// Resume enqueueing (7) plus the due ticker and the PR maintenance (8). The maintenance writes
	// into FOREIGN repositories and is held until the operator arms it, so the boot says which of the
	// two states it is starting in — the cutover reads this line before any surface is open.
	if deliver.MaintainArmed() {
		log.Printf("devlabd: pull-request maintenance ARMED (%s) — it merges, prunes and stamps", deliver.EnvMaintainEnforce)
	} else {
		log.Printf("devlabd: pull-request maintenance HELD — it reports and writes nothing; arm it with %s=1", deliver.EnvMaintainEnforce)
	}
	if err := scheduler.Start(rootCtx); err != nil {
		log.Printf("devlabd: scheduler start: %v", err)
	}
	// The reporter side of step 8: the daily report and the delivery self-check.
	server.StartReporter(rootCtx)
	// Branch protection is verified on the same cadence as the PR maintenance, contained: an
	// unreachable GitHub costs one pass, never the daemon.
	go verifyProtectionLoop(rootCtx, server)

	// The gate can also fall LATER (its listener dies while the daemon runs). The interlock is gone
	// from that moment on, so the daemon stops — draining first, so a running execution is still
	// persisted as interrupted (K-2) — and exits non-zero, which is what the unit restarts on.
	lost := false
	select {
	case <-rootCtx.Done():
		log.Printf("devlabd: shutdown requested — draining")
	case err := <-gateDown:
		lost = true
		log.Printf("devlabd: restart gate lost (%v) — stopping: without it a migration would run against a live state tree", err)
	}

	// SIGTERM drain (K-2): gate admissions, persist interrupted, then stop HTTP. The grace
	// budget stays below systemd's TimeoutStopSec (90 s).
	grace := envDuration("DEVLAB_RUNS_DRAIN_GRACE", 60*time.Second)
	drainCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := scheduler.DrainAndPersist(drainCtx); err != nil {
		log.Printf("devlabd: drain: %v", err)
	}
	if err := httpServer.Shutdown(drainCtx); err != nil {
		log.Printf("devlabd: shutdown: %v", err)
	}
	log.Printf("devlabd: stopped")
	if lost {
		os.Exit(1)
	}
}

// awaitGate waits until the readiness gate ANSWERS — a socket that accepts a connection is bound and
// listening — or until the attempt to bring it up failed, or until the budget is over. All three
// answers are named; a silent "the gate is probably up" is not one of them. The probe is a plain dial
// (the gate's own route belongs to its client, devlab-migrate) and closes immediately.
func awaitGate(ctx context.Context, sock string, failed <-chan error, budget time.Duration) error {
	if budget <= 0 {
		budget = 10 * time.Second
	}
	deadline := time.Now().Add(budget)
	for {
		select {
		case err := <-failed:
			return err
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no answer on the readiness socket within %s", budget)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// execute is the ExecFunc the scheduler drives — the ONE place the chain motor is invoked. It
// resolves the effective tuning (the one resolution point, REQ-010), composes the sink out of the
// document-progress half (sched) and the result recorder (api), and runs executor.Execute.
func execute(ctx context.Context, server *api.Server, scheduler *sched.Scheduler, doc execstate.Doc, run runs.Run) error {
	set, err := server.Settings()
	if err != nil {
		return err
	}
	deps := server.ChainDeps(api.SchedulerHooks(scheduler)).WithRun(run)
	defer deps.Close()

	rec := server.NewResultRecorder(doc, run)
	err = executor.Execute(ctx, deps, executor.Request{
		Run:    run,
		Doc:    doc,
		Budget: runs.EffectiveTuning(run, set),
	}, api.ExecutionSink(scheduler.DocSink(doc.ID), rec))
	rec.Finish(err)
	return err
}

// verifyProtectionLoop re-checks branch protection on the maintenance cadence (REQ-033.7),
// contained: a failing pass is logged, never fatal.
//
// FOREIGN EFFECT, HELD BY DEFAULT: one pass reaches EVERY repository of the configured organisation,
// and restoring a deviation WRITES to that repository's default branch — the one thing in this daemon
// that changes something outside DevLab. Two bounds make a boot harmless, and the cutover runbook
// names both:
//
//   - the first pass does NOT run at boot. It waits DEVLAB_RUNS_PROTECTION_START_DELAY (default 15m),
//     so a cutover, a restart loop or a mis-set organisation cannot reach GitHub before an operator
//     has seen the daemon come up; stopping the service inside that window costs nothing at all.
//   - writing is off until the operator ARMS it (DEVLAB_RUNS_PROTECTION_ENFORCE, read in
//     api.VerifyRepoProtection). Unarmed, a pass only READS and reports what deviates.
func verifyProtectionLoop(ctx context.Context, server *api.Server) {
	protectionLoop(ctx,
		envDuration("DEVLAB_RUNS_PROTECTION_START_DELAY", 15*time.Minute),
		envDuration("DEVLAB_RUNS_PROTECTION_INTERVAL", 6*time.Hour),
		func(c context.Context) error { _, err := server.VerifyRepoProtection(c); return err })
}

// protectionLoop is the loop itself, with the pass injected — the shape the delay and the cadence are
// provable in. It sleeps FIRST, so a cancelled context during the initial delay means no pass ever ran.
func protectionLoop(ctx context.Context, delay, interval time.Duration, pass func(context.Context) error) {
	if delay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := pass(ctx); err != nil {
			log.Printf("devlabd: protection verify: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// schedConfig reads the scheduler's runtime knobs. Env values are DEFAULTS of the process; the
// capacity itself lives in the settings pool, where a runtime change wins (REQ-013.2).
func schedConfig() sched.Config {
	return sched.Config{
		Tick:            envDuration("DEVLAB_RUNS_TICK", 30*time.Second),
		ResumeWindow:    envDuration("DEVLAB_RUNS_RESUME_WINDOW", 240*time.Hour),
		DrainGrace:      envDuration("DEVLAB_RUNS_DRAIN_GRACE", 60*time.Second),
		MaxDuration:     envDuration("DEVLAB_RUNS_MAX_DURATION", 0),
		LimitBackoff:    envDuration("DEVLAB_RUNS_LIMIT_BACKOFF", 30*time.Minute),
		LimitMaxResumes: envInt("DEVLAB_RUNS_LIMIT_MAX_RESUMES", 8),
		StartCap:        envInt("DEVLAB_RUNS_START_CAP", 0),
	}
}

// runsStore / resultsStore hand the scheduler the pools the server already owns — one handle per
// file, so there is never a second mutex over one truth.
func runsStore(s *api.Server) *runs.Store          { return s.RunStore() }
func resultsStore(s *api.Server) *runs.ResultStore { return s.ResultStore() }

// envDuration reads a duration from the environment, falling back to def for an unset or
// unparseable value. A negative value is refused the same way — operation tunes it, a typo never
// silently disables a pass.
func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Printf("devlabd: %s=%q is not a usable duration — using %s", key, v, def)
		return def
	}
	return d
}

// envInt reads a non-negative integer from the environment, falling back to def.
func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("devlabd: %s=%q is not a usable number — using %d", key, v, def)
		return def
	}
	return n
}
