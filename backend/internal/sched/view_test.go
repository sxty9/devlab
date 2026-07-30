package sched

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"devlab/backend/internal/model"
)

// Every repo of every execution leaves the projection with a stage LIST, even when it has no stage
// yet. A Go nil slice marshals to `null`, the surface declares the field as an array, and the first
// consumer that iterates it throws — one queued execution took the entire active view down that
// way ("This view could not be displayed"). A queued execution is exactly the shape with no stage.
func TestAQueuedExecutionCarriesAnEmptyStageListNeverNull(t *testing.T) {
	h := newHarness(t, Config{})
	h.addTodo("run_a", "A", "alpha")
	h.addTodo("run_b", "B", "alpha") // same repository — the second one queues behind the first

	outA := h.submit("run_a", nil)
	if !outA.Started {
		t.Fatalf("the first must start: %+v", outA)
	}
	// The second targets the SAME repository and therefore waits — the live shape that broke the
	// view was exactly this: "target repository busy".
	outB := h.submit("run_b", &Placement{Kind: PlacementQueue})
	if !outB.Queued {
		t.Fatalf("the second must queue behind the busy repository: %+v", outB)
	}
	h.sch.pass(context.Background(), false)

	list, err := h.sch.ActiveList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("the active answer lists both, got %d", len(list))
	}
	queued := 0
	for _, v := range list {
		for _, rp := range v.Repos {
			if rp.Stages == nil {
				t.Fatalf("execution %s / repo %s left the projection with an ABSENT stage list", v.ID, rp.Repo)
			}
		}
		if v.Phase == model.PhaseQueued {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("want exactly one queued execution, got %d", queued)
	}

	// And on the wire: the field is an empty array, never null.
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"stages":null`) {
		t.Fatalf("the wire carries a null stage list: %s", b)
	}

	h.exec.releaseAll()
}
