// Central port derivation for the Holistic landscape (F9, REQ-044).
//
// Ports used to be handed out by hand: a new service copied a default from the template and hoped
// nobody else had it — which is how one service shipped on another's port, crash-looped on the bind,
// and stayed dead until someone noticed. Nothing here is a maintained list that can go stale (the
// very thing that bit that pair). Occupancy is derived on read from two facts of the running host:
//
//	the edge routes   <routesDir>/<id>.caddy — who is routed to which port (named holders)
//	the bound sockets /proc/net/tcp{,6}      — which ports actually LISTEN right now
//
// A port is free only when neither routed nor bound. The band is only the WINDOW within which free
// ports are proposed; it never records assignments. Everything below is a passive projection: no
// state is stored, and double-bookings are named rather than hidden.
package atlas

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"devlab/backend/internal/model"
)

// Band is the window within which free ports are proposed. It bounds proposals only — it is not a
// ledger of who holds what.
type Band struct{ Lo, Hi int }

// The default Holistic service port band. Services answer on 127.0.0.1:<port> behind the edge.
const (
	defaultBandLo = 8770
	defaultBandHi = 8799
)

// BandFromEnv returns the proposal band, overridable with DEVLAB_PORT_BAND="lo-hi" so an operator
// can widen it without a code change. A malformed value falls back to the default band.
func BandFromEnv() Band {
	if s := os.Getenv("DEVLAB_PORT_BAND"); s != "" {
		if b, ok := parseBand(s); ok {
			return b
		}
	}
	return Band{Lo: defaultBandLo, Hi: defaultBandHi}
}

func parseBand(s string) (Band, bool) {
	parts := strings.SplitN(strings.TrimSpace(s), "-", 2)
	if len(parts) != 2 {
		return Band{}, false
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	hi, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || lo <= 0 || hi < lo {
		return Band{}, false
	}
	return Band{Lo: lo, Hi: hi}, true
}

func (b Band) String() string { return strconv.Itoa(b.Lo) + "-" + strconv.Itoa(b.Hi) }

// Allocations derives the port ledger from the given sources: the route drop-ins in routesDir
// (one <id>.caddy per routed service) and the kernel socket tables (procNetTCP/procNetTCP6 in
// /proc/net/tcp format; an empty or unreadable socket path degrades to the routes alone, e.g. in
// a sandbox). Pure over its inputs — fixture directories and files exercise it fully.
//
// One entry per routed (port, service) pair, plus one service-less entry per bound-but-unrouted
// port. Conflict is set on every routed entry whose port is claimed by more than one service —
// the double-booking is named, never collapsed. Sorted by port, then service.
func Allocations(routesDir, procNetTCP, procNetTCP6 string) ([]model.PortAllocation, error) {
	routed, err := routedFrom(routesDir)
	if err != nil {
		return nil, err
	}
	bound := listeningFrom(procNetTCP, procNetTCP6)

	allocs := make([]model.PortAllocation, 0, len(routed)+len(bound))
	for port, services := range routed {
		conflict := len(services) > 1
		for _, svc := range services {
			allocs = append(allocs, model.PortAllocation{
				Port: port, Service: svc, Routed: true, Bound: bound[port], Conflict: conflict,
			})
		}
	}
	for port := range bound {
		if _, isRouted := routed[port]; !isRouted {
			allocs = append(allocs, model.PortAllocation{Port: port, Bound: true})
		}
	}
	sort.Slice(allocs, func(i, j int) bool {
		if allocs[i].Port != allocs[j].Port {
			return allocs[i].Port < allocs[j].Port
		}
		return allocs[i].Service < allocs[j].Service
	})
	return allocs, nil
}

// AllocationsNow reads the ledger off this host: the shared route directory (DEVLAB_CADDY_CONF
// overrides, as everywhere in atlas) and the kernel socket tables (DEVLAB_PROC_NET_TCP{,6}
// override — a test seam; /proc/net is a kernel convention, not an instance value).
func AllocationsNow() ([]model.PortAllocation, error) {
	tcp := os.Getenv("DEVLAB_PROC_NET_TCP")
	if tcp == "" {
		tcp = "/proc/net/tcp"
	}
	tcp6 := os.Getenv("DEVLAB_PROC_NET_TCP6")
	if tcp6 == "" {
		tcp6 = "/proc/net/tcp6"
	}
	return Allocations(caddyDir(), tcp, tcp6)
}

// routedFrom reads port -> holder ids off the route drop-ins in dir.
func routedFrom(dir string) (map[int][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("atlas: reading routes %s: %w", dir, err)
	}
	byPort := map[int][]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, ok := stem(e.Name(), ".caddy")
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if m := portRe.FindSubmatch(raw); m != nil {
			if p, err := strconv.Atoi(string(m[1])); err == nil && p > 0 {
				byPort[p] = append(byPort[p], id)
			}
		}
	}
	for p := range byPort {
		sort.Strings(byPort[p])
	}
	return byPort, nil
}

