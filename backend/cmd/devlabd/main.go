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
	if v.PreviewGated() {
		log.Print("devlabd: preview mode (shared-password read-only)")
	} else {
		log.Print("devlabd: full-access mode (dev-bypass or holistic JWT)")
	}

	srv := &http.Server{
		Handler:           api.New(v).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Print("devlabd stopped")
}
