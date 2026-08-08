package deploy

// The unit name comes from the DELIVERED setup product, not the repo name — proven on the two Go seams
// the shell wrappers cannot cover: the port decision (a service's own running unit holds its port even
// without a route) and the artifact-side name discovery the port source consults.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"devlab/backend/internal/atlas"
	"devlab/backend/internal/model"
)

// A desired port that is BOUND BUT UNROUTED, held by the service's OWN running unit, is idempotent — not
// a conflict. This is the measured `holistic` case: the service ran under its divergently named unit on
// 8770 with no route, so the old ledger saw 8770 as a foreign occupant and blocked the service's own
// renewal. With the own unit active the port is recognised as its own.
func TestDecidePortOwnActiveUnitHoldsBoundUnroutedPort(t *testing.T) {
	allocs := []model.PortAllocation{{Port: 8770, Bound: true}} // bound, no route → holder ""
	band := atlas.Band{Lo: 8770, Hi: 8799}

	port, first, err := decidePort(allocs, band, 8770, true)
	if err != nil || port != 8770 || first {
		t.Fatalf("own active unit on a bound-unrouted port must be idempotent: port=%d first=%v err=%v", port, first, err)
	}
}

// The SAME bound-but-unrouted port, but the service's own unit is NOT active, stays a conflict: only the
// delivering service's own running unit earns the bypass, never any bound listener.
func TestDecidePortInactiveOwnUnitStaysConflict(t *testing.T) {
	allocs := []model.PortAllocation{{Port: 8770, Bound: true}}
	band := atlas.Band{Lo: 8770, Hi: 8799}

	_, _, err := decidePort(allocs, band, 8770, false)
	if err == nil {
		t.Fatal("a bound port with no active own unit must remain a conflict")
	}
	var occ *atlas.OccupiedError
	if !asOccupied(err, &occ) || occ.Port != 8770 {
		t.Fatalf("the conflict must be an OccupiedError naming the port: %v", err)
	}
}

// A port held by a ROUTED (thus named, foreign) service stays a conflict even when the own unit reports
// active — a foreign holder is never bypassed (task point 5: "remains one if a foreign holds it").
func TestDecidePortRoutedForeignHolderStaysConflict(t *testing.T) {
	allocs := []model.PortAllocation{{Port: 8770, Service: "other", Routed: true, Bound: true}}
	band := atlas.Band{Lo: 8771, Hi: 8799}

	_, _, err := decidePort(allocs, band, 8770, true)
	if err == nil {
		t.Fatal("a routed foreign holder must stay a conflict even with the own unit active")
	}
	var occ *atlas.OccupiedError
	if !asOccupied(err, &occ) || occ.Holder != "other" {
		t.Fatalf("the conflict must name the foreign holder: %v", err)
	}
}

// A free desired port is granted as-is whether or not the own unit is active — the bypass only ever turns
// an occupied-but-own port into an idempotent one; it never invents occupancy.
func TestDecidePortFreeDesiredGranted(t *testing.T) {
	band := atlas.Band{Lo: 8770, Hi: 8799}
	port, first, err := decidePort(nil, band, 8775, true)
	if err != nil || port != 8775 || !first {
		t.Fatalf("a free desired port must be granted as first-time: port=%d first=%v err=%v", port, first, err)
	}
}

func asOccupied(err error, target **atlas.OccupiedError) bool {
	oe, ok := err.(*atlas.OccupiedError)
	if ok {
		*target = oe
	}
	return ok
}

// DeliveredUnitName reads the unit's real name from the artifact's setup product, mirroring the shell
// rule: the conventional <service>.service wins; a lone divergent *.service is taken; ambiguity and
// absence both yield "" so the caller falls back to the service id.
func TestDeliveredUnitName(t *testing.T) {
	mk := func(t *testing.T, files ...string) string {
		art := t.TempDir()
		if len(files) > 0 {
			setup := filepath.Join(art, "setup")
			if err := os.MkdirAll(setup, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if err := os.WriteFile(filepath.Join(setup, f), []byte("[Unit]\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return art
	}

	cases := []struct {
		name    string
		files   []string
		service string
		want    string
	}{
		{"conventional wins", []string{"holistic.service", "holistic.caddy"}, "holistic", "holistic"},
		{"divergent lone unit", []string{"holistic-dashboard.service", "holistic.caddy"}, "holistic", "holistic-dashboard"},
		{"conventional beats a sibling", []string{"holistic.service", "extra.service"}, "holistic", "holistic"},
		{"ambiguous, none conventional", []string{"a.service", "b.service"}, "holistic", ""},
		{"no setup dir", nil, "holistic", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeliveredUnitName(mk(t, c.files...), c.service); got != c.want {
				t.Errorf("DeliveredUnitName = %q, want %q", got, c.want)
			}
		})
	}
}

// LivePorts wires the unit name into the decision: with the own unit active (seam), a bound-unrouted
// desired port is idempotent; the isActive default is only a systemctl probe, replaced here.
func TestLivePortsUsesOwnUnitSeam(t *testing.T) {
	lp := LivePorts{Unit: "holistic-dashboard", Active: func(context.Context, string) bool { return true }}
	// The decision itself is the pure decidePort; this only asserts the seam is honoured (no host call).
	if !lp.isActive(context.Background(), "holistic-dashboard") {
		t.Fatal("the Active seam must be consulted instead of systemctl")
	}
}
