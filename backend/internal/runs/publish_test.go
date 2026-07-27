package runs

import (
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/live"
)

func recvTopic(t *testing.T, ch <-chan live.Event) string {
	t.Helper()
	select {
	case ev := <-ch:
		return ev.Topic
	case <-time.After(time.Second):
		t.Fatal("expected a change notification, got none")
		return ""
	}
}

func recvNothing(t *testing.T, ch <-chan live.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("expected no notification, got topic %q", ev.Topic)
	case <-time.After(100 * time.Millisecond):
	}
}

// A successful Mutate (user config edit) and Patch (runtime state) both announce a "runs" change so
// open UIs refetch — this is the single point that covers create/update/delete AND schedule/result.
func TestStorePublishesOnMutateAndPatch(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	b := live.NewBroker()
	st := NewStore()
	st.SetPublisher(b)
	ch, cancel := b.Subscribe()
	defer cancel()

	if _, err := st.Mutate("create", "tester", func(cur []Run) ([]Run, error) {
		return append(cur, Run{ID: "r1", Name: "One"}), nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if got := recvTopic(t, ch); got != live.TopicRuns {
		t.Fatalf("Mutate topic = %q, want %q", got, live.TopicRuns)
	}

	if _, err := st.Patch(func(cur []Run) ([]Run, error) {
		if len(cur) > 0 {
			cur[0].Enabled = true
		}
		return cur, nil
	}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if got := recvTopic(t, ch); got != live.TopicRuns {
		t.Fatalf("Patch topic = %q, want %q", got, live.TopicRuns)
	}
}

// A mutation that ERRORS (e.g. unknown id) writes nothing, so it must NOT announce a change —
// otherwise every no-op update would wake every open UI for nothing.
func TestStoreDoesNotPublishOnError(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	b := live.NewBroker()
	st := NewStore()
	st.SetPublisher(b)
	ch, cancel := b.Subscribe()
	defer cancel()

	if _, err := st.Mutate("update", "tester", func(cur []Run) ([]Run, error) {
		return nil, ErrNotFound
	}); err == nil {
		t.Fatal("expected ErrNotFound")
	}
	recvNothing(t, ch)
}

// A store with no publisher wired must still work (nil-safe): publishing is optional.
func TestStoreWithoutPublisher(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	st := NewStore() // no SetPublisher
	if _, err := st.Mutate("create", "tester", func(cur []Run) ([]Run, error) {
		return append(cur, Run{ID: "r1"}), nil
	}); err != nil {
		t.Fatalf("Mutate without publisher: %v", err)
	}
}
