package atlas

import (
	"strings"
	"testing"
)

// The prizm case: a setup on an already-held port is rejected — it names the holder and grants a free
// port instead, never silently the taken one.
func TestProposeRejectsOccupiedAndNamesFree(t *testing.T) {
	routed := map[int][]string{8780: {"aigentic"}, 8771: {"hostek"}}
	p := proposeFrom("prizm", 8780, 8770, 8785, routed, nil)

	if p.Granted == 8780 {
		t.Fatalf("granted the occupied port 8780: %+v", p)
	}
	if p.Conflict != "aigentic" {
		t.Errorf("conflict = %q, want aigentic", p.Conflict)
	}
	if p.Granted != 8770 { // lowest free in 8770..8785 with 8780,8771 taken
		t.Errorf("granted = %d, want 8770", p.Granted)
	}
	if !p.InBand {
		t.Errorf("granted %d should be in band", p.Granted)
	}
	if !strings.Contains(p.Note, "aigentic") || !strings.Contains(p.Note, "free") {
		t.Errorf("note should name the holder and a free port: %q", p.Note)
	}
}

func TestProposeGrantsFreeDesired(t *testing.T) {
	p := proposeFrom("prizm", 8781, 8770, 8785, map[int][]string{8780: {"aigentic"}}, nil)
	if p.Granted != 8781 || p.Conflict != "" || !p.InBand {
		t.Errorf("a free desired port should be granted as-is: %+v", p)
	}
}

func TestProposeNoPreferenceLowestFree(t *testing.T) {
	routed := map[int][]string{8770: {"a"}, 8771: {"b"}, 8773: {"c"}}
	p := proposeFrom("new", 0, 8770, 8785, routed, nil)
	if p.Granted != 8772 { // 8770,8771 taken; 8772 is the lowest free (8773 also taken)
		t.Errorf("granted = %d, want 8772: %+v", p.Granted, p)
	}
}

// A service that already holds a port keeps it — re-proposing must be idempotent, not a move.
func TestProposeIdempotentKeepsOwnPort(t *testing.T) {
	routed := map[int][]string{8780: {"aigentic"}}
	for _, desired := range []int{0, 8780} {
		p := proposeFrom("aigentic", desired, 8770, 8785, routed, nil)
		if p.Granted != 8780 || p.Conflict != "" {
			t.Errorf("desired=%d: aigentic should keep 8780, got %+v", desired, p)
		}
	}
}

// A port that is bound but carries no Caddy route (the dashboard's own, say) is still occupied and must
// never be proposed as free.
func TestProposeExcludesBoundButUnroutedPort(t *testing.T) {
	bound := map[int]bool{8770: true, 8771: true}
	p := proposeFrom("new", 8770, 8770, 8785, nil, bound)
	if p.Granted == 8770 || p.Granted == 8771 {
		t.Fatalf("granted a bound port: %+v", p)
	}
	if p.Granted != 8772 {
		t.Errorf("granted = %d, want 8772", p.Granted)
	}
	if !strings.Contains(p.Note, "in use") {
		t.Errorf("note should say the desired port is in use: %q", p.Note)
	}
}

func TestProposeBandExhausted(t *testing.T) {
	routed := map[int][]string{8770: {"a"}, 8771: {"b"}}
	p := proposeFrom("new", 0, 8770, 8771, routed, nil)
	if p.Granted != 0 || p.InBand {
		t.Errorf("an exhausted band should grant 0 / not-in-band: %+v", p)
	}
	if !strings.Contains(p.Note, "No port is free") {
		t.Errorf("note should say no port is free: %q", p.Note)
	}
}

func TestProposeDesiredFreeButOutOfBand(t *testing.T) {
	p := proposeFrom("new", 9001, 8770, 8799, nil, nil)
	if p.Granted != 9001 || p.InBand {
		t.Errorf("a free out-of-band desired port is granted but flagged out-of-band: %+v", p)
	}
	if !strings.Contains(p.Note, "outside") {
		t.Errorf("note should flag the out-of-band port: %q", p.Note)
	}
}

func TestAllocationHeldAndFree(t *testing.T) {
	routed := map[int][]string{8772: {"privleg"}, 8770: {"hostek"}}
	bound := map[int]bool{8771: true} // bound but unrouted
	a := allocationFrom(8770, 8774, routed, bound, "t")

	if len(a.Held) != 2 || a.Held[0].Port != 8770 || a.Held[1].Port != 8772 {
		t.Errorf("held should be sorted by port: %+v", a.Held)
	}
	// free = band 8770..8774 minus routed {8770,8772} minus bound {8771} = 8773,8774
	want := []int{8773, 8774}
	if len(a.Free) != len(want) || a.Free[0] != 8773 || a.Free[1] != 8774 {
		t.Errorf("free = %v, want %v", a.Free, want)
	}
}

func TestAllocationSurfacesDoubleBooking(t *testing.T) {
	routed := map[int][]string{8780: {"aigentic", "prizm"}}
	a := allocationFrom(8770, 8785, routed, nil, "t")
	if len(a.Held) != 1 || len(a.Held[0].IDs) != 2 {
		t.Fatalf("a double-booked port should list both holders: %+v", a.Held)
	}
}

func TestFindingsFlagsOutOfBand(t *testing.T) {
	t.Setenv("DEVLAB_PORT_BAND", "8770-8799")
	nodes := []Node{
		{ID: "aigentic", Port: 8780, HasManifest: true, HasRoute: true},
		{ID: "legacy", Port: 9001, HasManifest: true, HasRoute: true},
	}
	fs := findings(nodes, false)
	if !hasFinding(fs, "warn", "legacy", "außerhalb") {
		t.Errorf("expected an out-of-band finding for legacy: %+v", fs)
	}
	if hasFinding(fs, "warn", "aigentic", "außerhalb") {
		t.Errorf("aigentic is in band and must not be flagged out-of-band: %+v", fs)
	}
}

func TestFindingsFlagsDoubleBooking(t *testing.T) {
	nodes := []Node{
		{ID: "aigentic", Port: 8780, HasManifest: true, HasRoute: true},
		{ID: "prizm", Port: 8780, HasManifest: true, HasRoute: true},
	}
	fs := findings(nodes, false)
	found := false
	for _, f := range fs {
		if f.Severity == "error" && strings.Contains(f.Message, "8780") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a port-collision error: %+v", fs)
	}
}

func hasFinding(fs []Finding, severity, node, substr string) bool {
	for _, f := range fs {
		if f.Severity != severity || !strings.Contains(f.Message, substr) {
			continue
		}
		for _, n := range f.Nodes {
			if n == node {
				return true
			}
		}
	}
	return false
}
