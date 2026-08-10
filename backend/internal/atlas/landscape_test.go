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

// The landscape reflection follows the routes onto their shelves for the same reason the port ledger does
// — and here the cost of not following them is larger: the PRODUCTION ROSTER is derived from exactly these
// routed ids, so a migrated host would report an empty landscape while every service on it ran.
func TestHostNodesReadBothShelvesAndTheFlatDirectory(t *testing.T) {
	conf := t.TempDir()
	perms := t.TempDir()
	t.Setenv("DEVLAB_CADDY_CONF", conf)
	t.Setenv("DEVLAB_HOLISTIC_PERMS", perms)
	write := func(rel, id, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(conf, rel), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(conf, rel, id+".caddy"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services", "prizm", "handle /api/services/prizm/* {\n\treverse_proxy 127.0.0.1:18811\n}\n")
	write("apps", "holistic", "http://dash.example.test:8080 {\n\thandle /api/* {\n\t\treverse_proxy 127.0.0.1:8770\n\t}\n}\n")
	write(".", "legacy", "handle /api/services/legacy/* {\n\treverse_proxy 127.0.0.1:18899\n}\n")

	nodes := hostNodes()
	for id, port := range map[string]int{"prizm": 18811, "holistic": 8770, "legacy": 18899} {
		n, ok := nodes[id]
		if !ok {
			t.Fatalf("%q must be seen on this host — a service the reflection cannot see is a service the production roster forgets", id)
		}
		if !n.HasRoute {
			t.Errorf("%q must be reported as routed: %+v", id, n)
		}
		if n.Port != port {
			t.Errorf("%q must be reported on port %d, got %d", id, port, n.Port)
		}
	}
}
