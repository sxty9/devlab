// Package live is the SSE broker (S12). Exactly ONE stream per surface; the broker pushes
// TOPIC NAMES ONLY, never data — a lost tick is always safe because the client refetches
// through the normal read path. Non-blocking, drop-on-full.
package live

import "context"

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

// Broker fans topic ticks out to subscribers. Implementation is B7's; the type and its
// surface are the Welle-0 contract.
type Broker struct {
	_ struct{}
}

// NewBroker builds the broker (non-blocking, drop-on-full).
func NewBroker() *Broker {
	panic("TODO(B7)")
}

// Subscribe returns a topic channel and its cancel. The channel is never written to
// blockingly; on a full buffer ticks are dropped (the client refetches anyway).
func (b *Broker) Subscribe(ctx context.Context) (<-chan Topic, func()) {
	panic("TODO(B7)")
}

// Publish fans one topic tick out — never blocks.
func (b *Broker) Publish(t Topic) {
	panic("TODO(B7)")
}
