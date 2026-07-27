package runs

import (
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/live"
)

// recvTopic waits for one topic on a broker subscription, failing after 200ms.
func recvTopic(t *testing.T, ch <-chan live.Event) string {
	t.Helper()
	select {
	case ev := <-ch:
		return ev.Topic
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no topic published")
		return ""
	}
}

// recvNothing asserts nothing is published within a short window.
func recvNothing(t *testing.T, ch <-chan live.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("expected no publish, got %q", ev.Topic)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestStorePublishesOnChange: a successful Mutate and Patch each emit a "runs" tick; an erroring Mutate
// emits nothing; a store with no publisher still works.
func TestStorePublishesOnChange(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(t.TempDir(), "hist"))
	b := live.NewBroker()
	ch, cancel := b.Subscribe()
	defer cancel()
	s := NewStore()
	s.SetPublisher(b)

	if _, err := s.Mutate("seed", "t", func([]Run) ([]Run, error) {
		return []Run{{ID: "r", Enabled: true}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if topic := recvTopic(t, ch); topic != live.TopicRuns {
		t.Errorf("Mutate should publish runs, got %q", topic)
	}

	if _, err := s.Patch(func(cur []Run) ([]Run, error) { return cur, nil }); err != nil {
		t.Fatal(err)
	}
	if topic := recvTopic(t, ch); topic != live.TopicRuns {
		t.Errorf("Patch should publish runs, got %q", topic)
	}

	// An erroring Mutate writes nothing and publishes nothing.
	if _, err := s.Mutate("bad", "t", func([]Run) ([]Run, error) { return nil, ErrNotFound }); err == nil {
		t.Fatal("expected the erroring mutate to fail")
	}
	recvNothing(t, ch)
}

// TestStoreNoPublisherIsSafe: a store without a publisher mutates fine (nil-safe).
func TestStoreNoPublisherIsSafe(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(t.TempDir(), "hist"))
	s := NewStore()
	if _, err := s.Mutate("seed", "t", func([]Run) ([]Run, error) { return []Run{{ID: "r"}}, nil }); err != nil {
		t.Fatalf("a publisher-less store must still work: %v", err)
	}
}
