package runs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigStoreRoundTrip pins the passive store's contract: a missing file reads as the zero Config,
// a Set is read back verbatim by a fresh store (persisted), and the write is atomic (no .tmp left).
func TestConfigStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s := &ConfigStore{path: path}

	// Missing file → zero Config, no error.
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get on missing store: %v", err)
	}
	if got.DefaultTimeBudget != "" {
		t.Fatalf("missing store should be zero Config, got %+v", got)
	}

	if err := s.Set(Config{DefaultTimeBudget: "3h"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// A fresh store reads the persisted value.
	got, err = (&ConfigStore{path: path}).Get()
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got.DefaultTimeBudget != "3h" {
		t.Fatalf("want 3h, got %q", got.DefaultTimeBudget)
	}

	// Atomic write: exactly one config.json and no leftover .tmp.
	entries, _ := os.ReadDir(dir)
	var jsonCount, tmpCount int
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".tmp"):
			tmpCount++
		case strings.HasSuffix(e.Name(), ".json"):
			jsonCount++
		}
	}
	if jsonCount != 1 || tmpCount != 0 {
		t.Fatalf("want one .json and no .tmp, got json=%d tmp=%d", jsonCount, tmpCount)
	}
}

// TestConfigStoreClearsToUnset confirms an empty value round-trips as unset (so the resolver falls back
// to the built-in default), not as a stored empty string that would shadow it differently.
func TestConfigStoreClearsToUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := &ConfigStore{path: path}
	if err := s.Set(Config{DefaultTimeBudget: "6h"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set(Config{DefaultTimeBudget: ""}); err != nil {
		t.Fatalf("Set empty: %v", err)
	}
	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DefaultTimeBudget != "" {
		t.Fatalf("cleared value should read empty, got %q", got.DefaultTimeBudget)
	}
}
