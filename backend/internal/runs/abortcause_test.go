package runs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAbortCausePropagation pins the invariant the executor relies on to tell a deliberate kill-switch
// abort apart from a duration cap or a process shutdown: the cancellation CAUSE must propagate through
// the WithTimeout wrapper the executor puts around the scheduler's context. If Go ever changed this,
// the executor would misclassify a shutdown as an abort (finalise → duplicate PRs) or vice versa.
//
// Layers mirror production: schedCtx (shutdown) → rctx WithCancelCause (kill-switch) → timeoutCtx
// WithTimeout (the sweep-duration cap the executor adds).
func TestAbortCausePropagation(t *testing.T) {
	t.Run("kill-switch cause reaches the timeout child", func(t *testing.T) {
		sched, schedCancel := context.WithCancel(context.Background())
		defer schedCancel()
		rctx, cancelCause := context.WithCancelCause(sched)
		defer cancelCause(nil)
		timeoutCtx, cancelTimeout := context.WithTimeout(rctx, time.Hour)
		defer cancelTimeout()

		cancelCause(ErrRunAborted) // the kill-switch fires

		if timeoutCtx.Err() != context.Canceled {
			t.Fatalf("Err = %v, want context.Canceled", timeoutCtx.Err())
		}
		if !errors.Is(context.Cause(timeoutCtx), ErrRunAborted) {
			t.Fatalf("Cause = %v, want ErrRunAborted — executor would misread the abort", context.Cause(timeoutCtx))
		}
	})

	t.Run("shutdown is NOT seen as an abort", func(t *testing.T) {
		sched, schedCancel := context.WithCancel(context.Background())
		rctx, cancelCause := context.WithCancelCause(sched)
		defer cancelCause(nil)
		timeoutCtx, cancelTimeout := context.WithTimeout(rctx, time.Hour)
		defer cancelTimeout()

		schedCancel() // process shutdown: the parent is cancelled with no abort cause

		if errors.Is(context.Cause(timeoutCtx), ErrRunAborted) {
			t.Fatal("shutdown misread as kill-switch abort — would finalise and duplicate PRs on next fire")
		}
	})

	t.Run("duration cap is NOT seen as an abort", func(t *testing.T) {
		rctx, cancelCause := context.WithCancelCause(context.Background())
		defer cancelCause(nil)
		timeoutCtx, cancelTimeout := context.WithTimeout(rctx, time.Millisecond)
		defer cancelTimeout()

		<-timeoutCtx.Done() // the sweep-duration cap fires

		if timeoutCtx.Err() != context.DeadlineExceeded {
			t.Fatalf("Err = %v, want DeadlineExceeded", timeoutCtx.Err())
		}
		if errors.Is(context.Cause(timeoutCtx), ErrRunAborted) {
			t.Fatal("duration cap misread as kill-switch abort")
		}
	})
}
