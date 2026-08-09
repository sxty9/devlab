package atlas

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"devlab/backend/internal/model"
)

// writeRouteFixtures lays down a route directory shaped like the host's drop-in dir.
func writeRouteFixtures(t *testing.T, routes map[string]int) string {
	t.Helper()
	dir := t.TempDir()
	for id, port := range routes {
		content := "handle /api/services/" + id + "/* {\n\treverse_proxy 127.0.0.1:" + strconv.Itoa(port) + "\n}\n"
		if err := os.WriteFile(filepath.Join(dir, id+".caddy"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeProcNetTCP writes a /proc/net/tcp-format fixture with the given ports in LISTEN state.
func writeProcNetTCP(t *testing.T, name string, listen []int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n")
	for i, p := range listen {
		b.WriteString("   " + strconv.Itoa(i) + ": 0100007F:" + strings.ToUpper(strconv.FormatInt(int64(p), 16)) +
			" 00000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 1 0000000000000000 100 0 0 10 0\n")
	}
	// One ESTABLISHED row (state 01) that must NOT count as listening.
	b.WriteString("  99: 0100007F:1F90 0100007F:2222 01 00000000:00000000 00:00000000 00000000   999        0 1 0000000000000000 100 0 0 10 0\n")
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func find(allocs []model.PortAllocation, port int, service string) (model.PortAllocation, bool) {
	for _, a := range allocs {
		if a.Port == port && a.Service == service {
			return a, true
		}
	}
	return model.PortAllocation{}, false
}

// The known legacy double-booking pattern: two services routed to ONE port. The ledger derived
// from fixture routes + a fixture socket table must find and NAME it (REQ-044.5, B-04).
func TestAllocationsFindsDoubleBookingFromFixtures(t *testing.T) {
	routes := writeRouteFixtures(t, map[string]int{"aigentic": 8780, "prizm": 8780, "hostek": 8771})
	tcp := writeProcNetTCP(t, "tcp", []int{8780, 8771, 8781}) // 8781: bound but unrouted (the dashboard's own)
	tcp6 := writeProcNetTCP(t, "tcp6", nil)

	allocs, err := Allocations(routes, tcp, tcp6)
	if err != nil {
		t.Fatal(err)
	}

	for _, svc := range []string{"aigentic", "prizm"} {
		a, ok := find(allocs, 8780, svc)
		if !ok {
			t.Fatalf("missing allocation for %s on 8780: %+v", svc, allocs)
		}
		if !a.Conflict || !a.Routed || !a.Bound {
			t.Errorf("%s on 8780 should be routed+bound+conflict, got %+v", svc, a)
		}
	}
	if a, ok := find(allocs, 8771, "hostek"); !ok || a.Conflict {
		t.Errorf("hostek on 8771 should exist without conflict, got %+v ok=%v", a, ok)
	}
	if a, ok := find(allocs, 8781, ""); !ok || a.Routed || !a.Bound {
		t.Errorf("8781 should appear as bound-but-unrouted, got %+v ok=%v", a, ok)
	}
	if _, ok := find(allocs, 8080, ""); ok {
		t.Errorf("the ESTABLISHED row must not count as listening: %+v", allocs)
	}
	// Sorted by port, then service.
	for i := 1; i < len(allocs); i++ {
		prev, cur := allocs[i-1], allocs[i]
		if prev.Port > cur.Port || (prev.Port == cur.Port && prev.Service > cur.Service) {
			t.Errorf("allocations not sorted at %d: %+v", i, allocs)
		}
	}
}

// A route directory that cannot be read is an honest error, never an empty green ledger.
func TestAllocationsMissingRoutesDirErrors(t *testing.T) {
	if _, err := Allocations(filepath.Join(t.TempDir(), "absent"), "", ""); err == nil {
		t.Fatal("expected an error for an unreadable routes dir")
	}
}

// Unreadable socket tables degrade to the routes alone (sandbox), not to an error.
func TestAllocationsDegradesWithoutProcfs(t *testing.T) {
	routes := writeRouteFixtures(t, map[string]int{"hostek": 8771})
	allocs, err := Allocations(routes, filepath.Join(t.TempDir(), "no-tcp"), "")
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := find(allocs, 8771, "hostek"); !ok || a.Bound {
		t.Errorf("expected routed-only hostek, got %+v ok=%v", a, ok)
	}
}

func TestProposeLowestFreeSkipsRoutedAndBound(t *testing.T) {
	allocs := []model.PortAllocation{
		{Port: 8770, Service: "a", Routed: true},
		{Port: 8771, Bound: true}, // bound but unrouted still counts as taken
		{Port: 8773, Service: "c", Routed: true},
	}
	got, err := Propose(allocs, Band{Lo: 8770, Hi: 8785})
	if err != nil || got != 8772 {
		t.Errorf("Propose = %d, %v; want 8772", got, err)
	}
}

func TestProposeExhaustedBandErrors(t *testing.T) {
	allocs := []model.PortAllocation{
		{Port: 8770, Service: "a", Routed: true},
		{Port: 8771, Service: "b", Routed: true},
	}
	if _, err := Propose(allocs, Band{Lo: 8770, Hi: 8771}); err == nil {
		t.Fatal("expected an error for an exhausted band")
	}
}

// The setup-on-a-taken-port case: rejected, the holder named, a free port proposed instead
// (REQ-044.2) — never silently the taken one.
func TestProposeDesiredOccupiedRejectedWithProposal(t *testing.T) {
	allocs := []model.PortAllocation{
		{Port: 8780, Service: "aigentic", Routed: true, Bound: true},
		{Port: 8771, Service: "hostek", Routed: true},
	}
	_, err := ProposeDesired(allocs, Band{Lo: 8770, Hi: 8785}, 8780)
	var occ *OccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("expected *OccupiedError, got %v", err)
	}
	if occ.Holder != "aigentic" || occ.Free != 8770 {
		t.Errorf("occ = %+v; want holder aigentic, free 8770", occ)
	}
	if msg := occ.Error(); !strings.Contains(msg, "aigentic") || !strings.Contains(msg, "8770") {
		t.Errorf("error should name the holder and the free proposal: %q", msg)
	}
}

func TestProposeDesiredBoundButUnroutedNamesNoHolder(t *testing.T) {
	allocs := []model.PortAllocation{{Port: 8781, Bound: true}}
	_, err := ProposeDesired(allocs, Band{Lo: 8770, Hi: 8785}, 8781)
	var occ *OccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("expected *OccupiedError, got %v", err)
	}
	if occ.Holder != "" || !strings.Contains(occ.Error(), "bound but not routed") {
		t.Errorf("bound-only occupation should carry no holder: %+v (%s)", occ, occ.Error())
	}
}

func TestProposeDesiredFreeGranted(t *testing.T) {
	got, err := ProposeDesired(nil, Band{Lo: 8770, Hi: 8785}, 8779)
	if err != nil || got != 8779 {
		t.Errorf("ProposeDesired = %d, %v; want 8779", got, err)
	}
	// No preference falls back to the lowest free.
	got, err = ProposeDesired([]model.PortAllocation{{Port: 8770, Service: "a", Routed: true}}, Band{Lo: 8770, Hi: 8785}, 0)
	if err != nil || got != 8771 {
		t.Errorf("ProposeDesired(0) = %d, %v; want 8771", got, err)
	}
}

func TestRoutedPort(t *testing.T) {
	allocs := []model.PortAllocation{
		{Port: 8771, Service: "hostek", Routed: true},
		{Port: 8781, Bound: true},
	}
	if p, ok := RoutedPort(allocs, "hostek"); !ok || p != 8771 {
		t.Errorf("RoutedPort(hostek) = %d, %v; want 8771, true", p, ok)
	}
	if _, ok := RoutedPort(allocs, "absent"); ok {
		t.Error("RoutedPort should miss for an unrouted service")
	}
}

func TestBandFromEnv(t *testing.T) {
	t.Setenv("DEVLAB_PORT_BAND", "9000-9010")
	if b := BandFromEnv(); b.Lo != 9000 || b.Hi != 9010 {
		t.Errorf("band = %+v, want 9000-9010", b)
	}
	t.Setenv("DEVLAB_PORT_BAND", "garbage")
	if b := BandFromEnv(); b.Lo != defaultBandLo || b.Hi != defaultBandHi {
		t.Errorf("malformed band should fall back to the default, got %+v", b)
	}
	t.Setenv("DEVLAB_PORT_BAND", "")
	if b := BandFromEnv(); b.Lo != defaultBandLo || b.Hi != defaultBandHi {
		t.Errorf("unset band should be the default, got %+v", b)
	}
}

// THE LEDGER FOLLOWS THE ROUTES ONTO THEIR SHELVES. A delivered route is no longer one file flat in the
// route directory: a uniform service's fragment goes on `services/`, a root application's whole site block
// on `apps/`. This ledger answers "is that port already spoken for?", so reading only the flat directory
// after that move would not merely lose information — it would hand a NEW service a port an existing
// service is already routed to, and the two would fight over one socket. The flat directory keeps being
// read because a grown, hand-built edge still keeps its routes there.
func TestAllocationsReadBothShelvesAndTheFlatDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, id string, port int, app bool) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, rel), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "handle /api/services/" + id + "/* {\n\treverse_proxy 127.0.0.1:" + strconv.Itoa(port) + "\n}\n"
		if app {
			body = "http://" + id + ".example.test:8080 {\n\thandle /api/* {\n\t\treverse_proxy 127.0.0.1:" +
				strconv.Itoa(port) + "\n\t}\n}\n"
		}
		if err := os.WriteFile(filepath.Join(dir, rel, id+".caddy"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services", "prizm", 18811, false) // a migrated uniform service
	write("apps", "holistic", 8770, true)    // a root application's site block
	write("apps", "devlab", 8781, true)      // a second root application
	write(".", "legacy", 18899, false)       // a grown edge's flat route, still valid

	tcp := writeProcNetTCP(t, "tcp", []int{18811, 8770})
	got, err := Allocations(dir, tcp, "")
	if err != nil {
		t.Fatalf("Allocations: %v", err)
	}
	byPort := map[int]model.PortAllocation{}
	for _, a := range got {
		byPort[a.Port] = a
	}
	for _, want := range []struct {
		port int
		id   string
	}{{18811, "prizm"}, {8770, "holistic"}, {8781, "devlab"}, {18899, "legacy"}} {
		a, ok := byPort[want.port]
		if !ok {
			t.Fatalf("port %d must be in the ledger — a port nobody sees is a port that gets handed out twice: %+v", want.port, got)
		}
		if !a.Routed {
			t.Errorf("port %d (%s) must be reported as routed: %+v", want.port, want.id, a)
		}
		if a.Service != want.id {
			t.Errorf("port %d must be held by %q, got %q", want.port, want.id, a.Service)
		}
	}
	// A half-migrated host carries the same route on the shelf AND flat for a moment. That is one holder,
	// not two, and it must never read as a conflict.
	write(".", "prizm", 18811, false)
	got2, err := Allocations(dir, tcp, "")
	if err != nil {
		t.Fatalf("Allocations: %v", err)
	}
	for _, a := range got2 {
		if a.Port == 18811 && a.Conflict {
			t.Errorf("one route present on two shelves during a migration is ONE holder, not a conflict: %+v", a)
		}
	}
}
