package runs

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// A nil Go slice marshals to JSON `null`, and the UI then calls .map() on null and blanks the
// list. Empty listings must therefore be empty slices, never nil.
func TestEmptyListsMarshalAsArraysNotNull(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "legacy"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	res, h := NewResultStore(&statepath.Paths{Root: dir}), NewHistory(nil)

	all, _ := res.List()
	refs, _ := res.ForRun("run_missing")
	snaps, _ := h.List()
	if all == nil || refs == nil || snaps == nil {
		t.Fatalf("nil listing: List=%v ForRun=%v History.List=%v", all == nil, refs == nil, snaps == nil)
	}
	b, err := json.Marshal(map[string]any{"executions": all, "results": refs, "snapshots": snaps})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("empty listings marshalled with null (blanks the UI): %s", b)
	}
}

// A stored result carries its run's kind so the execution history can be split per surface
// even after the run is deleted. List must surface that stamp on every document.
func TestListCarriesKindStamp(t *testing.T) {
	res := NewResultStore(&statepath.Paths{Root: t.TempDir()})
	if err := res.Put(Result{ID: NewResultID(time.Now()), RunID: "run_a", Kind: model.KindTodo, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := res.Put(Result{ID: NewResultID(time.Now().Add(time.Second)), RunID: "run_b", Kind: model.KindAuto, StartedAt: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	all, err := res.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]model.RunKind{}
	for _, e := range all {
		got[e.RunID] = e.Kind
	}
	if got["run_a"] != model.KindTodo || got["run_b"] != model.KindAuto {
		t.Fatalf("kind stamp not surfaced by List(): %+v", got)
	}
}

func TestScheduleNextDaily(t *testing.T) {
	s := ScheduleSpec{Kind: Daily, TimeOfDay: "03:00"}
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
	// NextFire wraps the same rule (the ONE place fire times come from).
	if nf := NextFire(s, after); !nf.Equal(time.Date(2026, 7, 16, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("NextFire: got %v", nf)
	}
	if nf := NextFire(ScheduleSpec{Kind: "bogus"}, after); !nf.IsZero() {
		t.Errorf("NextFire on an invalid spec must be zero, got %v", nf)
	}
}

func TestScheduleNextWeekly(t *testing.T) {
	s := ScheduleSpec{Kind: Weekly, TimeOfDay: "03:00", Weekdays: []time.Weekday{time.Monday}}
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
	s := ScheduleSpec{Kind: Daily, TimeOfDay: "03:00"}
	after := time.Date(2020, 1, 1, 4, 0, 0, 0, time.UTC)
	got, _ := s.Next(after)
	if want := time.Date(2020, 1, 2, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestScheduleValid(t *testing.T) {
	bad := []ScheduleSpec{
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
	if err := (ScheduleSpec{Kind: Daily, TimeOfDay: "03:00"}).Valid(); err != nil {
		t.Errorf("daily valid: %v", err)
	}
	if err := (ScheduleSpec{Kind: Weekly, TimeOfDay: "23:59", Weekdays: []time.Weekday{time.Sunday, time.Saturday}}).Valid(); err != nil {
		t.Errorf("weekly valid: %v", err)
	}
}

// Put/Get/Delete are the store's write surface; every user-meaningful mutation snapshots the
// history so any past constellation stays restorable.
func TestStorePutDeleteAndHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	s := NewStore(nil)

	if all, err := s.List(); err != nil || len(all) != 0 {
		t.Fatalf("empty store: %v len=%d", err, len(all))
	}

	r := Run{ID: "run_a", Kind: model.KindAuto, Title: "A",
		Schedule: &ScheduleSpec{Kind: Daily, TimeOfDay: "03:00"}, AxiomIDs: []string{"ax_1"},
		Authorship: model.Authorship{Created: model.Actor{User: "tester"}, Updated: model.Actor{User: "tester"}}}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.Get("run_a"); !ok || got.Title != "A" {
		t.Fatalf("get after create failed: %+v ok=%v", got, ok)
	}

	snaps, err := s.History().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].Action != "create" || snaps[0].RunCount != 1 {
		t.Fatalf("history after create: %+v", snaps)
	}

	r.Title = "B"
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	snaps, _ = s.History().List()
	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots after update, got %d", len(snaps))
	}
	// oldest snapshot (create) still holds the original title → restorable
	oldest := snaps[len(snaps)-1]
	full, ok, _ := s.History().Get(oldest.TS)
	if !ok || len(full.Runs) != 1 || full.Runs[0].Title != "A" {
		t.Fatalf("oldest snapshot content: ok=%v %+v", ok, full.Runs)
	}

	if err := s.Delete("run_a"); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.List(); len(all) != 0 {
		t.Fatalf("after delete want 0, got %d", len(all))
	}
	// Deleting an unknown id aborts before any write — no spurious snapshot.
	before, _ := s.History().List()
	if err := s.Delete("run_ghost"); err == nil {
		t.Fatal("deleting an unknown id must error")
	}
	after, _ := s.History().List()
	if len(after) != len(before) {
		t.Fatalf("a failed delete must not append a snapshot: %d → %d", len(before), len(after))
	}
}

// An autonomous mutation is recorded AS autonomous in the config history (REQ-041.3): the
// snapshot's actor names the system, never a person it could be misattributed to.
func TestAutonomousActorRecordedAsSuch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	s := NewStore(nil)
	r := Run{ID: "run_a", Kind: model.KindAuto, Title: "A",
		Authorship: model.Authorship{Created: model.Actor{Autonomous: true}, Updated: model.Actor{Autonomous: true}}}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	snaps, err := s.History().List()
	if err != nil || len(snaps) != 1 {
		t.Fatalf("history: %v len=%d", err, len(snaps))
	}
	if snaps[0].Actor != "autonomous" {
		t.Fatalf("autonomous mutation recorded as %q, want %q", snaps[0].Actor, "autonomous")
	}
}

// Patch is the runtime-state write path: it saves but never snapshots the history.
func TestPatchDoesNotSnapshotHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	s := NewStore(nil)
	if err := s.Put(Run{ID: "run_a", Kind: model.KindAuto, Title: "A"}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.History().List()
	if _, err := s.Patch(func(cur []Run) ([]Run, error) {
		cur[0].PromptSnapshot = "recomposed"
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.History().List()
	if len(after) != len(before) {
		t.Fatalf("Patch must not snapshot: %d → %d", len(before), len(after))
	}
	if got, _, _ := s.Get("run_a"); got.PromptSnapshot != "recomposed" {
		t.Fatalf("Patch must persist: %+v", got)
	}
}
