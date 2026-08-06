package atlas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetCache clears the package host-reflection cache so a test reads its own fixture dirs rather
// than a value another test populated. The cache is package-owned; this test lives in the package.
func resetCache(t *testing.T) {
	t.Helper()
	mu.Lock()
	cached = nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		cached = nil
		mu.Unlock()
	})
}

// TestRoutedServiceIDs proves the roster backbone is DERIVED from the edge routes the host serves —
// a service is a member because the edge routes it — and that a manifest-only service with no route
// (and the load-bearing members that carry no route at all) are correctly NOT among the routed ids.
func TestRoutedServiceIDs(t *testing.T) {
	routes := writeRouteFixtures(t, map[string]int{"aigentic": 8780, "notify": 8778, "prizm": 8781})
	perms := t.TempDir()
	// A manifest-only service (rights, but no edge route) must NOT count as routed.
	if err := os.WriteFile(filepath.Join(perms, "loner.json"), []byte(`{"service":"loner","categories":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_CADDY_CONF", routes)
	t.Setenv("DEVLAB_HOLISTIC_PERMS", perms)
	resetCache(t)

	got := strings.Join(RoutedServiceIDs(), ",")
	want := "aigentic,notify,prizm"
	if got != want {
		t.Fatalf("routed service ids must be exactly the edge-routed services, got %q want %q", got, want)
	}
}
