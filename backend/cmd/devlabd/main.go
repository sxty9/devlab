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
	"syscall"
	"time"

	"devlab/backend/internal/api"
	"devlab/backend/internal/auth"
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

	// ── 3.–4. Boot reconcile (ghost exorcism + restart completion) ───────────────────
	// TODO(B2): execstate.Open + MarkInterruptedAtBoot + restart.json completion, then
	// server.SetExecution(docs, scheduler) — wired here once sched/execstate are filled.
	// The settings store (env values seed the FIRST start only; runtime wins, REQ-013.2)
	// and the scheduler construction (sched.New with the injected preflight gate, the
	// executor ExecFunc, deliver.Maintain and the broker) land here in the same step.

	// ── 5. HTTP (+ SSE via the same mux) + ready socket ──────────────────────────────
	addr := os.Getenv("DEVLAB_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	httpServer := &http.Server{Addr: addr, Handler: server.Handler()}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// TODO(B2): go server.ServeReadySocket(rootCtx) — the Unix-socket readiness endpoint
	// (statepath.ReadySocket; 204 free / 423 busy; dead ⇒ free). Never on the TCP mux.

	// ── 6.–8. Asynchronous after listen: startup reconcile, resumes, ticks ───────────
	server.StartReporter(rootCtx)
	// TODO(B2/B3/B4): scheduler.Start(rootCtx) — resume enqueueing + due ticker + PR
	// maintenance + protection verify + self-check.

	go func() {
		log.Printf("devlabd: listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("devlabd: %v", err)
		}
	}()

	<-rootCtx.Done()
	log.Printf("devlabd: shutdown requested — draining")

	// SIGTERM drain (K-2): gate admissions, persist interrupted, then stop HTTP. The grace
	// budget stays below systemd's TimeoutStopSec (90 s).
	grace := 60 * time.Second
	if v := os.Getenv("DEVLAB_RUNS_DRAIN_GRACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			grace = d
		}
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	// TODO(B2): scheduler.DrainAndPersist(drainCtx) before the HTTP shutdown.
	if err := httpServer.Shutdown(drainCtx); err != nil {
		log.Printf("devlabd: shutdown: %v", err)
	}
	log.Printf("devlabd: stopped")
}
