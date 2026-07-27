package api

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newTestRollout builds a worker with a tiny debounce and a fake push seam (doFn), so the debounce /
// bundling behaviour is exercised with no GitHub token or network.
func newTestRollout(doFn func(ctx context.Context, block string) RolloutReport) *autoRollout {
	return &autoRollout{debounce: 15 * time.Millisecond, baseCtx: context.Background(), doFn: doFn}
}

// A burst of writes within the quiet period collapses into exactly ONE rollout, of the NEWEST block.
func TestAutoRolloutBurstCollapsesToOne(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	a := newTestRollout(func(_ context.Context, block string) RolloutReport {
		mu.Lock()
		calls = append(calls, block)
		mu.Unlock()
		return RolloutReport{}
	})

	a.enqueue("v1")
	a.enqueue("v2")
	a.enqueue("v3")

	time.Sleep(80 * time.Millisecond) // past the debounce

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("a burst must collapse into one rollout, got %d: %v", len(calls), calls)
	}
	if calls[0] != "v3" {
		t.Fatalf("the rollout must use the newest block, got %q", calls[0])
	}
}

// A write that lands WHILE a rollout is in flight is not lost: the worker re-arms and rolls the newer
// block out afterwards — never overlapping two rollouts.
func TestAutoRolloutRearmsMidRollout(t *testing.T) {
	release := make(chan struct{})
	started := make(chan string, 4)
	var mu sync.Mutex
	var calls []string
	a := newTestRollout(func(_ context.Context, block string) RolloutReport {
		started <- block
		mu.Lock()
		calls = append(calls, block)
		mu.Unlock()
		<-release // hold the rollout open until the test releases it
		return RolloutReport{}
	})

	a.enqueue("v1")
	if got := <-started; got != "v1" { // first rollout is now in flight (blocked)
		t.Fatalf("first rollout block = %q, want v1", got)
	}
	a.enqueue("v2") // a write arrives mid-rollout
	close(release)  // let both rollouts proceed

	select {
	case got := <-started:
		if got != "v2" {
			t.Fatalf("second rollout block = %q, want v2", got)
		}
	case <-time.After(time.Second):
		t.Fatal("a write during a rollout must trigger a follow-up rollout")
	}

	// Give the worker a moment to ensure it does NOT run a third time.
	time.Sleep(60 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "v1" || calls[1] != "v2" {
		t.Fatalf("want exactly [v1 v2], got %v", calls)
	}
}

// The last rollout's report is retained and handed back by report() (nil before the first rollout).
func TestAutoRolloutReportStored(t *testing.T) {
	a := newTestRollout(func(_ context.Context, block string) RolloutReport {
		return RolloutReport{Repos: 3, Changed: []string{"devlab"}, Unchanged: 2}
	})
	if a.report() != nil {
		t.Fatal("report must be nil before the first rollout")
	}
	a.enqueue("block")
	time.Sleep(80 * time.Millisecond)

	rep := a.report()
	if rep == nil {
		t.Fatal("report must be set after a rollout")
	}
	if rep.Repos != 3 || len(rep.Changed) != 1 || rep.Changed[0] != "devlab" || rep.Unchanged != 2 {
		t.Fatalf("report not stored faithfully: %+v", rep)
	}
}
