package live

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestTopicsClosedSet pins the EXACTLY EIGHT topics (REQ-034): the set is closed, distinct,
// and matches the frontend vocabulary verbatim.
func TestTopicsClosedSet(t *testing.T) {
	want := []Topic{"axioms", "runs", "active", "progress", "deliveries", "notices", "slots", "restart"}
	got := Topics()
	if len(got) != 8 {
		t.Fatalf("Topics() has %d entries, want exactly 8", len(got))
	}
	seen := map[Topic]bool{}
	for i, tp := range got {
		if tp != want[i] {
			t.Errorf("Topics()[%d] = %q, want %q", i, tp, want[i])
		}
		if seen[tp] {
			t.Errorf("duplicate topic %q", tp)
		}
		seen[tp] = true
	}
}

// TestBrokerFanOut: a published topic reaches every subscriber.
func TestBrokerFanOut(t *testing.T) {
	b := NewBroker()
	a, cancelA := b.Subscribe(context.Background())
	c, cancelC := b.Subscribe(context.Background())
	defer cancelA()
	defer cancelC()

	b.Publish(TopicRuns)
	for i, ch := range []<-chan Topic{a, c} {
		select {
		case got := <-ch:
			if got != TopicRuns {
				t.Errorf("subscriber %d got %q, want %q", i, got, TopicRuns)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d never received the topic", i)
		}
	}
}

// TestBrokerCancelIsIdempotentAndClosing: cancel removes the subscription, closes the
// channel, and is safe to call twice; publishing after cancel does not panic.
func TestBrokerCancelIsIdempotentAndClosing(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe(context.Background())
	if b.Subscribers() != 1 {
		t.Fatalf("expected 1 subscriber, got %d", b.Subscribers())
	}
	cancel()
	cancel() // idempotent — must not panic (double close)
	if b.Subscribers() != 0 {
		t.Errorf("cancel must remove the subscription, got %d", b.Subscribers())
	}
	if _, ok := <-ch; ok {
		t.Error("the channel must be closed after cancel")
	}
	b.Publish(TopicActive) // publish after cancel — no panic, no delivery
}

// TestBrokerContextCancelUnsubscribes: when the subscriber's context ends (an SSE client
// disconnect), the subscription is released without an explicit cancel call.
func TestBrokerContextCancelUnsubscribes(t *testing.T) {
	b := NewBroker()
	ctx, stop := context.WithCancel(context.Background())
	ch, cancel := b.Subscribe(ctx)
	defer cancel()
	if b.Subscribers() != 1 {
		t.Fatalf("expected 1 subscriber, got %d", b.Subscribers())
	}
	stop()
	deadline := time.After(time.Second)
	for b.Subscribers() != 0 {
		select {
		case <-deadline:
			t.Fatal("context cancellation did not release the subscription")
		case <-time.After(2 * time.Millisecond):
		}
	}
	if _, ok := <-ch; ok {
		t.Error("the channel must be closed after the context ends")
	}
}

// TestBrokerPublishNeverBlocks: a slow (undrained) subscriber never stalls Publish — extra
// ticks past the buffer are dropped, and a fast subscriber still receives.
func TestBrokerPublishNeverBlocks(t *testing.T) {
	b := NewBroker()
	_, cancelSlow := b.Subscribe(context.Background()) // never drained
	defer cancelSlow()
	fast, cancelFast := b.Subscribe(context.Background())
	defer cancelFast()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ { // far past the subscriber buffer of the slow one
			b.Publish(TopicProgress)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
	select {
	case got := <-fast:
		if got != TopicProgress {
			t.Errorf("fast subscriber got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the fast subscriber received nothing")
	}
}

// TestNilBrokerIsNoOp: a nil broker is safe to publish to (publishers wire it optionally).
func TestNilBrokerIsNoOp(t *testing.T) {
	var b *Broker
	b.Publish(TopicRuns) // must not panic
	if b.Subscribers() != 0 {
		t.Error("a nil broker has no subscribers")
	}
}

// TestBrokerConcurrent exercises Subscribe/Publish/cancel (explicit and via context) under -race.
func TestBrokerConcurrent(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(viaCtx bool) {
			defer wg.Done()
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			ch, cancel := b.Subscribe(ctx)
			go func() {
				for range ch {
				}
			}()
			for j := 0; j < 50; j++ {
				b.Publish(TopicRuns)
			}
			if viaCtx {
				stop()
			} else {
				cancel()
			}
		}(i%2 == 0)
	}
	wg.Wait()
}