// listeningFrom returns the set of TCP ports in LISTEN state, parsed from files in the
// /proc/net/tcp format. Unreadable files yield the empty set (sandbox degradation).
func listeningFrom(paths ...string) map[int]bool {
	const tcpListen = "0A" // include/net/tcp_states.h: TCP_LISTEN
	ports := map[int]bool{}
	for _, f := range paths {
		if f == "" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		if len(lines) < 2 {
			continue
		}
		for _, ln := range lines[1:] { // skip the header row
			fields := strings.Fields(ln)
			if len(fields) < 4 || fields[3] != tcpListen {
				continue
			}
			local := fields[1] // HEXIP:HEXPORT
			if c := strings.LastIndex(local, ":"); c >= 0 {
				if p, err := strconv.ParseInt(local[c+1:], 16, 32); err == nil && p > 0 {
					ports[int(p)] = true
				}
			}
		}
	}
	return ports
}

// ─── proposals (pure over an already-derived ledger) ─────────────────────────

// OccupiedError says a desired port is taken: the holder is named (empty when the port is bound
// but unrouted) and, when the band still has one, a free port is proposed alongside — a setup can
// then say what happened instead of installing a service that will not start (REQ-044.2).
type OccupiedError struct {
	Port   int
	Holder string // routed holder(s), comma-joined; "" = bound but unrouted
	Free   int    // proposed free port; 0 when the band is exhausted
}

func (e *OccupiedError) Error() string {
	msg := "port " + strconv.Itoa(e.Port)
	if e.Holder != "" {
		msg += " is held by " + e.Holder
	} else {
		msg += " is in use (bound but not routed)"
	}
	if e.Free != 0 {
		return msg + "; " + strconv.Itoa(e.Free) + " is free"
	}
	return msg + "; no port is free in the band"
}

// occupied reports whether port is routed or bound in allocs, naming any routed holders.
func occupied(allocs []model.PortAllocation, port int) (holder string, taken bool) {
	var holders []string
	for _, a := range allocs {
		if a.Port != port {
			continue
		}
		taken = true
		if a.Routed && a.Service != "" {
			holders = append(holders, a.Service)
		}
	}
	sort.Strings(holders)
	return strings.Join(holders, ", "), taken
}

// RoutedPort returns the routed port a service already holds, if any.
func RoutedPort(allocs []model.PortAllocation, service string) (int, bool) {
	for _, a := range allocs {
		if a.Routed && a.Service == service {
			return a.Port, true
		}
	}
	return 0, false
}

// Propose returns the lowest port in the band that is neither routed nor bound. It reserves
// nothing — the port becomes a service's the moment its route is written.
func Propose(allocs []model.PortAllocation, band Band) (int, error) {
	for p := band.Lo; p <= band.Hi; p++ {
		if _, taken := occupied(allocs, p); !taken {
			return p, nil
		}
	}
	return 0, fmt.Errorf("atlas: no free port in band %s", band)
}

// ProposeDesired validates a desired port against the ledger: 0 means no preference (lowest free
// in the band); a free desired port is granted as-is (a declared value wins, even out of band);
// an occupied one is REJECTED with an *OccupiedError that names the holder and proposes a free
// port instead (REQ-044.2) — never silently the taken one.
func ProposeDesired(allocs []model.PortAllocation, band Band, desired int) (int, error) {
	if desired == 0 {
		return Propose(allocs, band)
	}
	if holder, taken := occupied(allocs, desired); taken {
		free, _ := Propose(allocs, band)
		return 0, &OccupiedError{Port: desired, Holder: holder, Free: free}
	}
	return desired, nil
}
