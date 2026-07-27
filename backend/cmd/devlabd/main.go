// Command devlabd is the DevLab backend daemon. It serves the JSON API (and, in later phases,
// terminal/Claude WebSockets) under /api, gated by package auth. It runs unprivileged behind
// the sxgate Caddy proxy (static_proxy mode serves dist/ and proxies /api/* here), or directly
// in local dev (vite proxies /api). One process, one port, one auth gate.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"devlab/backend/internal/api"
	"devlab/backend/internal/auth"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8780", "address to listen on")
	flag.Parse()

	v := auth.New()
	if v.DevBypass() {
		log.Print("devlabd: dev-bypass mode (full access, no auth)")
	} else if !v.HasSecret() {
		// Refuse to serve without a real secret: an empty HMAC key would validate forged
		// tokens (fail-open). Set HOLISTIC_SECRET_FILE or run with DEVLAB_DEV_BYPASS_AUTH=1.
		log.Fatal("devlabd: no JWT secret (set HOLISTIC_SECRET_FILE) — refusing to start in SSO mode")
	} else {
		log.Print("devlabd: holistic SSO mode (hp_devlab_access required)")
	}

	server := api.New(v)
	srv := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Mercury's run scheduler runs in the background for the process lifetime; it stays dormant
	// unless DEVLAB_RUNS_MODE + DEVLAB_RUNS_USER are configured. Cancelled on shutdown so an
	// in-flight run drains before the process exits. The auto-rollout worker shares that lifetime:
	// axiom/rule edits debounce into one CLAUDE.md rollout, pushed with the same runner token.
	schedCtx, schedCancel := context.WithCancel(context.Background())
	server.StartScheduler(schedCtx)
	server.StartRolloutWorker(schedCtx)
	// The daily run-report emailer shares the scheduler's lifetime and gating: at the close of a day
	// on which runs executed, it mails the owner a summary of what they did.
	server.StartReporter(schedCtx)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		schedCancel()
		log.Fatalf("devlabd: listen %s: %v", *listen, err)
	}
	go func() {
		log.Printf("devlabd listening on %s", *listen)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("devlabd: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	schedCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Print("devlabd stopped")
}
