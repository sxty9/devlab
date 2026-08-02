package api

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"devlab/backend/internal/notify"
	"devlab/backend/internal/runs"
)

// fakeEmitter records what the dispatcher tried to deliver, so a test observes the outward voice
// without a running notify service.
type fakeEmitter struct {
	mu     sync.Mutex
	events []notify.Event
	err    error
}

func (f *fakeEmitter) Emit(_ context.Context, e notify.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func (f *fakeEmitter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

// wire builds a real notice pool wired to a synchronous dispatcher over the fake emitter — the same
// path production uses, minus the goroutine, so the delivery is observable the instant a notice is
// recorded.
func wire(t *testing.T) (*runs.NoticeStore, *fakeEmitter) {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(t.TempDir(), "notices.json"))
	store := runs.NewNoticeStore(nil)
	fake := &fakeEmitter{}
	d := &noticeDispatcher{
		recipient:  "ada",
		linkBase:   "https://devlab.example",
		newEmitter: func() (noticeEmitter, error) { return fake, nil },
		async:      false, // synchronous: the delivery completes before Coalesce returns
	}
	store.SetOnNew(d.dispatch)
	return store, fake
}

// (a) A recognised disturbance leads to EXACTLY ONE delivery, carrying the finding to the named owner.
func TestNoticeDeliveryDeliversADisturbanceOnce(t *testing.T) {
	store, fake := wire(t)
	if _, err := store.Coalesce(runs.Notice{
		Kind: runs.NoticeDeliveryBlocked, Repo: "o/x", Text: "pull request #7 is blocked: 502 from origin",
	}); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 1 {
		t.Fatalf("a disturbance must be delivered exactly once, got %d", fake.count())
	}
	e := fake.events[0]
	if e.User != "ada" {
		t.Errorf("recipient = %q, want the run owner", e.User)
	}
	if e.Title != "Delivery blocked — o/x" {
		t.Errorf("title = %q, want the humanized kind with the repo", e.Title)
	}
	if e.Body != "pull request #7 is blocked: 502 from origin" {
		t.Errorf("body = %q, want the finding's wording", e.Body)
	}
	if e.Level != notify.LevelWarning || e.URL != "https://devlab.example/#/mercury" {
		t.Errorf("a disturbance is a warning that links into the dashboard, got level=%q url=%q", e.Level, e.URL)
	}
}

// (b) The SAME disturbance a second time leads to no further delivery — the pool bundles the repeat
// and the outward voice stays quiet.
func TestNoticeDeliveryDoesNotRedeliverARepeat(t *testing.T) {
	store, fake := wire(t)
	blocked := func() runs.Notice {
		return runs.Notice{Kind: runs.NoticeDeliveryBlocked, Repo: "o/x", Text: "pull request #7 is blocked: 502"}
	}
	for i := 0; i < 5; i++ {
		if _, err := store.Coalesce(blocked()); err != nil {
			t.Fatal(err)
		}
	}
	if fake.count() != 1 {
		t.Fatalf("a repeating disturbance is delivered ONCE, got %d deliveries", fake.count())
	}
}

// (c) Operational noise leads to NO delivery — it is recorded in the pool but never pushed outward.
func TestNoticeDeliveryIgnoresOperationalNoise(t *testing.T) {
	store, fake := wire(t)
	for _, kind := range []string{"restart-requested", "restart-completed", "startup-reconcile", runs.NoticeAssigned} {
		if _, err := store.Coalesce(runs.Notice{Kind: kind, Text: "routine"}); err != nil {
			t.Fatal(err)
		}
	}
	if fake.count() != 0 {
		t.Fatalf("operational noise must not be delivered, got %d deliveries", fake.count())
	}
	// The noise is still in the pool — recorded, just not delivered.
	list, _ := store.List()
	if len(list) != 4 {
		t.Fatalf("noise stays in the pool for the record, want 4, got %d", len(list))
	}
}

// A failing emit is a logged skip, not a crash: the notice is safely recorded regardless, and a later
// distinct disturbance is still attempted.
func TestNoticeDeliverySkipsOnEmitFailure(t *testing.T) {
	store, fake := wire(t)
	fake.err = context.DeadlineExceeded
	if _, err := store.Coalesce(runs.Notice{Kind: runs.NoticeGitHubQuota, Text: "budget exhausted"}); err != nil {
		t.Fatalf("a delivery failure must not fail the record write: %v", err)
	}
	list, _ := store.List()
	if len(list) != 1 {
		t.Fatalf("the notice is recorded even when its delivery fails, got %d", len(list))
	}
}
