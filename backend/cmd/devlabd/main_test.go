package main

// The one background pass with an effect OUTSIDE DevLab is the protection check: it walks every
// repository of the configured organisation and, armed, PATCHes the ones that deviate. It used to run
// the moment the process came up — before an operator could see the daemon start, and on a boot loop
// once per restart. These tests hold the loop to the shape that makes a boot harmless: it waits first,
// and a stop inside the waiting window means no pass ever ran.

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// The readiness socket is the interlock devlab-migrate takes: a live daemon answers 423 and the
// migration refuses (exit 10), while a daemon whose socket never came up reads as dead and lets the
// migration work a LIVE state tree. So the boot waits for the gate to answer and names every way it
// can fail to — it never continues on the assumption that the gate is probably up.
func TestAwaitGateWaitsForAnAnsweringSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "ready.sock")
	// The gate comes up a moment after the boot asks for it — the normal case.
	go func() {
		time.Sleep(60 * time.Millisecond)
		l, err := net.Listen("unix", sock)
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = l.Close() })
	}()
	if err := awaitGate(context.Background(), sock, make(chan error), 3*time.Second); err != nil {
		t.Fatalf("a gate that comes up must be awaited, got %v", err)
	}
}

func TestAwaitGateRefusesWhenTheSocketCannotComeUp(t *testing.T) {
	failed := make(chan error, 1)
	boom := errors.New("bind: permission denied")
	failed <- boom
	// No socket will ever exist here; the attempt reported its own failure.
	err := awaitGate(context.Background(), filepath.Join(t.TempDir(), "ready.sock"), failed, 3*time.Second)
	if !errors.Is(err, boom) {
		t.Fatalf("the failure to bring the gate up must be handed back verbatim, got %v", err)
	}
}

func TestAwaitGateRefusesOnSilence(t *testing.T) {
	start := time.Now()
	err := awaitGate(context.Background(), filepath.Join(t.TempDir(), "ready.sock"), make(chan error), 120*time.Millisecond)
	if err == nil {
		t.Fatal("a gate that never answers must refuse the start — a silent pass is the old behaviour under a new name")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("the wait is unbounded (%s) — a boot must not hang on a missing gate", took)
	}
}

// The first pass waits for the start delay — a boot alone changes nothing on GitHub.
func TestProtectionLoopDoesNotRunAtBoot(t *testing.T) {
	var passes int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		protectionLoop(ctx, 150*time.Millisecond, time.Hour, func(context.Context) error {
			atomic.AddInt64(&passes, 1)
			return nil
		})
	}()

	time.Sleep(40 * time.Millisecond)
	if n := atomic.LoadInt64(&passes); n != 0 {
		t.Fatalf("the protection check ran %d time(s) inside the start delay — a boot must reach no repository", n)
	}

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&passes) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the protection check never ran after the start delay — the pass must still happen, only later")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

// A daemon stopped inside the window performs NO pass at all: the cutover's "start it and watch it"
// costs nothing, and a restart loop cannot turn into a write loop.
func TestProtectionLoopCancelledInsideTheDelayNeverRuns(t *testing.T) {
	var passes int64
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		protectionLoop(ctx, 5*time.Second, time.Hour, func(context.Context) error {
			atomic.AddInt64(&passes, 1)
			return nil
		})
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not return on cancellation inside the start delay")
	}
	if n := atomic.LoadInt64(&passes); n != 0 {
		t.Fatalf("a daemon stopped inside the start delay must have performed no pass, got %d", n)
	}
}

// The delay is operator-tunable and defaults to a real waiting period — a zero default would be the
// old behaviour under a new name.
func TestProtectionStartDelayDefaultsToAWaitingPeriod(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_PROTECTION_START_DELAY", "")
	if d := envDuration("DEVLAB_RUNS_PROTECTION_START_DELAY", 15*time.Minute); d < time.Minute {
		t.Fatalf("the default start delay is %s — too short to see a daemon come up before it writes", d)
	}
	t.Setenv("DEVLAB_RUNS_PROTECTION_START_DELAY", "0s")
	if d := envDuration("DEVLAB_RUNS_PROTECTION_START_DELAY", 15*time.Minute); d != 0 {
		t.Fatalf("an operator must be able to switch the delay off deliberately, got %s", d)
	}
}
