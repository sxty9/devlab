package runs

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A nil Go slice marshals to JSON `null`, and the UI then calls .map() on null and hangs on "Lädt…".
// Empty listings must therefore be empty slices, never nil.
func TestEmptyListsMarshalAsArraysNotNull(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "res"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	res, h := NewResults(), NewHistory()

	all, _ := res.All()
	refs, _ := res.ListForRun("run_missing")
	snaps, _ := h.List()
	if all == nil || refs == nil || snaps == nil {
		t.Fatalf("nil listing: All=%v ListForRun=%v History.List=%v", all == nil, refs == nil, snaps == nil)
	}
	b, err := json.Marshal(map[string]any{"executions": all, "results": refs, "snapshots": snaps})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("empty listings marshalled with null (hangs the UI): %s", b)
	}
}

func TestScheduleNextDaily(t *testing.T) {
	s := Schedule{Kind: Daily, TimeOfDay: "03:00"}
	// before the time-of-day → same day
	after := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	got, err := s.Next(after)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("before: got %v want %v", got, want)
	}
	// after the time-of-day → next day
	after = time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	got, _ = s.Next(after)
	if want := time.Date(2026, 7, 16, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("after: got %v want %v", got, want)
	}
}

func TestScheduleNextWeekly(t *testing.T) {
	s := Schedule{Kind: Weekly, TimeOfDay: "03:00", Weekdays: []time.Weekday{time.Monday}}
	after := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	got, err := s.Next(after)
	if err != nil {
		t.Fatal(err)
	}
	if got.Weekday() != time.Monday {
		t.Errorf("weekday %v, want Monday", got.Weekday())
	}
	if !got.After(after) || got.Sub(after) > 7*24*time.Hour {
		t.Errorf("next %v not within a week after %v", got, after)
	}
	if got.Hour() != 3 || got.Minute() != 0 {
		t.Errorf("time-of-day %02d:%02d, want 03:00", got.Hour(), got.Minute())
	}
}

func TestScheduleNextForwardOnceAfterDowntime(t *testing.T) {
	// Far in the past → exactly one upcoming time (catch-up), never N missed periods.
	s := Schedule{Kind: Daily, TimeOfDay: "03:00"}
	after := time.Date(2020, 1, 1, 4, 0, 0, 0, time.UTC)
	got, _ := s.Next(after)
	if want := time.Date(2020, 1, 2, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestScheduleValid(t *testing.T) {
	bad := []Schedule{
		{Kind: Daily, TimeOfDay: "25:00"},
		{Kind: Daily, TimeOfDay: "3:00"},
		{Kind: Weekly, TimeOfDay: "03:00"}, // weekly needs weekdays
		{Kind: Daily, TimeOfDay: "03:00", Weekdays: []time.Weekday{time.Monday}},
		{Kind: "monthly", TimeOfDay: "03:00"},
	}
	for i, s := range bad {
		if err := s.Valid(); err == nil {
			t.Errorf("case %d should be invalid", i)
		}
	}
	if err := (Schedule{Kind: Daily, TimeOfDay: "03:00"}).Valid(); err != nil {
		t.Errorf("daily valid: %v", err)
	}
	if err := (Schedule{Kind: Weekly, TimeOfDay: "23:59", Weekdays: []time.Weekday{time.Sunday, time.Saturday}}).Valid(); err != nil {
		t.Errorf("weekly valid: %v", err)
	}
}

func TestStoreMutateAndHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	s := NewStore()

	if all, err := s.List(); err != nil || len(all) != 0 {
		t.Fatalf("empty store: %v len=%d", err, len(all))
	}

	r := Run{ID: "run_a", Name: "A", Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, AxiomIDs: []string{"ax_1"}}
	if _, err := s.Mutate("create", "tester", func(cur []Run) ([]Run, error) { return append(cur, r), nil }); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.Get("run_a"); !ok || got.Name != "A" {
		t.Fatalf("get after create failed: %+v ok=%v", got, ok)
	}

	snaps, err := s.History().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Action != "create" || snaps[0].RunCount != 1 {
		t.Fatalf("history after create: %+v", snaps)
	}

	if _, err := s.Mutate("update", "tester", func(cur []Run) ([]Run, error) { cur[0].Name = "B"; return cur, nil }); err != nil {
		t.Fatal(err)
	}
	snaps, _ = s.History().List()
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots after update, got %d", len(snaps))
	}
	// oldest snapshot (create) still holds the original name → restorable
	oldest := snaps[len(snaps)-1]
	full, ok, _ := s.History().Get(oldest.TS)
	if !ok || len(full.Runs) != 1 || full.Runs[0].Name != "A" {
		t.Fatalf("oldest snapshot content: ok=%v %+v", ok, full.Runs)
	}

	if _, err := s.Mutate("delete", "tester", func(cur []Run) ([]Run, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.List(); len(all) != 0 {
		t.Fatalf("after delete want 0, got %d", len(all))
	}
}

func TestMutateAbortsOnClosureError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	s := NewStore()
	if _, err := s.Mutate("create", "t", func(cur []Run) ([]Run, error) {
		return append(cur, Run{ID: "run_a", Name: "A", Schedule: Schedule{Kind: Daily, TimeOfDay: "03:00"}, AxiomIDs: []string{"x"}}), nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.History().List()
	// A closure that errors (e.g. unknown id → ErrNotFound) must NOT save or append a snapshot.
	if _, err := s.Mutate("noop", "t", func(cur []Run) ([]Run, error) { return nil, ErrNotFound }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	after, _ := s.History().List()
	if len(after) != len(before) {
		t.Errorf("errored mutation appended a snapshot: before=%d after=%d", len(before), len(after))
	}
}

func TestHistoryGetRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	h := NewHistory()
	for _, ts := range []string{"../../etc/passwd", "../x", "a/b", "../"} {
		if _, ok, _ := h.Get(ts); ok {
			t.Errorf("traversal ts %q resolved to a file", ts)
		}
	}
}
