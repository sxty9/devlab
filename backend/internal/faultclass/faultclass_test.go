package faultclass

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
)

func status(code int) error { return &github.StatusError{Status: code, Msg: "x"} }

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"nil", nil, Satisfied},
		{"satisfied sentinel", ErrSatisfied, Satisfied},
		{"wrapped satisfied", fmt.Errorf("delete branch: %w", ErrSatisfied), Satisfied},
		{"404", status(404), Permanent},
		{"403", status(403), Permanent},
		{"422", status(422), Permanent},
		{"401", status(401), Permanent},
		{"410", status(410), Permanent},
		{"429 rate limit", status(429), Transient},
		{"500", status(500), Transient},
		{"other 4xx", status(400), Permanent},
		{"wrapped status", fmt.Errorf("create pr: %w", status(422)), Permanent},
		{"generic connectivity", errors.New("dial tcp: connection refused"), Transient},
		{"context canceled", context.Canceled, Permanent},
		{"context deadline", context.DeadlineExceeded, Permanent},
	}
	for _, c := range cases {
		if got := Classify(c.err); got != c.want {
			t.Errorf("%s: Classify = %v, want %v", c.name, got, c.want)
		}
	}
}

// K-5: the intervals grow demonstrably, and the record carries reason/time/attempts.
func TestNextGrowingIntervals(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	var b model.Backoff
	var prev time.Duration
	for i := 1; i <= 5; i++ {
		var blocked bool
		b, blocked = Next(b, now, 10, MaxDelay)
		if blocked {
			t.Fatalf("blocked at attempt %d with maxAttempts 10", i)
		}
		d := b.NextAt.Sub(now)
		if d <= prev {
			t.Fatalf("attempt %d: interval %v does not grow beyond %v", i, d, prev)
		}
		prev = d
	}
	if b.Attempts != 5 {
		t.Fatalf("attempts = %d, want 5", b.Attempts)
	}
	if b.FirstAt != now || b.LastAt != now {
		t.Fatalf("first/last not kept: %v / %v", b.FirstAt, b.LastAt)
	}
}

func TestNextBlocksAtMax(t *testing.T) {
	now := time.Now().UTC()
	var b model.Backoff
	var blocked bool
	for i := 0; i < 3; i++ {
		b, blocked = Next(b, now, 3, MaxDelay)
	}
	if !blocked {
		t.Fatalf("not blocked after maxAttempts")
	}
	if b.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", b.Attempts)
	}
}

// A self-ending obstacle is retried indefinitely: with maxAttempts 0 the schedule NEVER blocks,
// however many times it fails, and the growing interval is capped at the caller's own ceiling.
func TestNextNeverBlocksWithZeroCap(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	cap := time.Hour
	var b model.Backoff
	for i := 1; i <= 50; i++ {
		var blocked bool
		b, blocked = Next(b, now, 0, cap)
		if blocked {
			t.Fatalf("a zero cap must never block, blocked at attempt %d", i)
		}
		if d := b.NextAt.Sub(now); d > cap {
			t.Fatalf("attempt %d: interval %v exceeds the ceiling %v", i, d, cap)
		}
	}
	if b.Attempts != 50 {
		t.Fatalf("attempts = %d, want 50", b.Attempts)
	}
	// The tail settles exactly at the ceiling, not beyond it.
	if got := b.NextAt.Sub(now); got != cap {
		t.Fatalf("the settled interval = %v, want the ceiling %v", got, cap)
	}
}

func TestDelayCapped(t *testing.T) {
	if d := Delay(100, MaxDelay); d != MaxDelay {
		t.Fatalf("Delay(100) = %v, want cap %v", d, MaxDelay)
	}
	if Delay(1, MaxDelay) != BaseDelay {
		t.Fatalf("Delay(1) = %v, want base %v", Delay(1, MaxDelay), BaseDelay)
	}
	// The ceiling is the caller's: a one-hour cap lets the interval grow past the 15-minute default.
	if d := Delay(100, time.Hour); d != time.Hour {
		t.Fatalf("Delay(100, 1h) = %v, want 1h", d)
	}
}

func shrinkDelays(t *testing.T) {
	t.Helper()
	oldBase, oldMax := BaseDelay, MaxDelay
	BaseDelay, MaxDelay = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { BaseDelay, MaxDelay = oldBase, oldMax })
}

// K-5: a permanent fault (422 class) gets exactly ONE attempt and a named end.
func TestRetryPermanentOneAttempt(t *testing.T) {
	shrinkDelays(t)
	calls := 0
	var b model.Backoff
	err := Retry(context.Background(), &b, 5, func() error {
		calls++
		return fmt.Errorf("create pr: %w", status(422))
	})
	if calls != 1 {
		t.Fatalf("permanent fault retried: %d calls", calls)
	}
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != 422 {
		t.Fatalf("named end lost: %v", err)
	}
}

// K-5: a transient fault backs off with growing intervals and ends honestly blocked with
// reason, time and attempt count.
func TestRetryTransientEndsBlocked(t *testing.T) {
	shrinkDelays(t)
	calls := 0
	var b model.Backoff
	err := Retry(context.Background(), &b, 3, func() error {
		calls++
		return errors.New("connection reset")
	})
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("want BlockedError, got %v", err)
	}
	if be.Backoff.Attempts != 3 || be.Backoff.Reason != "connection reset" || be.Backoff.Class != "transient" {
		t.Fatalf("blocked record incomplete: %+v", be.Backoff)
	}
	if be.Backoff.LastAt.IsZero() || be.Backoff.FirstAt.IsZero() {
		t.Fatalf("blocked record misses times: %+v", be.Backoff)
	}
	if b.Attempts != 3 {
		t.Fatalf("caller's backoff state not updated: %+v", b)
	}
}

func TestRetryTransientThenSuccess(t *testing.T) {
	shrinkDelays(t)
	calls := 0
	err := Retry(context.Background(), nil, 5, func() error {
		calls++
		if calls < 3 {
			return errors.New("flaky")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("recovery failed: err=%v calls=%d", err, calls)
	}
}

// REQ-032.2: a goal already reached counts as success.
func TestRetrySatisfiedIsSuccess(t *testing.T) {
	shrinkDelays(t)
	err := Retry(context.Background(), nil, 5, func() error {
		return fmt.Errorf("branch already gone: %w", ErrSatisfied)
	})
	if err != nil {
		t.Fatalf("satisfied not success: %v", err)
	}
}

func TestRetryStopsOnCanceledContext(t *testing.T) {
	oldBase := BaseDelay
	BaseDelay = time.Hour // the wait must be interrupted by the context, not elapse
	t.Cleanup(func() { BaseDelay = oldBase })
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- Retry(ctx, nil, 5, func() error {
			calls++
			return errors.New("transient")
		})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Retry did not stop on canceled context")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
