package live

import (
	"sync"
	"testing"
	"time"
)

// A published event reaches every current subscriber.
func TestPublishFansOutToAllSubscribers(t *testing.T) {
	b := NewBroker()
	a, cancelA := b.Subscribe()
	c, cancelC := b.Subscribe()
	defer cancelA()
	defer cancelC()

	if got := b.Subscribers(); got != 2 {
		t.Fatalf("Subscribers = %d, want 2", got)
	}

	b.Publish(TopicRuns)

	for i, ch := range []<-chan Event{a, c} {
		select {
		case ev := <-ch:
			if ev.Topic != TopicRuns {
				t.Fatalf("subscriber %d got topic %q, want %q", i, ev.Topic, TopicRuns)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

// After cancel the channel is closed and no longer receives events.
func TestCancelUnsubscribesAndCloses(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
	cancel()

	if got := b.Subscribers(); got != 0 {
		t.Fatalf("Subscribers after cancel = %d, want 0", got)
	}
	// The channel is closed: a receive returns the zero value with ok=false.
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
	// Publishing after everyone left must not panic (no send on a closed channel).
	b.Publish(TopicRuns)
	// cancel is idempotent — a deferred second call must not double-close.
	cancel()
}

// A subscriber that never drains does not block Publish, and does not stall other subscribers.
func TestPublishNeverBlocksOnASlowSubscriber(t *testing.T) {
	b := NewBroker()
	slow, cancelSlow := b.Subscribe() // never drained
	fast, cancelFast := b.Subscribe()
	defer cancelSlow()
	defer cancelFast()

	done := make(chan struct{})
	go func() {
		// Far more than the buffer (16): the slow subscriber's queue fills and the rest are dropped,
		// but Publish must keep returning promptly.
		for i := 0; i < 1000; i++ {
			b.Publish(TopicActive)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	// The fast subscriber can still drain what it buffered — delivery is not wedged.
	select {
	case ev := <-fast:
		if ev.Topic != TopicActive {
			t.Fatalf("got %q, want %q", ev.Topic, TopicActive)
		}
	case <-time.After(time.Second):
		t.Fatal("fast subscriber received nothing")
	}
	_ = slow
}

// A nil broker is a usable no-op so optional publishers need no nil-guards.
func TestNilBrokerIsNoOp(t *testing.T) {
	var b *Broker
	b.Publish(TopicRuns) // must not panic
	if b.Subscribers() != 0 {
		t.Fatal("nil broker should report 0 subscribers")
	}
}

// Concurrent Subscribe/Publish/cancel must be race-free (run with -race).
func TestConcurrentSubscribePublishCancel(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe()
			go func() {
				for range ch { // drain until closed
				}
			}()
			b.Publish(TopicProgress)
			cancel()
		}()
	}
	for i := 0; i < 50; i++ {
		b.Publish(TopicRuns)
	}
	wg.Wait()
}
