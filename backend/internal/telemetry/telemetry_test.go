package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devlab/backend/internal/statepath"
)

// The ONE AI-usage pool takes samples from every source and aggregates them per source over a
// window — reported, never judged (cross-cutting 5).
func TestUsageLedgerRecordsAndAggregatesPerSource(t *testing.T) {
	u := OpenUsageAt(filepath.Join(t.TempDir(), "ai-usage.json"))
	now := time.Now().UTC()

	samples := []UsageSample{
		{Source: "assistant", User: "ada", Model: "m", In: 100, Out: 10, At: now.Add(-time.Hour)},
		{Source: "agent", User: "ada", Repo: "o/x", Model: "m", In: 200, Out: 20, At: now.Add(-2 * time.Hour)},
		{Source: "run", Repo: "o/x", Model: "m", In: 400, Out: 40, At: now.Add(-3 * time.Hour)},
		{Source: "run", Repo: "o/y", Model: "m", In: 800, Out: 80, At: now.Add(-100 * time.Hour)}, // outside the window
	}
	for _, s := range samples {
		if err := u.Record(s); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	view, err := u.Aggregate(24 * time.Hour)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if view.WindowHours != 24 {
		t.Errorf("windowHours = %d, want 24", view.WindowHours)
	}
	if view.Samples != 3 {
		t.Errorf("samples in window = %d, want 3", view.Samples)
	}
	if view.Totals.InputTokens != 700 || view.Totals.OutputTokens != 70 {
		t.Errorf("totals = %+v, want 700/70", view.Totals)
	}
	if got := view.BySource["run"]; got.InputTokens != 400 || got.OutputTokens != 40 {
		t.Errorf("run source = %+v, want 400/40", got)
	}
	if got := view.BySource["assistant"]; got.InputTokens != 100 {
		t.Errorf("assistant source = %+v", got)
	}
	if _, ok := view.BySource["agent"]; !ok {
		t.Error("the agent source must appear in the aggregate")
	}

	// The whole pool is reachable too (window ≤ 0), so nothing recorded is unreportable.
	all, err := u.Aggregate(0)
	if err != nil {
		t.Fatal(err)
	}
	if all.Samples != 4 || all.Totals.InputTokens != 1500 {
		t.Errorf("full aggregate = %+v", all)
	}
}

// The pool is a rolling window: samples older than the maximum age are dropped on write, so the
// report stays portioned instead of growing into an archive.
func TestUsageLedgerRollsOffOldSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai-usage.json")
	u := OpenUsageAt(path)
	now := time.Now().UTC()

	if err := u.Record(UsageSample{Source: "run", In: 1, At: now.Add(-usageMaxAge - time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := u.Record(UsageSample{Source: "run", In: 2, At: now}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f usageFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Samples) != 1 || f.Samples[0].In != 2 {
		t.Errorf("stored samples = %+v, want only the fresh one", f.Samples)
	}
}

// A missing pool is a named condition, never a silent drop: a consumption sample that cannot be
// recorded says so.
func TestUsageLedgerWithoutPoolIsNamed(t *testing.T) {
	if err := OpenUsage(nil).Record(UsageSample{Source: "run"}); err == nil {
		t.Fatal("recording without a state root must be refused, not swallowed")
	}
	view, err := OpenUsage(nil).Aggregate(time.Hour)
	if err != nil {
		t.Fatalf("an unconfigured pool aggregates to empty, got %v", err)
	}
	if view.Samples != 0 || view.BySource == nil {
		t.Errorf("empty aggregate = %+v (bySource must never be nil)", view)
	}
}

// Storage reports one row per POOL (not per file) with what it actually holds, and only for pools
// that exist — absence is not occupancy.
func TestStorageReportsExistingPoolsPortioned(t *testing.T) {
	root := t.TempDir()
	p := &statepath.Paths{Root: root}
	if err := os.MkdirAll(p.Mercury(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Runs(), []byte(`{"runs":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p.Executions(), "exec_1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Executions(), "exec_1", "result.json"), []byte(`{"id":"exec_1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Executions(), "exec_1", "state.json"), []byte(`{"id":"exec_1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	view, err := Storage(p)
	if err != nil {
		t.Fatalf("Storage: %v", err)
	}
	byName := map[string]int64{}
	files := map[string]int{}
	for _, pl := range view.Pools {
		byName[pl.Name] = pl.Bytes
		files[pl.Name] = pl.Files
	}
	if _, ok := byName["runs"]; !ok {
		t.Errorf("the runs pool must be reported, got %+v", view.Pools)
	}
	if files["executions"] != 2 {
		t.Errorf("executions files = %d, want 2", files["executions"])
	}
	if _, ok := byName["attachments"]; ok {
		t.Error("a pool that does not exist yet must not be reported as an empty one")
	}
	var sum int64
	for _, pl := range view.Pools {
		sum += pl.Bytes
	}
	if view.TotalBytes != sum {
		t.Errorf("totalBytes = %d, want the sum of the pools (%d)", view.TotalBytes, sum)
	}
}

// Storage without a state root is a named error, not a fabricated empty report.
func TestStorageWithoutRootIsNamed(t *testing.T) {
	if _, err := Storage(nil); err == nil {
		t.Fatal("a storage report without a state root must be refused")
	}
}

// Load reports the process's own claim — goroutines always, CPU/RSS where the kernel exposes them.
// It never judges: there is no threshold, no verdict, just the figures.
func TestLoadReportsOwnClaim(t *testing.T) {
	view := Load()
	if view.Goroutines <= 0 {
		t.Errorf("goroutines = %d, want the running count", view.Goroutines)
	}
	if view.CPUPercent < 0 {
		t.Errorf("cpuPercent = %f, must never be negative", view.CPUPercent)
	}
	if _, err := os.Stat("/proc/self/stat"); err == nil {
		if view.RSSBytes <= 0 {
			t.Errorf("rssBytes = %d, want the resident size the kernel reports", view.RSSBytes)
		}
	}
}
