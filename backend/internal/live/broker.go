// Package live is a tiny in-process publish/subscribe broker for the ONE Mercury change-stream. It knows
// nothing of HTTP, runs, or axioms (zero dependencies), so any layer can import it without a cycle. The
// payload is ONLY a topic name — never data — so a dropped or coalesced tick is always safe: a subscriber
// simply refetches through the normal read endpoints.
package live

import "sync"

// Topics — the coarse channels the UI subscribes to. Shared verbatim with the frontend.
const (
	TopicAxioms     = "axioms"     // the axiom/rule tree changed (add/edit/move/delete/reorder/rename/migrate)
	TopicRuns       = "runs"       // a run/ToDo changed: config edits AND runtime state — all flow through runs.Store
	TopicActive     = "active"     // the live-run set changed: started / result-id known / ended
	TopicProgress   = "progress"   // an in-flight execution advanced (a step or transcript line)
	TopicDeliveries = "deliveries" // the delivery ledger changed (a delivery/PR outcome, a blocked deploy)
)

// Event is what a subscriber receives — just the topic that changed.
type Event struct {
	Topic string `json:"topic"`
}

// Broker is a minimal fan-out pub/sub. Publish never blocks (a full subscriber is skipped), so it is safe
// to call while holding a store lock.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBroker() *Broker { return &Broker{subs: make(map[chan Event]struct{})} }

// Subscribe returns a buffered receive channel and a cancel that removes-and-closes it (idempotent).
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16) // headroom so a briefly-busy SSE writer doesn't make Publish drop a tick
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			close(ch)
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

// Publish delivers a topic to every subscriber. It NEVER blocks: a subscriber whose buffer is full is
// skipped (its next refetch reconciles). A nil broker is a no-op, so publishing is always safe.
func (b *Broker) Publish(topic string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- Event{Topic: topic}:
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
