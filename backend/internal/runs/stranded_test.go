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

// TestFindStaleHusk pins the reap detector: only an unfinished, unsuspended husk untouched past the
// cutoff is returned — the crash-orphaned "Leiche" the executor finalizes so it stops lingering and the
// run becomes restartable. A recently-worked husk (an in-flight resume), a finished result and a live
// suspension are all left alone.
func TestFindStaleHusk(t *testing.T) {
	dir := t.TempDir()
	resDir := filepath.Join(dir, "res")
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", resDir)
	res := NewResults()
	now := time.Now().UTC()
	zero := time.Time{}

	writeRaw := func(runID, id string, started, updated, finished time.Time, suspended bool) {
		r := Result{RunID: runID, ResultID: id, StartedAt: started, UpdatedAt: updated,
			FinishedAt: finished, Suspended: suspended, Repos: []RepoResult{}}
		b, _ := json.Marshal(r)
		d := filepath.Join(resDir, runID)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, id+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := now.Add(-15 * time.Minute)

	// A crash husk untouched for an hour → stale, reapable.
	writeRaw("run_stale", "h", now.Add(-2*time.Hour), now.Add(-time.Hour), zero, false)
	if got, ok := res.FindStaleHusk("run_stale", cutoff); !ok || got.ResultID != "h" {
		t.Errorf("FindStaleHusk missed an abandoned husk: got %q ok=%v", got.ResultID, ok)
	}
	// A husk worked a minute ago (a live carry-over) → within the grace, NOT reaped.
	writeRaw("run_fresh", "h", now.Add(-30*time.Minute), now.Add(-time.Minute), zero, false)
	if _, ok := res.FindStaleHusk("run_fresh", cutoff); ok {
		t.Error("FindStaleHusk reaped a husk still being worked within the grace")
	}
	// A finished result → never a husk.
	writeRaw("run_done", "h", now.Add(-2*time.Hour), now.Add(-90*time.Minute), now.Add(-90*time.Minute), false)
	if _, ok := res.FindStaleHusk("run_done", cutoff); ok {
		t.Error("FindStaleHusk reaped a finished result")
	}
	// A suspended husk (usage limit) resumes itself → not reaped here.
	writeRaw("run_susp", "h", now.Add(-2*time.Hour), now.Add(-time.Hour), zero, true)
	if _, ok := res.FindStaleHusk("run_susp", cutoff); ok {
		t.Error("FindStaleHusk reaped a suspended husk")
	}
}

// TestFindResumableIncludesOrphanedSuspension pins the freeze-bug fix: an execution suspended on the
// usage limit whose run lost its Suspended pointer must still be resumable (FindResumable), so it is
// continued on the next fire instead of freezing forever with its spend stranded — while FindStranded
// keeps ignoring suspended husks (that path owns only crash/carry-over husks).
func TestFindResumableIncludesOrphanedSuspension(t *testing.T) {
	dir := t.TempDir()
	resDir := filepath.Join(dir, "res")
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", resDir)
	res := NewResults()
	now := time.Now().UTC()
	zero := time.Time{}

	writeRaw := func(runID, id string, started, updated, finished time.Time, suspended bool) {
		r := Result{RunID: runID, ResultID: id, StartedAt: started, UpdatedAt: updated,
			FinishedAt: finished, Suspended: suspended, Repos: []RepoResult{}}
		b, _ := json.Marshal(r)
		d := filepath.Join(resDir, runID)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, id+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A lone suspended husk, worked recently: FindStranded ignores it, FindResumable recovers it.
	writeRaw("run_s", "susp", now.Add(-30*time.Minute), now.Add(-10*time.Minute), zero, true)
	if _, ok := res.FindStranded("run_s", now.Add(-12*time.Hour)); ok {
		t.Error("FindStranded must NOT pick up a suspended husk")
	}
	if got, ok := res.FindResumable("run_s", now.Add(-12*time.Hour)); !ok || got.ResultID != "susp" {
		t.Errorf("FindResumable must recover an orphaned suspension: got %q ok=%v", got.ResultID, ok)
	}

	// Still bounded by the window: an abandoned suspended husk ages out.
	writeRaw("run_old", "stale", now.Add(-48*time.Hour), now.Add(-48*time.Hour), zero, true)
	if _, ok := res.FindResumable("run_old", now.Add(-12*time.Hour)); ok {
		t.Error("FindResumable must age out a suspended husk untouched past the window")
	}

	// A finished result is never resumable, suspended flag notwithstanding.
	writeRaw("run_fin", "done", now.Add(-20*time.Minute), now.Add(-5*time.Minute), now, true)
	if _, ok := res.FindResumable("run_fin", now.Add(-12*time.Hour)); ok {
		t.Error("FindResumable must ignore a finished result")
	}
}
