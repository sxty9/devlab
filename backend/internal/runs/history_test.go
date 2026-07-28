package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSnapshotWriteIsAtomic pins the Atomare-Zugriffe fix for the config history: snapshot() must land
// each file via an atomic tmp+rename, so a concurrent List/Get — which read the history directory
// WITHOUT holding the runs-store mutex — can never observe a half-written snapshot, and a crash
// mid-write can never leave a torn file at the final path. It asserts the invariant that proves it (the
// finished directory holds exactly one complete, parseable ".json" and no leftover ".tmp") and that
// List, which matches only ".json", ignores an in-flight ".json.tmp" the way a reader racing a live
// write would.
func TestSnapshotWriteIsAtomic(t *testing.T) {
	h := &History{dir: t.TempDir()}

	h.snapshot("create", "tester", []Run{{ID: "run_a", Title: "A"}})

	entries, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatalf("read history dir: %v", err)
	}
	var jsonCount, tmpCount int
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".json.tmp"):
			tmpCount++
		case strings.HasSuffix(e.Name(), ".json"):
			jsonCount++
		}
	}
	if jsonCount != 1 {
		t.Fatalf("want exactly one .json snapshot, got %d", jsonCount)
	}
	if tmpCount != 0 {
		t.Fatalf("atomic write left a .tmp behind: %d", tmpCount)
	}

	// The single listed snapshot must be complete and restorable — no torn content ever reaches disk.
	snaps, err := h.List()
	if err != nil || len(snaps) != 1 {
		t.Fatalf("List after snapshot: err=%v len=%d", err, len(snaps))
	}
	full, ok, err := h.Get(snaps[0].TS)
	if err != nil || !ok || len(full.Runs) != 1 || full.Runs[0].Title != "A" {
		t.Fatalf("Get returned incomplete snapshot: ok=%v err=%v %+v", ok, err, full.Runs)
	}

	// A stray in-flight tmp — what a plain truncating writer would expose to a concurrent reader
	// mid-write — must stay invisible: List matches only ".json", so the ".json.tmp" is never parsed.
	stray := filepath.Join(h.dir, "2099-01-02T03-04-05.json.tmp")
	if err := os.WriteFile(stray, []byte("{ partial, not valid json"), 0o600); err != nil {
		t.Fatalf("write stray tmp: %v", err)
	}
	snaps, err = h.List()
	if err != nil {
		t.Fatalf("List with stray tmp present: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("List must ignore the .json.tmp; want 1 snapshot, got %d", len(snaps))
	}
}
