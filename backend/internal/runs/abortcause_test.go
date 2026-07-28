package runs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// errExecutionAborted stands in for the executor's kill-switch cancellation cause (the sentinel
// the S8 chain cancels a targeted execution with). The invariant pinned here is about the
// stdlib, not the sentinel's home: context.Cause must survive the WithTimeout wrapper.
var errExecutionAborted = errors.New("execution aborted by user")

// TestAbortCausePropagation pins the invariant the executor relies on to tell a deliberate
// targeted abort (REQ-013.5) apart from a time-budget cap or a process shutdown: the
// cancellation CAUSE must propagate through the WithTimeout wrapper the executor puts around
// the scheduler's context. If Go ever changed this, the executor would misclassify a shutdown
// as an abort (finalize → duplicate PRs) or an abort as a budget overrun (dishonest report).
//
// Layers mirror production: schedCtx (shutdown) → rctx WithCancelCause (targeted abort) →
// timeoutCtx WithTimeout (the per-execution time budget, REQ-010).
func TestAbortCausePropagation(t *testing.T) {
	t.Run("targeted abort cause reaches the timeout child", func(t *testing.T) {
		sched, schedCancel := context.WithCancel(context.Background())
		defer schedCancel()
		rctx, cancelCause := context.WithCancelCause(sched)
		defer cancelCause(nil)
		timeoutCtx, cancelTimeout := context.WithTimeout(rctx, time.Hour)
		defer cancelTimeout()

		cancelCause(errExecutionAborted) // the targeted abort fires

		if timeoutCtx.Err() != context.Canceled {
			t.Fatalf("Err = %v, want context.Canceled", timeoutCtx.Err())
		}
		if !errors.Is(context.Cause(timeoutCtx), errExecutionAborted) {
			t.Fatalf("Cause = %v, want the abort sentinel — the executor would misread the abort", context.Cause(timeoutCtx))
		}
	})

	t.Run("shutdown is NOT seen as an abort", func(t *testing.T) {
		sched, schedCancel := context.WithCancel(context.Background())
		rctx, cancelCause := context.WithCancelCause(sched)
		defer cancelCause(nil)
		timeoutCtx, cancelTimeout := context.WithTimeout(rctx, time.Hour)
		defer cancelTimeout()

		schedCancel() // process shutdown: the parent is cancelled with no abort cause

		if errors.Is(context.Cause(timeoutCtx), errExecutionAborted) {
			t.Fatal("shutdown misread as targeted abort — would finalize instead of persisting interrupted")
		}
	})

	t.Run("time-budget cap is NOT seen as an abort", func(t *testing.T) {
		rctx, cancelCause := context.WithCancelCause(context.Background())
		defer cancelCause(nil)
		timeoutCtx, cancelTimeout := context.WithTimeout(rctx, time.Millisecond)
		defer cancelTimeout()

		<-timeoutCtx.Done() // the budget cap fires

		if timeoutCtx.Err() != context.DeadlineExceeded {
			t.Fatalf("Err = %v, want DeadlineExceeded", timeoutCtx.Err())
		}
		if errors.Is(context.Cause(timeoutCtx), errExecutionAborted) {
			t.Fatal("budget cap misread as targeted abort — the overrun must be reported as the budget, honestly (REQ-010.4)")
		}
	})
}
