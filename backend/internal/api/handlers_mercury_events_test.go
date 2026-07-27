package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/live"
)

// The SSE stream proves the three behaviours the task requires of the mechanism: an externally
// triggered change appears on an open connection without a reload; closing (interrupting) the
// connection releases the subscription server-side; and a fresh connection re-subscribes and receives
// events again (the server side of "an interrupted connection finds its way back").
func TestMercuryEventsStreamDisconnectReconnect(t *testing.T) {
	s := &Server{live: live.NewBroker()}
	srv := httptest.NewServer(http.HandlerFunc(s.mercuryEvents))
	defer srv.Close()

	open := func() (<-chan string, func()) {
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("Content-Type = %q, want text/event-stream", ct)
		}
		lines := make(chan string, 16)
		done := make(chan struct{})
		go func() {
			br := bufio.NewReader(resp.Body)
			for {
				line, err := br.ReadString('\n')
				if data, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "data: "); ok {
					select {
					case lines <- data:
					case <-done:
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		return lines, func() { close(done); _ = resp.Body.Close() }
	}

	waitSubs := func(want int) {
		t.Helper()
		for i := 0; i < 400; i++ {
			if s.live.Subscribers() == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("subscriber count = %d, want %d", s.live.Subscribers(), want)
	}
	recv := func(ch <-chan string) string {
		t.Helper()
		select {
		case l := <-ch:
			return l
		case <-time.After(2 * time.Second):
			t.Fatal("no data line received")
			return ""
		}
	}

	// 1) Connect and receive an externally published change without reconnecting.
	lines1, close1 := open()
	waitSubs(1)
	s.publish(live.TopicRuns)
	if got := recv(lines1); got != live.TopicRuns {
		t.Fatalf("first event = %q, want %q", got, live.TopicRuns)
	}

	// 2) Interrupt the connection → the server drops the subscription (no leak).
	close1()
	waitSubs(0)

	// 3) Reconnect → a brand-new connection re-subscribes and receives events again.
	lines2, close2 := open()
	defer close2()
	waitSubs(1)
	s.publish(live.TopicActive)
	if got := recv(lines2); got != live.TopicActive {
		t.Fatalf("post-reconnect event = %q, want %q", got, live.TopicActive)
	}
}

// A change made through a handler (here mercuryReorder — the one axiom mutation that touches no
// network, only the local order file) announces an "axioms" change to open streams.
func TestReorderPublishesAxioms(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_ORDER", filepath.Join(t.TempDir(), "order.json"))
	s := &Server{live: live.NewBroker()}
	ch, cancel := s.live.Subscribe()
	defer cancel()

	req := httptest.NewRequest(http.MethodPost, "/api/mercury/reorder",
		strings.NewReader(`{"category":"axiome/architektur","order":["a.md","b.md"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.mercuryReorder(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, body=%s", rec.Code, rec.Body.String())
	}
	select {
	case ev := <-ch:
		if ev.Topic != live.TopicAxioms {
			t.Fatalf("topic = %q, want %q", ev.Topic, live.TopicAxioms)
		}
	case <-time.After(time.Second):
		t.Fatal("expected an axioms change notification")
	}
}
