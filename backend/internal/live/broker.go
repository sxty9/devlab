// Package live is Mercury's single change-notification transport: a lightweight in-process
// publish/subscribe broker that lets the server tell every open UI "something in the inventory
// changed — refetch it" without the UI polling on a fixed rhythm.
//
// It is deliberately tiny and knows nothing about HTTP, runs, or axioms: the api layer owns the
// SSE endpoint that fans a subscription out to one browser connection, and the domain layers
// (runs.Store, runs.Scheduler, the axiom handlers) call Publish after a successful mutation. This
// keeps the broker importable from anywhere with no import cycle.
//
// The contract is intentionally coarse: an Event carries only a Topic ("something of this kind
// changed"), never the changed data. A client reacts by refetching the current state through the
// normal read endpoints, so a coalesced or dropped notification is always safe — the next fetch
// reconciles to the truth. That is what lets Publish be non-blocking (see Publish).
package live

import "sync"

// Topic names the kind of thing that changed. The set is closed and shared verbatim with the
// frontend (src/lib/live.ts) so both ends agree on the wire vocabulary.
const (
	// TopicAxioms — the axiom / rule / Laufregel / meta tree changed (add, edit, move, delete,
	// reorder, category rename, migration or rollout apply).
	TopicAxioms = "axioms"
	// TopicRuns — a run or ToDo changed: config edits (create/update/delete/recompose/plan apply/
	// history restore) AND runtime state (schedule advanced, result attached, suspended, done). Every
	// such change flows through runs.Store, so publishing there covers them all in one place.
	TopicRuns = "runs"
	// TopicActive — the live-run pointer changed: a run started, its result id became known, or it
	// ended. Drives the "a run is live right now" surface without a resting poll.
	TopicActive = "active"
	// TopicProgress — the in-flight execution advanced (a step started/finished or the agent emitted
	// another line of transcript). Drives the live-follow view of a running execution.
	TopicProgress = "progress"
)

// Event is one change notification. Topic is one of the constants above.
type Event struct {
	Topic string `json:"topic"`
}

// Broker fans one Publish out to every current subscriber. Safe for concurrent use.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewBroker builds an empty broker.
func NewBroker() *Broker {
	return &Broker{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a new subscriber and returns its event channel plus a cancel func that
// removes and closes it. The channel is buffered so a brief consumer stall does not drop the most
// recent notifications, and closed on cancel so a range-over-channel consumer terminates cleanly.
// Every Subscribe MUST be paired with a call to cancel (typically deferred) or the channel leaks.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	// Buffered: an SSE writer briefly busy flushing must not make Publish drop the tick. 16 is ample
	// headroom given events coalesce to a plain "refetch" signal.
	ch := make(chan Event, 16)
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

// Publish delivers a Topic event to every subscriber. It NEVER blocks: a subscriber whose buffer is
// full is skipped (its next successful delivery, or the client's own reconnect/refetch, reconciles
// the missed tick). This matters because Publish is called from mutation paths that may hold a store
// lock — it must never stall on a slow or vanished reader. A nil broker is a no-op, so callers can
// hold an optional *Broker without nil checks.
func (b *Broker) Publish(topic string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- Event{Topic: topic}:
		default: // full → drop; a coarse "refetch" signal is safe to coalesce
		}
	}
}

// Subscribers reports the current subscriber count (for tests and diagnostics).
func (b *Broker) Subscribers() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
