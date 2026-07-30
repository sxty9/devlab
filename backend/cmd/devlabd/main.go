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
//  5. HTTP + SSE + ready socket up — the first answers are ghost-free
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
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devlab/backend/internal/api"
	"devlab/backend/internal/auth"
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
	go func() {
		if err := server.ServeReadySocket(rootCtx); err != nil {
			log.Printf("devlabd: ready socket: %v", err)
		}
	}()

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
	// Resume enqueueing (7) plus the due ticker and the PR maintenance (8).
	if err := scheduler.Start(rootCtx); err != nil {
		log.Printf("devlabd: scheduler start: %v", err)
	}
	// The reporter side of step 8: the daily report and the delivery self-check.
	server.StartReporter(rootCtx)
	// Branch protection is verified on the same cadence as the PR maintenance, contained: an
	// unreachable GitHub costs one pass, never the daemon.
	go verifyProtectionLoop(rootCtx, server)

	<-rootCtx.Done()
	log.Printf("devlabd: shutdown requested — draining")

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
func verifyProtectionLoop(ctx context.Context, server *api.Server) {
	interval := envDuration("DEVLAB_RUNS_PROTECTION_INTERVAL", 6*time.Hour)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := server.VerifyRepoProtection(ctx); err != nil {
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
