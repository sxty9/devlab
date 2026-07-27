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
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(t.TempDir(), "hist"))
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
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(t.TempDir(), "hist"))
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

// The delivery ledger announces a "deliveries" change on every write, so the Lieferungen surface
// refetches without polling.
func TestDeliveryStorePublishes(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS_DELIVERIES", filepath.Join(t.TempDir(), "deliveries.json"))
	b := live.NewBroker()
	ds := NewDeliveryStore()
	ds.SetPublisher(b)
	ch, cancel := b.Subscribe()
	defer cancel()

	if err := ds.Add(Delivery{ID: "d1", RunID: "r", Repo: "o/x", Status: DeliveryOpen}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := recvTopic(t, ch); got != live.TopicDeliveries {
		t.Fatalf("Add topic = %q, want %q", got, live.TopicDeliveries)
	}
}

// The scheduler announces an "active" change when a run goes live and when it ends, so the "what is
// running now" surface tracks it without a resting poll.
func TestSchedulerPublishesActive(t *testing.T) {
	store := seedStore(t, []Run{todoRun("t1", "repo-a")})
	b := live.NewBroker()
	be := newBlockingExec()
	s := NewScheduler(store, be, time.Second)
	s.logf = noopLog
	s.SetPublisher(b)
	ch, cancel := b.Subscribe()
	defer cancel()

	if _, ok := s.FireNow("t1", "user", false); !ok {
		t.Fatal("FireNow must start the run")
	}
	// The start (admit) publishes an active change.
	if got := recvTopic(t, ch); got != live.TopicActive {
		t.Fatalf("start topic = %q, want %q", got, live.TopicActive)
	}
	recvWithin(t, be.started, time.Second)
	be.release("t1")
	waitFor(t, 2*time.Second, func() bool { return s.ActiveCount() == 0 }, "t1 to finish")

	// The end (release) publishes another active change — drain any buffered report-active first.
	sawEnd := false
	for i := 0; i < 12 && !sawEnd; i++ {
		select {
		case ev := <-ch:
			if ev.Topic == live.TopicActive {
				sawEnd = true
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !sawEnd {
		t.Fatal("expected an active notification after the run ended")
	}
}

// A store with no publisher wired must still work (nil-safe): publishing is optional.
func TestStoreWithoutPublisher(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(t.TempDir(), "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(t.TempDir(), "hist"))
	st := NewStore() // no SetPublisher
	if _, err := st.Mutate("create", "tester", func(cur []Run) ([]Run, error) {
		return append(cur, Run{ID: "r1"}), nil
	}); err != nil {
		t.Fatalf("Mutate without publisher: %v", err)
	}
}
