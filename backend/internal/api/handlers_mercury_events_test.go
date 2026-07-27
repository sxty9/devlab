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
	"devlab/backend/internal/runs"
)

// sseClient is a minimal EventSource for tests: it connects to an SSE handler and exposes the "data:"
// topic lines it receives on a channel. close() disconnects (which the server observes as ctx.Done).
type sseClient struct {
	lines chan string
	stop  func()
}

func dialSSE(t *testing.T, url string) *sseClient {
	t.Helper()
	resp, err := http.Get(url) //nolint:bodyclose // closed via stop()
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
	return &sseClient{lines: lines, stop: func() { close(done); _ = resp.Body.Close() }}
}

func (c *sseClient) next(t *testing.T) string {
	t.Helper()
	select {
	case l := <-c.lines:
		return l
	case <-time.After(2 * time.Second):
		t.Fatal("no data line received")
		return ""
	}
}

func waitSubscribers(t *testing.T, b *live.Broker, want int) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if b.Subscribers() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscriber count = %d, want %d", b.Subscribers(), want)
}

// The SSE stream proves three behaviours the task requires: an externally triggered change appears on
// an open connection without a reload; closing (interrupting) the connection releases the subscription
// server-side; and a fresh connection re-subscribes and receives events again (the server side of "an
// interrupted connection finds its way back").
func TestMercuryEventsStreamDisconnectReconnect(t *testing.T) {
	s := &Server{live: live.NewBroker()}
	srv := httptest.NewServer(http.HandlerFunc(s.mercuryEvents))
	defer srv.Close()

	// 1) Connect and receive an externally published change without reconnecting.
	c1 := dialSSE(t, srv.URL)
	waitSubscribers(t, s.live, 1)
	s.publish(live.TopicRuns)
	if got := c1.next(t); got != live.TopicRuns {
		t.Fatalf("first event = %q, want %q", got, live.TopicRuns)
	}

	// 2) Interrupt the connection → the server drops the subscription (no leak).
	c1.stop()
	waitSubscribers(t, s.live, 0)

	// 3) Reconnect → a brand-new connection re-subscribes and receives events again.
	c2 := dialSSE(t, srv.URL)
	defer c2.stop()
	waitSubscribers(t, s.live, 1)
	s.publish(live.TopicActive)
	if got := c2.next(t); got != live.TopicActive {
		t.Fatalf("post-reconnect event = %q, want %q", got, live.TopicActive)
	}
}

// End to end: a run created through the store (an "external" change, exactly as another session or the
// scheduler would make it) reaches an already-open stream as a "runs" notification — no reload.
func TestRunChangeReachesOpenStream(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	broker := live.NewBroker()
	store := runs.NewStore()
	store.SetPublisher(broker)
	s := &Server{live: broker, runs: store}

	srv := httptest.NewServer(http.HandlerFunc(s.mercuryEvents))
	defer srv.Close()

	c := dialSSE(t, srv.URL)
	defer c.stop()
	waitSubscribers(t, s.live, 1)

	if _, err := store.Mutate("create", "someone-else", func(cur []runs.Run) ([]runs.Run, error) {
		return append(cur, runs.Run{ID: "run_x", Name: "From another session"}), nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if got := c.next(t); got != live.TopicRuns {
		t.Fatalf("event = %q, want %q", got, live.TopicRuns)
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
