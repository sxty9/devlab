package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/statepath"
)

// sseClient is a minimal EventSource for tests: it connects to an SSE handler and exposes the
// "data:" topic lines (and ":" comment lines, for the heartbeat) it receives on channels.
// close() disconnects — which the server observes as ctx.Done.
type sseClient struct {
	resp     *http.Response
	lines    chan string
	comments chan string
	stop     func()
}

func dialSSE(t *testing.T, url string) *sseClient {
	t.Helper()
	resp, err := http.Get(url) //nolint:bodyclose // closed via stop()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	lines := make(chan string, 16)
	comments := make(chan string, 16)
	done := make(chan struct{})
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			trimmed := strings.TrimRight(line, "\n")
			if data, ok := strings.CutPrefix(trimmed, "data: "); ok {
				select {
				case lines <- data:
				case <-done:
					return
				}
			} else if strings.HasPrefix(trimmed, ":") {
				select {
				case comments <- trimmed:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return &sseClient{resp: resp, lines: lines, comments: comments, stop: func() { close(done); _ = resp.Body.Close() }}
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

// TestEventsGuarded: the stream sits behind the session guard — without a session the request
// is rejected as JSON (401), no stream is opened, and no subscription is registered.
func TestEventsGuarded(t *testing.T) {
	t.Setenv("DEVLAB_DEV_BYPASS_AUTH", "") // ensure the bypass is off
	broker := live.NewBroker()
	s := &Server{v: auth.New(), broker: broker}
	srv := httptest.NewServer(s.guard(s.events)) // the same composition api.go registers
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("rejection Content-Type = %q, want application/json (no stream)", ct)
	}
	if broker.Subscribers() != 0 {
		t.Errorf("an unauthenticated request must not subscribe, got %d", broker.Subscribers())
	}
}

// TestEventsStreamWithoutCSRF proves the guarded happy path AND the no-CSRF property in one:
// with a valid session (dev-bypass verifier) and NO CSRF header or cookie, the GET streams.
// It then proves the server side of self-healing: an interrupted connection releases its
// subscription, and a fresh connection re-subscribes and receives again.
func TestEventsStreamWithoutCSRF(t *testing.T) {
	t.Setenv("DEVLAB_DEV_BYPASS_AUTH", "1")
	s := &Server{v: auth.New(), broker: live.NewBroker()}
	srv := httptest.NewServer(s.guard(s.events))
	defer srv.Close()

	// 1) Connect (no CSRF anywhere) and receive an externally published change, no reload.
	c1 := dialSSE(t, srv.URL)
	waitSubscribers(t, s.broker, 1)
	s.publish(live.TopicRuns)
	if got := c1.next(t); got != string(live.TopicRuns) {
		t.Fatalf("first event = %q, want %q", got, live.TopicRuns)
	}

	// 2) Interrupt the connection → the server drops the subscription (no leak).
	c1.stop()
	waitSubscribers(t, s.broker, 0)

	// 3) Reconnect → a brand-new connection re-subscribes and receives events again.
	c2 := dialSSE(t, srv.URL)
	defer c2.stop()
	waitSubscribers(t, s.broker, 1)
	s.publish(live.TopicActive)
	if got := c2.next(t); got != string(live.TopicActive) {
		t.Fatalf("post-reconnect event = %q, want %q", got, live.TopicActive)
	}
}

// TestEventsPayloadIsTopicNameOnly: every data line carries EXACTLY a topic name from the
// closed set — never data. That is what makes a lost tick safe (S12).
func TestEventsPayloadIsTopicNameOnly(t *testing.T) {
	s := &Server{broker: live.NewBroker()}
	srv := httptest.NewServer(http.HandlerFunc(s.events))
	defer srv.Close()

	c := dialSSE(t, srv.URL)
	defer c.stop()
	waitSubscribers(t, s.broker, 1)

	valid := map[string]bool{}
	for _, tp := range live.Topics() {
		valid[string(tp)] = true
	}
	for _, tp := range live.Topics() {
		s.publish(tp)
	}
	for range live.Topics() {
		got := c.next(t)
		if !valid[got] {
			t.Fatalf("data line %q is not a bare topic name from the closed set", got)
		}
	}
}

// TestEventsHeartbeat: an idle stream stays alive through comment-line pings (ignored by
// EventSource), so intermediaries never time the connection out.
func TestEventsHeartbeat(t *testing.T) {
	old := eventsHeartbeat
	eventsHeartbeat = 20 * time.Millisecond
	defer func() { eventsHeartbeat = old }()

	s := &Server{broker: live.NewBroker()}
	srv := httptest.NewServer(http.HandlerFunc(s.events))
	defer srv.Close()

	c := dialSSE(t, srv.URL)
	defer c.stop()
	select {
	case got := <-c.comments:
		if !strings.HasPrefix(got, ":") {
			t.Fatalf("heartbeat %q is not an SSE comment line", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no heartbeat received on an idle stream")
	}
}

// TestEventsWithoutBrokerFailsSoft: an unwired broker answers 501 JSON instead of hanging.
func TestEventsWithoutBrokerFailsSoft(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.events(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

// TestStoreChangeReachesOpenStream — end to end (fixture publisher): a run written through
// the real store, followed by the caller-side publish exactly as the frozen handlers do it
// (write succeeds → s.publish), reaches an already-open SSE connection as a bare "runs"
// tick — an external change appears without a reload.
func TestStoreChangeReachesOpenStream(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", "") // pin the store to the temp state root
	store := runs.NewStore(&statepath.Paths{Root: t.TempDir()})
	s := &Server{broker: live.NewBroker(), runs: store}

	srv := httptest.NewServer(http.HandlerFunc(s.events))
	defer srv.Close()

	c := dialSSE(t, srv.URL)
	defer c.stop()
	waitSubscribers(t, s.broker, 1)

	// The fixture publisher: a successful write through the passive pool, then the tick —
	// the pool itself stays passive (the publisher is always the caller).
	now := time.Now().UTC()
	who := model.Actor{User: "someone-else"}
	if err := store.Put(runs.Run{
		ID:         runs.NewID(),
		Kind:       model.KindTodo,
		Title:      "From another session",
		Authorship: model.Authorship{Created: who, CreatedAt: now, Updated: who, UpdatedAt: now},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	s.publish(live.TopicRuns)

	if got := c.next(t); got != string(live.TopicRuns) {
		t.Fatalf("event = %q, want %q", got, live.TopicRuns)
	}
}
