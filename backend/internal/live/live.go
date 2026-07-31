// Package live is the SSE broker (S12). Exactly ONE stream per surface; the broker pushes
// TOPIC NAMES ONLY, never data — a lost tick is always safe because the client refetches
// through the normal read path. Non-blocking, drop-on-full.
package live

import (
	"context"
	"sync"
)

// Topic is one of the EXACTLY EIGHT topics (the closed set; REQ-034).
type Topic string

const (
	TopicAxioms     Topic = "axioms"
	TopicRuns       Topic = "runs"
	TopicActive     Topic = "active"
	TopicProgress   Topic = "progress"
	TopicDeliveries Topic = "deliveries"
	TopicNotices    Topic = "notices"
	TopicSlots      Topic = "slots"
	TopicRestart    Topic = "restart"
)

// Topics returns the closed topic set (for guards and tests).
func Topics() []Topic {
	return []Topic{TopicAxioms, TopicRuns, TopicActive, TopicProgress, TopicDeliveries, TopicNotices, TopicSlots, TopicRestart}
}

// Publisher is the injection seam the writers (handlers, sched, recorder, deliver) publish
// through after a successful write.
type Publisher interface {
	Publish(t Topic)
}

// subscriberBuffer gives a briefly-busy SSE writer headroom before Publish starts dropping
// ticks on its channel (a dropped tick is safe — the client's next refetch reconciles).
const subscriberBuffer = 16

// Broker fans topic ticks out to subscribers. Implementation is B7's; the type and its
// surface are the Welle-0 contract.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Topic]struct{}
}

// NewBroker builds the broker (non-blocking, drop-on-full).
func NewBroker() *Broker {
	return &Broker{subs: make(map[chan Topic]struct{})}
}

// Subscribe returns a topic channel and its cancel. The channel is never written to
// blockingly; on a full buffer ticks are dropped (the client refetches anyway). The
// subscription ends when cancel is called OR the context is done — cancel is idempotent
// and closes the channel.
func (b *Broker) Subscribe(ctx context.Context) (<-chan Topic, func()) {
	ch := make(chan Topic, subscriberBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			// Closing under the lock excludes a concurrent Publish send on this channel.
			close(ch)
			b.mu.Unlock()
		})
	}
	// context.Background() has a nil Done channel — no watcher goroutine is spawned then.
	if ctx != nil && ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			cancel()
		}()
	}
	return ch, cancel
}

// Publish fans one topic tick out — it NEVER blocks: a subscriber whose buffer is full is
// skipped (its next refetch reconciles). Safe to call while holding a store lock, and safe
// on a nil broker (publishers wire it optionally).
func (b *Broker) Publish(t Topic) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- t:
		default: // full → drop; a coalesced "refetch" is safe
		}
	}
}

// Subscribers reports how many streams are open (tests/diagnostics).
func (b *Broker) Subscribers() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
