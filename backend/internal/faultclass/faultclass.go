// Package faultclass is the ONE fault-classification point (K-5). Every retry in the system
// runs through Retry: a Permanent fault gets exactly one named attempt; a Transient one backs
// off with growing intervals until it is honestly "blocked" (reason, time, attempts —
// persisted at the affected record); Satisfied means the desired state already holds (e.g.
// deleting an already-deleted branch) and counts as success.
package faultclass

import (
	"context"
	"errors"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
)

// Class is a fault's class.
type Class int

const (
	Transient Class = iota
	Permanent
	Satisfied
)

// String names the class for backoff records and honest messages.
func (c Class) String() string {
	switch c {
	case Permanent:
		return "permanent"
	case Satisfied:
		return "satisfied"
	}
	return "transient"
}

// ErrSatisfied is the sentinel a caller wraps (or returns) when an operation finds its goal
// already reached — e.g. deleting a branch that is already gone. Classify maps it (and any
// error wrapping it) to Satisfied, which Retry treats as success (REQ-032.2).
var ErrSatisfied = errors.New("already in the desired state")

// Backoff schedule: growing intervals, base*2^(attempts-1) capped at a maximum. BaseDelay is the
// first interval; MaxDelay is the DEFAULT ceiling Retry uses and the one a caller passes when it has
// no ceiling of its own. Both are package variables so tests can shrink them; production leaves the
// defaults. A caller that wants a different ceiling (the PR maintenance retries a self-ending
// obstacle no more often than once an hour) passes it explicitly to Delay/Next.
var (
	BaseDelay = 10 * time.Second
	MaxDelay  = 15 * time.Minute
)

// Classify classifies an error:
//   - nil or (wrapping) ErrSatisfied ⇒ Satisfied — the goal already holds.
//   - github.StatusError with a definitive client status (404 not found, 403 forbidden,
//     401 unauthorized, 410 gone, 422 unprocessable) ⇒ Permanent — the object does not exist,
//     permission is missing, or the request is invalid; repeating cannot change that. A 404 on
//     a delete-like operation is the CALLER's Satisfied (wrap ErrSatisfied there).
//   - github.StatusError 429 or any 5xx ⇒ Transient (rate limiting, server hiccups).
//   - context cancellation/deadline ⇒ Permanent for the classification's purpose: the caller
//     decided to stop; a retry loop must not spin on a dead context.
//   - everything else (connectivity, DNS, timeouts, unclassified failures) ⇒ Transient —
//     bounded by Retry's attempt cap, never endless (K-5).
func Classify(err error) Class {
	if err == nil || errors.Is(err, ErrSatisfied) {
		return Satisfied
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Permanent
	}
	var se *github.StatusError
	if errors.As(err, &se) {
		switch {
		case se.Status == 401 || se.Status == 403 || se.Status == 404 || se.Status == 410 || se.Status == 422:
			return Permanent
		case se.Status == 429 || se.Status >= 500:
			return Transient
		}
		// Other 4xx: the request as made will not succeed by repetition.
		if se.Status >= 400 && se.Status < 500 {
			return Permanent
		}
	}
	return Transient
}

// Delay returns the growing interval before attempt n (1-based): BaseDelay*2^(n-1), capped at max.
// Exposed so tests can prove the intervals grow (K-5). The cap is a parameter, not a fixed global,
// so a caller that must retry a self-ending obstacle indefinitely can space the retries out to its
// own ceiling (e.g. one hour) rather than the package default.
func Delay(attempt int, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := BaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}

// Next advances a backoff state by one failed attempt at `now`: attempts increment, the
// first/last timestamps are kept honest, and NextAt moves out by the growing interval, capped at
// maxDelay.
//
// blocked is true once maxAttempts is reached — the record then waits for an EXPLICIT resume in the
// surface (REQ-032.3), never for another silent retry. A maxAttempts of 0 (or less) means the
// schedule NEVER blocks: the obstacle is one that ends by itself, so it is retried indefinitely,
// only ever more slowly (up to maxDelay between attempts). There is then no attempt count that
// gives up — the growing, visible backoff record IS the reaction, not a countdown to a standstill.
func Next(b model.Backoff, now time.Time, maxAttempts int, maxDelay time.Duration) (model.Backoff, bool) {
	b.Attempts++
	if b.FirstAt.IsZero() {
		b.FirstAt = now
	}
	b.LastAt = now
	b.NextAt = now.Add(Delay(b.Attempts, maxDelay))
	return b, maxAttempts > 0 && b.Attempts >= maxAttempts
}

// BlockedError reports that a transient fault exhausted its attempts: the operation is now
// "blocked" with reason, time and attempt count (K-5) and waits for explicit resumption.
type BlockedError struct {
	Backoff model.Backoff
	Err     error
}

// Error names the block honestly: reason, attempts, when.
func (e *BlockedError) Error() string {
	return "blocked after " + itoa(e.Backoff.Attempts) + " attempts (" +
		e.Backoff.LastAt.UTC().Format(time.RFC3339) + "): " + e.Backoff.Reason
}

// Unwrap exposes the underlying fault.
func (e *BlockedError) Unwrap() error { return e.Err }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Retry drives op through the classification — nobody in the rebuild retries outside this
// function (K-5):
//   - success or Satisfied ⇒ nil (a goal already reached counts as success, REQ-032.2);
//   - Permanent ⇒ exactly ONE attempt, the named error returned unchanged;
//   - Transient ⇒ growing intervals recorded in *b (reason, class, attempts, times); once
//     maxAttempts is reached the result is a *BlockedError carrying the final backoff state,
//     which the caller persists at the affected record and surfaces for explicit resumption.
//
// The wait is context-aware: a canceled context returns ctx.Err() immediately. b may be nil
// (a throwaway state is used); when non-nil it is updated in place so the caller can persist
// every intermediate state.
func Retry(ctx context.Context, b *model.Backoff, maxAttempts int, op func() error) error {
	if b == nil {
		b = &model.Backoff{}
	}
	for {
		err := op()
		switch Classify(err) {
		case Satisfied:
			return nil
		case Permanent:
			return err
		}
		next, blocked := Next(*b, time.Now().UTC(), maxAttempts, MaxDelay)
		next.Reason = err.Error()
		next.Class = Transient.String()
		*b = next
		if blocked {
			return &BlockedError{Backoff: *b, Err: err}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(b.NextAt)):
		}
	}
}
