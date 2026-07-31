package api

// The terminal is remshel's (Reuse before Build): DevLab only proxies. What is tested here is
// what DevLab itself decides — the workspace cwd it sets server-side, the session key it forwards
// so a reload can pick up its previous session (S3), and the visible notice when that session is
// gone: a lost terminal is never silent (D 19).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeRemshel is a stand-in terminal provider. It records every upgrade it was asked for and
// answers 404 for any session key that is not `live`.
type fakeRemshel struct {
	mu      sync.Mutex
	queries []url.Values
	cookies []string
	live    string // the one session key it still holds ("" = holds none)
}

func (f *fakeRemshel) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, r.URL.Query())
	f.cookies = append(f.cookies, r.Header.Get("Cookie"))
}

func (f *fakeRemshel) calls() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]url.Values, len(f.queries))
	copy(out, f.queries)
	return out
}

var fakeUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// startFakeRemshel serves the provider and points DEVLAB_REMSHEL_URL at it.
func startFakeRemshel(t *testing.T, f *fakeRemshel) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if s := r.URL.Query().Get("session"); s != "" && s != f.live {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": "No such session"})
			return
		}
		c, err := fakeUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		// Echo whatever arrives, so the test can prove the relay is live.
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DEVLAB_REMSHEL_URL", "ws://"+srv.Listener.Addr().String()+"/api/services/remshel/pty")
}

// terminalServer serves the pty route over a real listener (a WebSocket upgrade needs a
// hijackable connection) and returns the ws:// URL of the route.
func terminalServer(t *testing.T, s *Server) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/repos/{id}/pty", s.pty)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws://" + srv.Listener.Addr().String()
}

// dialTerminal opens the browser side of the terminal, with a same-origin Origin header.
func dialTerminal(t *testing.T, base, path string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u, err := url.Parse(base + path)
	if err != nil {
		t.Fatal(err)
	}
	hdr := http.Header{}
	hdr.Set("Origin", "http://"+u.Host)
	hdr.Set("Cookie", "h_access=abc")
	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	return d.Dial(u.String(), hdr)
}

func readNotice(t *testing.T, c *websocket.Conn) sessionNotice {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("no session notice: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("session notice must be a text control frame, got type %d", mt)
	}
	var n sessionNotice
	if err := json.Unmarshal(data, &n); err != nil {
		t.Fatalf("session notice is not JSON: %v (%s)", err, data)
	}
	return n
}

// TestTerminalCarriesTheSessionKey: a requested session key is forwarded to the provider verbatim
// (DevLab keeps no sessions of its own) and the browser is told the session was picked up.
func TestTerminalCarriesTheSessionKey(t *testing.T) {
	s, repo := devBypassServer(t)
	f := &fakeRemshel{live: "sess-live"}
	startFakeRemshel(t, f)
	base := terminalServer(t, s)

	c, _, err := dialTerminal(t, base, "/api/repos/"+repo+"/pty?session=sess-live")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	n := readNotice(t, c)
	if n.Type != "session" || n.State != "attached" || n.Session != "sess-live" {
		t.Errorf("notice = %+v, want the attached session sess-live", n)
	}
	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	if got := calls[0].Get("session"); got != "sess-live" {
		t.Errorf("forwarded session = %q, want sess-live", got)
	}
	if !strings.Contains(f.cookies[0], "h_access=abc") {
		t.Errorf("the caller's session cookie must reach the provider, got %q", f.cookies[0])
	}

	// The relay is live: a keystroke comes back from the provider's echo.
	if err := c.WriteMessage(websocket.BinaryMessage, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, back, err := c.ReadMessage()
	if err != nil || string(back) != "ls\n" {
		t.Errorf("relay did not pass the frame through: %q / %v", back, err)
	}
}

// TestTerminalLostSessionIsVisible: when the provider no longer holds the requested session, a
// fresh shell is opened AND the browser is told — never a silent loss (S3).
func TestTerminalLostSessionIsVisible(t *testing.T) {
	s, repo := devBypassServer(t)
	f := &fakeRemshel{live: ""} // holds nothing anymore
	startFakeRemshel(t, f)
	base := terminalServer(t, s)

	c, _, err := dialTerminal(t, base, "/api/repos/"+repo+"/pty?session=sess-gone")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	n := readNotice(t, c)
	if n.State != "new" {
		t.Errorf("state = %q, want a visibly NEW session", n.State)
	}
	if n.Reason == "" {
		t.Error("a session that could not be resumed must name the reason")
	}
	calls := f.calls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2 (attach attempt, then a fresh shell)", len(calls))
	}
	if calls[0].Get("session") != "sess-gone" {
		t.Errorf("first call must attempt the attach, got %v", calls[0])
	}
	if calls[1].Has("session") {
		t.Errorf("the fallback must ask for a fresh shell, got %v", calls[1])
	}
	if cwd := calls[1].Get("cwd"); !strings.HasSuffix(cwd, "/"+repo) {
		t.Errorf("the fresh shell must start in the workspace, cwd = %q", cwd)
	}
}

// TestTerminalWithoutSessionStartsFresh: with no key the terminal is honestly a new session, and
// the cwd is resolved server-side from the workspace — the client cannot choose it.
func TestTerminalWithoutSessionStartsFresh(t *testing.T) {
	s, repo := devBypassServer(t)
	f := &fakeRemshel{}
	startFakeRemshel(t, f)
	base := terminalServer(t, s)

	c, _, err := dialTerminal(t, base, "/api/repos/"+repo+"/pty?cwd=/etc")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	n := readNotice(t, c)
	if n.State != "new" || n.Reason != "" || n.Session != "" {
		t.Errorf("notice = %+v, want a plain new session", n)
	}
	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	if cwd := calls[0].Get("cwd"); cwd == "/etc" || !strings.HasSuffix(cwd, "/"+repo) {
		t.Errorf("cwd must be the server-resolved workspace, got %q", cwd)
	}
}

// TestTerminalRefusesCrossOrigin: the upgrade carries no CSRF header, so same-origin is the
// defence — and it is checked before the upstream is touched.
func TestTerminalRefusesCrossOrigin(t *testing.T) {
	s, repo := devBypassServer(t)
	f := &fakeRemshel{}
	startFakeRemshel(t, f)
	base := terminalServer(t, s)

	u, _ := url.Parse(base + "/api/repos/" + repo + "/pty")
	hdr := http.Header{}
	hdr.Set("Origin", "http://evil.example")
	if _, resp, err := (&websocket.Dialer{}).Dial(u.String(), hdr); err == nil {
		t.Fatal("a cross-origin upgrade must be refused")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin refusal status = %v, want 403", resp)
	}
	if len(f.calls()) != 0 {
		t.Error("the provider must not be dialled for a refused upgrade")
	}
}
