package live

import (
	"sync"
	"testing"
	"time"
)

// TestBrokerFanOut: a published topic reaches every subscriber.
func TestBrokerFanOut(t *testing.T) {
	b := NewBroker()
	a, cancelA := b.Subscribe()
	c, cancelC := b.Subscribe()
	defer cancelA()
	defer cancelC()

	b.Publish(TopicRuns)
	for i, ch := range []<-chan Event{a, c} {
		select {
		case ev := <-ch:
			if ev.Topic != TopicRuns {
				t.Errorf("subscriber %d got %q, want %q", i, ev.Topic, TopicRuns)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d never received the topic", i)
		}
	}
}

// TestBrokerCancelIsIdempotentAndClosing: cancel removes the subscription, closes the channel, and is safe
// to call twice; publishing after cancel does not panic.
func TestBrokerCancelIsIdempotentAndClosing(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe()
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

// TestBrokerPublishNeverBlocks: a slow (undrained) subscriber never stalls Publish — extra ticks past the
// buffer are dropped, and a fast subscriber still receives.
func TestBrokerPublishNeverBlocks(t *testing.T) {
	b := NewBroker()
	_, cancelSlow := b.Subscribe() // never drained
	defer cancelSlow()
	fast, cancelFast := b.Subscribe()
	defer cancelFast()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ { // far past the 16-buffer of the slow subscriber
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
	case ev := <-fast:
		if ev.Topic != TopicProgress {
			t.Errorf("fast subscriber got %q", ev.Topic)
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

// TestBrokerConcurrent exercises Subscribe/Publish/cancel under -race.
func TestBrokerConcurrent(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe()
			go func() {
				for range ch {
				}
			}()
			for j := 0; j < 50; j++ {
				b.Publish(TopicRuns)
			}
			cancel()
		}()
	}
	wg.Wait()
}
