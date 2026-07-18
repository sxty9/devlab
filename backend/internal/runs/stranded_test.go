package runs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFindStranded pins the crash/carry-over resume detector: a run killed or carried over mid-sweep
// (unfinished, unsuspended, worked recently) must be resumable so the next fire skips already-done
// repos instead of duplicating every PR — while finished, suspended, and abandoned (untouched past the
// window) results must NOT be picked up. Recency is measured by UpdatedAt (last worked), not StartedAt.
func TestFindStranded(t *testing.T) {
	dir := t.TempDir()
	resDir := filepath.Join(dir, "res")
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", resDir)
	res := NewResults()

	now := time.Now().UTC()
	// writeRaw persists a result with FULL control over its timestamps (Save() would overwrite
	// UpdatedAt, defeating the aging test), so we marshal and drop the file directly.
	writeRaw := func(runID, id string, started, updated, finished time.Time, suspended bool) {
		r := Result{RunID: runID, ResultID: id, StartedAt: started, UpdatedAt: updated,
			FinishedAt: finished, Suspended: suspended, Repos: []RepoResult{}}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		d := filepath.Join(resDir, runID)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, id+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	zero := time.Time{}

	// finished — ignored
	writeRaw("run_x", "a", now.Add(-30*time.Minute), now.Add(-25*time.Minute), now.Add(-25*time.Minute), false)
	// suspended (usage limit, live) — ignored (the run.Suspended pointer path owns it)
	writeRaw("run_x", "b", now.Add(-20*time.Minute), now.Add(-15*time.Minute), zero, true)
	// abandoned — started long ago AND not touched within the window — ignored
	writeRaw("run_x", "c", now.Add(-48*time.Hour), now.Add(-48*time.Hour), zero, false)
	// genuinely stranded (crash / carry-over), worked recently — THIS is the one to resume
	writeRaw("run_x", "d", now.Add(-10*time.Minute), now.Add(-2*time.Minute), zero, false)

	got, ok := res.FindStranded("run_x", now.Add(-12*time.Hour))
	if !ok {
		t.Fatal("FindStranded found nothing; expected the stranded result 'd'")
	}
	if got.ResultID != "d" {
		t.Errorf("FindStranded = %q, want \"d\"", got.ResultID)
	}

	// A multi-night carry-over: started 9 days ago but touched last hour → still eligible (the frozen
	// StartedAt would have aged it out; UpdatedAt keeps it alive because it is actively worked).
	writeRaw("run_c", "spanning", now.Add(-9*24*time.Hour), now.Add(-1*time.Hour), zero, false)
	if got, ok := res.FindStranded("run_c", now.Add(-10*24*time.Hour)); !ok || got.ResultID != "spanning" {
		t.Errorf("multi-night carry-over aged out on StartedAt: got %q ok=%v", got.ResultID, ok)
	}

	// Only finished present → nothing resumable.
	writeRaw("run_y", "fin", now.Add(-5*time.Minute), now.Add(-1*time.Minute), now, false)
	if _, ok := res.FindStranded("run_y", now.Add(-12*time.Hour)); ok {
		t.Error("FindStranded resumed a finished result")
	}

	// Newest-wins when two are stranded.
	writeRaw("run_z", "older", now.Add(-2*time.Hour), now.Add(-90*time.Minute), zero, false)
	writeRaw("run_z", "newer", now.Add(-1*time.Hour), now.Add(-30*time.Minute), zero, false)
	if got, ok := res.FindStranded("run_z", now.Add(-12*time.Hour)); !ok || got.ResultID != "newer" {
		t.Errorf("FindStranded newest-wins: got %q ok=%v, want \"newer\"", got.ResultID, ok)
	}
}
