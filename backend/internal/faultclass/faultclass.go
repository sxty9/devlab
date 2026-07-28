// Package faultclass is the ONE fault-classification point (K-5). Every retry in the system
// runs through Retry: a Permanent fault gets exactly one named attempt; a Transient one backs
// off with growing intervals until it is honestly "blocked" (reason, time, attempts —
// persisted at the affected record); Satisfied means the desired state already holds (e.g.
// deleting an already-deleted branch) and counts as success.
package faultclass

import (
	"context"
	"time"

	"devlab/backend/internal/model"
)

// Class is a fault's class.
type Class int

const (
	Transient Class = iota
	Permanent
	Satisfied
)

// Classify classifies an error: github.StatusError 404/403/422 ⇒ Permanent (with 404 on a
// delete-like operation being the CALLER's Satisfied); connectivity/timeouts ⇒ Transient;
// "already in the desired state" sentinels ⇒ Satisfied.
func Classify(err error) Class {
	panic("TODO(B3)")
}

// Next advances a backoff state: growing interval, and blocked=true once maxAttempts is
// reached.
func Next(b model.Backoff, now time.Time, maxAttempts int) (model.Backoff, bool) {
	panic("TODO(B3)")
}

// Retry drives op through the classification: Permanent ⇒ one attempt, named end; Transient ⇒
// growing backoff up to maxAttempts, then blocked; Satisfied ⇒ success. Used by executor,
// deliver and deploy — nobody retries outside this function.
func Retry(ctx context.Context, b *model.Backoff, maxAttempts int, op func() error) error {
	panic("TODO(B3)")
}
