package runs

import (
	"path/filepath"
	"testing"
	"time"
)

func tempDeliveryStore(t *testing.T) *DeliveryStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_DELIVERIES", filepath.Join(t.TempDir(), "deliveries.json"))
	return NewDeliveryStore()
}

// mkDelivery is a terse builder for ledger fixtures with a deterministic CreatedAt (t0 + n minutes).
func mkDelivery(id, repo string, n int, status DeliveryStatus) Delivery {
	t0 := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	return Delivery{
		ID: id, Repo: repo, Branch: "mercury-run/" + id, Status: status,
		CreatedAt: t0.Add(time.Duration(n) * time.Minute),
	}
}

// TestDeliveryStoreRoundTrip pins the passive-pool basics: add, dedupe, get, and oldest-first listing.
func TestDeliveryStoreRoundTrip(t *testing.T) {
	s := tempDeliveryStore(t)
	if err := s.Add(mkDelivery("dlv_b", "o/x", 2, DeliveryOpen)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(mkDelivery("dlv_a", "o/x", 1, DeliveryOpen)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(mkDelivery("dlv_a", "o/x", 1, DeliveryMerged)); err != nil { // duplicate id → ignored
		t.Fatal(err)
	}

	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 deliveries (dupe ignored), got %d", len(all))
	}
	if all[0].ID != "dlv_a" || all[1].ID != "dlv_b" {
		t.Errorf("want oldest-first [dlv_a dlv_b], got [%s %s]", all[0].ID, all[1].ID)
	}
	if d, ok, _ := s.Get("dlv_a"); !ok || d.Status != DeliveryOpen {
		t.Errorf("Get(dlv_a) = %+v ok=%v — dedupe must keep the first write", d, ok)
	}
}

// TestDeliveryLatestOpen is the stacked-PR base rule (req 9): the base is the most recent OPEN delivery,
// skipping merged/closed/reverted ones; none open → caller falls back to the default branch.
func TestDeliveryLatestOpen(t *testing.T) {
	s := tempDeliveryStore(t)
	for _, d := range []Delivery{
		mkDelivery("d1", "o/x", 1, DeliveryMerged),
		mkDelivery("d2", "o/x", 2, DeliveryOpen),
		mkDelivery("d3", "o/x", 3, DeliveryReverted),
		mkDelivery("d4", "o/x", 4, DeliveryOpen),
		mkDelivery("other", "o/y", 5, DeliveryOpen), // different repo — must not leak in
	} {
		if err := s.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.LatestOpen("o/x")
	if err != nil || !ok {
		t.Fatalf("LatestOpen ok=%v err=%v", ok, err)
	}
	if got.ID != "d4" {
		t.Errorf("LatestOpen = %s, want d4 (most recent open)", got.ID)
	}

	// A repo with no open delivery → not found (base = default branch).
	empty := tempDeliveryStore(t)
	_ = empty.Add(mkDelivery("m", "o/z", 1, DeliveryMerged))
	if _, ok, _ := empty.LatestOpen("o/z"); ok {
		t.Errorf("LatestOpen must be false when nothing is open")
	}
}

// TestDeliveryLaterOpenDeliveries pins what a rollback consults to detect stacked follow-up work.
func TestDeliveryLaterOpenDeliveries(t *testing.T) {
	s := tempDeliveryStore(t)
	for _, d := range []Delivery{
		mkDelivery("d1", "o/x", 1, DeliveryOpen),
		mkDelivery("d2", "o/x", 2, DeliveryOpen),
		mkDelivery("d3", "o/x", 3, DeliveryMerged), // later but merged — not stacked-open follow-up
		mkDelivery("d4", "o/x", 4, DeliveryOpen),
	} {
		if err := s.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	later, err := s.LaterOpenDeliveries("o/x", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(later) != 2 || later[0].ID != "d2" || later[1].ID != "d4" {
		t.Errorf("LaterOpenDeliveries(d1) = %v, want [d2 d4]", ids(later))
	}
	if none, _ := s.LaterOpenDeliveries("o/x", "d4"); len(none) != 0 {
		t.Errorf("nothing stacks on the newest delivery, got %v", ids(none))
	}
}

// TestDeliverySetStatusByPR pins the Maintain mirror: a merged/closed PR flips its delivery's status, but
// a reverted delivery is authoritative and stays reverted.
func TestDeliverySetStatusByPR(t *testing.T) {
	s := tempDeliveryStore(t)
	d := mkDelivery("d1", "o/x", 1, DeliveryOpen)
	d.PRNumber = 42
	if err := s.Add(d); err != nil {
		t.Fatal(err)
	}
	if hit, ok, err := s.SetStatusByPR("o/x", 42, DeliveryMerged); err != nil || !ok || hit.Status != DeliveryMerged {
		t.Fatalf("SetStatusByPR merged: hit=%+v ok=%v err=%v", hit, ok, err)
	}
	if got, _, _ := s.Get("d1"); got.Status != DeliveryMerged {
		t.Errorf("status = %s, want merged", got.Status)
	}
	// A reverted delivery must not be flipped back by a late PR event.
	_ = s.Update("d1", func(dv *Delivery) { dv.Status = DeliveryReverted })
	if _, _, err := s.SetStatusByPR("o/x", 42, DeliveryClosed); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get("d1"); got.Status != DeliveryReverted {
		t.Errorf("reverted delivery must stay reverted, got %s", got.Status)
	}
	// An unknown PR number is a silent no-op.
	if _, ok, _ := s.SetStatusByPR("o/x", 999, DeliveryMerged); ok {
		t.Errorf("unknown PR must report not-found")
	}
}

// TestDeliveryOpenForExecution pins the duplicate-work guard (req 5): the OPEN delivery a specific
// execution already made for a repo is found by its exact (runId, resultId), while a delivery from a
// different execution or a merged/closed one is never mistaken for it.
func TestDeliveryOpenForExecution(t *testing.T) {
	s := tempDeliveryStore(t)
	mk := func(id, run, result, repo string, n int, status DeliveryStatus) Delivery {
		d := mkDelivery(id, repo, n, status)
		d.RunID, d.ResultID, d.PRUrl = run, result, "https://gh/"+id
		return d
	}
	for _, d := range []Delivery{
		mk("d1", "run_a", "res_1", "o/x", 1, DeliveryOpen),   // the one to adopt
		mk("d2", "run_a", "res_2", "o/x", 2, DeliveryOpen),   // same run, DIFFERENT execution — not it
		mk("d3", "run_b", "res_1", "o/x", 3, DeliveryOpen),   // same result id, DIFFERENT run — not it
		mk("d4", "run_a", "res_1", "o/y", 4, DeliveryOpen),   // right ids, DIFFERENT repo — not it
		mk("d5", "run_a", "res_9", "o/x", 5, DeliveryMerged), // merged — never adopted
	} {
		if err := s.Add(d); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.OpenForExecution("run_a", "res_1", "o/x")
	if err != nil || !ok {
		t.Fatalf("OpenForExecution ok=%v err=%v — expected to find d1", ok, err)
	}
	if got.ID != "d1" {
		t.Errorf("OpenForExecution = %s, want d1", got.ID)
	}
	// A merged delivery of the same execution is not an open PR to adopt.
	if _, ok, _ := s.OpenForExecution("run_a", "res_9", "o/x"); ok {
		t.Error("OpenForExecution must ignore a merged delivery")
	}
	// An execution that delivered nothing to this repo → nothing to adopt.
	if _, ok, _ := s.OpenForExecution("run_a", "res_absent", "o/x"); ok {
		t.Error("OpenForExecution must be false when the execution made no open delivery here")
	}
}

func ids(ds []Delivery) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}
	return out
}
