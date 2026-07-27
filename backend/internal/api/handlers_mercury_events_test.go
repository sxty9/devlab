package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/live"
)

// The SSE endpoint subscribes to the broker and streams a published topic to the client, after the
// reconnect-preamble. Cancelling the request context ends the handler (as a client disconnect would).
func TestMercuryEventsStreamsPublishedTopic(t *testing.T) {
	s := &Server{live: live.NewBroker()}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/mercury/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.mercuryEvents(rec, req)
		close(done)
	}()

	// Wait until the handler has subscribed, then publish a change.
	deadline := time.Now().Add(time.Second)
	for s.live.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if s.live.Subscribers() != 1 {
		t.Fatalf("handler did not subscribe (subscribers=%d)", s.live.Subscribers())
	}
	s.publish(live.TopicDeliveries)
	time.Sleep(50 * time.Millisecond) // let the handler write the event
	cancel()                          // client disconnect → handler returns, unsubscribes
	<-done

	if s.live.Subscribers() != 0 {
		t.Errorf("handler did not unsubscribe on disconnect (subscribers=%d)", s.live.Subscribers())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "retry: 3000") {
		t.Errorf("missing reconnect preamble: %q", body)
	}
	if !strings.Contains(body, "data: deliveries") {
		t.Errorf("published topic not streamed: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}
