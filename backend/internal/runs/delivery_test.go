package runs

import (
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/statepath"
)

// The delivery ledger is a passive, append-only pool: Put upserts by id, All lists in stack
// order, Open filters to a repo's not-merged/not-closed deliveries, OpenForExecution finds the
// newest open EXECUTION delivery (the resume probe), and ByID addresses one record.
func TestDeliveryLedgerPool(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_RUNS_DELIVERIES", filepath.Join(t.TempDir(), "deliveries.json"))
	s := NewDeliveryStore(&statepath.Paths{Root: t.TempDir()})

	t0 := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	merged := t0.Add(2 * time.Hour)
	d1 := Delivery{ID: "dlv_1", Repo: "svc-a", Branch: "fix/one", FromCommit: "a1", ToCommit: "a2", CreatedAt: t0, ExecutionID: "exec_1"}
	d2 := Delivery{ID: "dlv_2", Repo: "svc-a", Branch: "fix/two", FromCommit: "a2", ToCommit: "a3", CreatedAt: t0.Add(time.Hour), ExecutionID: "exec_2"}
	d3 := Delivery{ID: "dlv_3", Repo: "svc-b", Branch: "fix/three", FromCommit: "b1", ToCommit: "b2", CreatedAt: t0, MergedAt: &merged}

	for _, d := range []Delivery{d1, d2, d3} {
		if err := s.Put(d); err != nil {
			t.Fatalf("Put(%s): %v", d.ID, err)
		}
	}

	all, err := s.All()
	if err != nil || len(all) != 3 {
		t.Fatalf("All = %d, %v; want 3", len(all), err)
	}

	// Open: only svc-a's two open deliveries, in stack (creation) order.
	open, err := s.Open("svc-a")
	if err != nil || len(open) != 2 || open[0].ID != "dlv_1" || open[1].ID != "dlv_2" {
		t.Fatalf("Open(svc-a) = %+v, %v", open, err)
	}
	// A merged delivery is not open.
	if openB, _ := s.Open("svc-b"); len(openB) != 0 {
		t.Fatalf("Open(svc-b) = %+v, want empty (merged)", openB)
	}

	// OpenForExecution: the NEWEST open execution delivery.
	got, err := s.OpenForExecution("svc-a")
	if err != nil || got == nil || got.ID != "dlv_2" {
		t.Fatalf("OpenForExecution = %+v, %v; want dlv_2", got, err)
	}

	// ByID addresses one record.
	one, ok, err := s.ByID("dlv_3")
	if err != nil || !ok || one.Repo != "svc-b" || one.MergedAt == nil {
		t.Fatalf("ByID(dlv_3) = %+v %v %v", one, ok, err)
	}

	// Put upserts: closing dlv_1 with a reason takes it out of the open set — the record stays
	// (a delivery is never destroyed).
	closed := d1
	closedAt := merged
	closed.ClosedAt = &closedAt
	closed.ClosedReason = "rolled back"
	if err := s.Put(closed); err != nil {
		t.Fatal(err)
	}
	open, _ = s.Open("svc-a")
	if len(open) != 1 || open[0].ID != "dlv_2" {
		t.Fatalf("after close, Open(svc-a) = %+v, want only dlv_2", open)
	}
	if all, _ := s.All(); len(all) != 3 {
		t.Fatal("a closed delivery must remain in the ledger")
	}
}
