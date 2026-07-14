package api

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// remshelPtyURL is the loopback WebSocket endpoint of the remshel terminal provider. DevLab
// never runs a PTY itself (Reuse before Build) — it proxies to remshel, which owns the pty,
// the mandatory session recording and the run-as-user sudo wrapper. Override with
// DEVLAB_REMSHEL_URL (e.g. in tests).
func remshelPtyURL() string {
	if v := os.Getenv("DEVLAB_REMSHEL_URL"); v != "" {
		return v
	}
	return "ws://127.0.0.1:8774/api/services/remshel/pty"
}

var ptyUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     sameOriginWS,
}

// pty bridges a browser terminal to a login shell in the caller's repo workspace. DevLab
// resolves the workspace cwd server-side (the client cannot choose it), then proxies the
// WebSocket to remshel over loopback — forwarding the shared session cookie so remshel starts
// the shell AS the same authenticated user, confined by Phase C's per-user isolation. Frames
// pass through untouched (binary I/O + JSON resize) so remshel stays the single owner of the
// pty, its framing and the audit recording.
func (s *Server) pty(w http.ResponseWriter, r *http.Request) {
	// Resolve + authorize the workspace (clones on first use); writes its own error on failure.
	wt, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	// CSWSH guard for the browser upgrade — a WebSocket carries no CSRF header, so same-origin
	// is the defence. Reject before touching the upstream.
	if !sameOriginWS(r) {
		writeErr(w, http.StatusForbidden, "Cross-origin WebSocket refused")
		return
	}

	up, err := url.Parse(remshelPtyURL())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Terminal provider misconfigured")
		return
	}
	q := up.Query()
	q.Set("cwd", wt) // remshel re-validates this is an allowlisted root owned by the user
	up.RawQuery = q.Encode()

	hdr := http.Header{}
	if c := r.Header.Get("Cookie"); c != "" {
		hdr.Set("Cookie", c) // same shared session ⇒ remshel resolves the SAME user
	}
	// remshel enforces its own same-origin check on the upgrade; make the loopback Origin match
	// its host so the internal dial is accepted.
	hdr.Set("Origin", (&url.URL{Scheme: originScheme(up.Scheme), Host: up.Host}).String())

	// Dial remshel BEFORE upgrading the browser so an upstream failure is a clean HTTP error
	// rather than a half-open browser socket.
	upstream, resp, err := websocket.DefaultDialer.Dial(up.String(), hdr)
	if err != nil {
		status := http.StatusBadGateway
		if resp != nil {
			status = resp.StatusCode
		}
		log.Printf("devlabd: terminal dial remshel failed: %v (upstream status %d)", err, status)
		if status == http.StatusForbidden {
			writeErr(w, http.StatusForbidden, "Shell access is disabled for your account")
		} else {
			writeErr(w, http.StatusBadGateway, "Terminal provider unavailable")
		}
		return
	}
	defer upstream.Close()

	client, err := ptyUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already responded (e.g. cross-origin)
	}
	defer client.Close()

	proxyWS(client, upstream)
}

// proxyWS relays every frame verbatim between the browser and remshel until either side
// closes. Each connection is written by exactly one goroutine (its inbound relay), so there
// are no concurrent writes; closing both conns on return unblocks any pending read.
func proxyWS(client, upstream *websocket.Conn) {
	var once sync.Once
	done := make(chan struct{})
	stop := func() { once.Do(func() { close(done) }) }

	relay := func(dst, src *websocket.Conn) {
		defer stop()
		for {
			mt, data, err := src.ReadMessage()
			if err != nil {
				return
			}
			if err := dst.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}
	go relay(upstream, client) // browser → remshel (keystrokes, resize)
	go relay(client, upstream) // remshel → browser (shell output)
	<-done
}

// sameOriginWS is the CSWSH guard: the Origin host must equal the request host. Browsers
// always send Origin on a WebSocket handshake, so a missing Origin is refused.
func sameOriginWS(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func originScheme(wsScheme string) string {
	if wsScheme == "wss" {
		return "https"
	}
	return "http"
}
