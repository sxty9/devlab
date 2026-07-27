package api

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/live"
)

// TestMercuryEventsStreamsPublishedTopics: an open /events stream sets the SSE headers and delivers a
// topic published after it subscribed (the store→stream path an open surface relies on).
func TestMercuryEventsStreamsPublishedTopics(t *testing.T) {
	s := &Server{live: live.NewBroker()}
	srv := httptest.NewServer(http.HandlerFunc(s.mercuryEvents))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// Wait until our connection has registered as a subscriber, then publish.
	deadline := time.Now().Add(2 * time.Second)
	for s.live.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.live.Subscribers() == 0 {
		t.Fatal("the stream never subscribed to the broker")
	}
	s.live.Publish(live.TopicRuns)

	// Read lines until the data frame arrives.
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	got := false
	timeout := time.After(2 * time.Second)
	for !got {
		select {
		case line := <-lines:
			if strings.TrimSpace(line) == "data: runs" {
				got = true
			}
		case <-timeout:
			t.Fatal("the published topic never arrived on the stream")
		}
	}

	// Closing the response cancels the request context → the handler returns and unsubscribes.
	resp.Body.Close()
	for s.live.Subscribers() != 0 && time.Now().Before(time.Now().Add(time.Second)) {
		time.Sleep(5 * time.Millisecond)
	}
}
