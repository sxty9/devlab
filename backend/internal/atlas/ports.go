package atlas

// Central port allocation for the Holistic landscape.
//
// Ports used to be handed out by hand: a new service copied a default from the template and hoped
// nobody else had it. That is exactly how `prizm` shipped on 8780 — aigentic's port — crash-looped
// on the bind, and stayed dead until someone noticed. The allocation below removes the guessing:
// it reads which ports are ACTUALLY in use off this host and proposes a provably free one.
//
// Nothing here is a maintained list that can go stale (the very thing that bit prizm). Occupancy is
// derived on read from two facts of the running host:
//
//	the Caddy routes  /etc/caddy/conf.d/<id>.caddy   — who is routed to which port (named holders)
//	the bound sockets /proc/net/tcp{,6}              — which ports are actually LISTENing right now
//
// A port is free only when neither routed nor bound. The band is only the WINDOW within which free
// ports are proposed; it never records assignments. This stays a passive projection, like the rest
// of atlas: it holds no state and judges nothing it has not just read off the host.

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Holistic service port band. Services answer on 127.0.0.1:<port> behind the Caddy edge. The
// band bounds where free ports are proposed; it is not a list of who-holds-what. Overridable with
// DEVLAB_PORT_BAND="lo-hi" so an operator can widen it without a code change.
const (
	defaultBandLo = 8770
	defaultBandHi = 8799
)

func band() (int, int) {
	if s := os.Getenv("DEVLAB_PORT_BAND"); s != "" {
		if lo, hi, ok := parseBand(s); ok {
			return lo, hi
		}
	}
	return defaultBandLo, defaultBandHi
}

func parseBand(s string) (lo, hi int, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(s), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	b, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || a <= 0 || b < a {
		return 0, 0, false
	}
	return a, b, true
}

func bandStr(lo, hi int) string { return strconv.Itoa(lo) + "–" + strconv.Itoa(hi) }

// PortHolder is a routed port and the service(s) pointed at it. More than one id is a double-booking
// — the prizm failure — and is surfaced as such rather than hidden.
type PortHolder struct {
	Port int      `json:"port"`
	IDs  []string `json:"ids"` // sorted; len > 1 means the port is double-booked
}

// Allocation is the port ledger: which service holds which routed port, and which ports in the band
// are free. It is computed on read from the host's own routes and sockets, never stored, so it cannot
// drift from what is deployed.
type Allocation struct {
	Band      [2]int       `json:"band"`
	Held      []PortHolder `json:"held"` // routed ports and who holds them, sorted by port
	Free      []int        `json:"free"` // free ports within the band, ascending
	ScannedAt string       `json:"scannedAt"`
}

// Proposal answers "which port should service <id> take?" — derived from the host's actual state, so
// a new service gets a provably free port instead of a template default. It never resolves silently:
// when the desired port is taken, Conflict names the holder (when it can) and Granted is a free port
// instead, so setup can say what happened rather than install a service that will not start.
type Proposal struct {
	ID       string `json:"id"`
	Desired  int    `json:"desired,omitempty"`  // 0 = no preference
	Granted  int    `json:"granted"`            // free port to use; 0 only when the band is exhausted
	Conflict string `json:"conflict,omitempty"` // service already holding Desired, if it was taken
	InBand   bool   `json:"inBand"`             // whether Granted lies within the managed band
	Note     string `json:"note"`               // human-readable resolution, always set
}

// ─── actual-state sources ────────────────────────────────────────────────────

// routedPorts reads port -> holder ids off the host's Caddy routes (via the shared atlas snapshot).
func routedPorts() map[int][]string {
	byPort := map[int][]string{}
	for _, n := range snapshot() {
		if n.Port != 0 {
			byPort[n.Port] = append(byPort[n.Port], n.ID)
		}
	}
	for p := range byPort {
		sort.Strings(byPort[p])
	}
	return byPort
}

// listeningPorts returns the set of TCP ports that are actually in LISTEN state on this host,
// straight from the kernel via /proc/net/tcp{,6}. This catches a port that is bound but not routed
// through Caddy — the dashboard's own 8781, say — so it is never proposed as free. Unreadable procfs
// (e.g. in a sandbox) yields the empty set: the allocation degrades to the routes it can see.
func listeningPorts() map[int]bool {
	const tcpListen = "0A" // include/net/tcp_states.h: TCP_LISTEN
	ports := map[int]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(raw), "\n")[1:] { // skip the header row
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

// ─── pure derivation (unit-testable without a host) ──────────────────────────

// allocationFrom builds the ledger from already-read occupancy. routed names the holders; bound is
// the raw set of listening ports. A band port is free only when neither routed nor bound.
func allocationFrom(lo, hi int, routed map[int][]string, bound map[int]bool, scannedAt string) Allocation {
	held := make([]PortHolder, 0, len(routed))
	for p, ids := range routed {
		cp := make([]string, len(ids))
		copy(cp, ids)
		held = append(held, PortHolder{Port: p, IDs: cp})
	}
	sort.Slice(held, func(i, j int) bool { return held[i].Port < held[j].Port })

	free := make([]int, 0)
	for p := lo; p <= hi; p++ {
		if _, routedHere := routed[p]; routedHere {
			continue
		}
		if bound[p] {
			continue
		}
		free = append(free, p)
	}
	return Allocation{Band: [2]int{lo, hi}, Held: held, Free: free, ScannedAt: scannedAt}
}

// proposeFrom picks a free port for id from already-read occupancy. A service that already holds a
// port keeps it (re-proposing is idempotent): its own port is never counted against it.
func proposeFrom(id string, desired, lo, hi int, routed map[int][]string, bound map[int]bool) Proposal {
	// The port id itself already holds, if any — so it is not reported as its own conflict.
	own := 0
	for p, ids := range routed {
		for _, h := range ids {
			if h == id {
				own = p
			}
		}
	}

	// holderOf names who occupies p other than id, and whether p is occupied at all (routed OR bound).
	holderOf := func(p int) (string, bool) {
		others := make([]string, 0, 2)
		for _, h := range routed[p] {
			if h != id {
				others = append(others, h)
			}
		}
		occupied := len(others) > 0 || (bound[p] && p != own)
		return strings.Join(others, ", "), occupied
	}
	lowestFree := func() int {
		for p := lo; p <= hi; p++ {
			if _, taken := holderOf(p); !taken {
				return p
			}
		}
		return 0
	}

	p := Proposal{ID: id, Desired: desired}

	// Idempotent: id already holds a port and asks for nothing different — keep it.
	if own != 0 && (desired == 0 || desired == own) {
		p.Granted, p.InBand = own, own >= lo && own <= hi
		p.Note = id + " already holds port " + strconv.Itoa(own) + "."
		return p
	}

	if desired != 0 {
		if holder, taken := holderOf(desired); taken {
			free := lowestFree()
			p.Granted, p.Conflict, p.InBand = free, holder, free != 0
			switch {
			case free == 0 && holder != "":
				p.Note = "Port " + strconv.Itoa(desired) + " is held by " + holder + " and no port is free in band " + bandStr(lo, hi) + "."
			case free == 0:
				p.Note = "Port " + strconv.Itoa(desired) + " is in use and no port is free in band " + bandStr(lo, hi) + "."
			case holder != "":
				p.Note = "Port " + strconv.Itoa(desired) + " is held by " + holder + "; " + strconv.Itoa(free) + " is free."
			default:
				p.Note = "Port " + strconv.Itoa(desired) + " is in use (bound but not routed); " + strconv.Itoa(free) + " is free."
			}
			return p
		}
		if desired < lo || desired > hi {
			p.Granted, p.InBand = desired, false
			p.Note = "Port " + strconv.Itoa(desired) + " is free but outside the managed band " + bandStr(lo, hi) + "."
			return p
		}
		p.Granted, p.InBand = desired, true
		p.Note = "Port " + strconv.Itoa(desired) + " is free."
		return p
	}

	// No preference — the lowest free port in the band.
	free := lowestFree()
	p.Granted, p.InBand = free, free != 0
	if free == 0 {
		p.Note = "No port is free in band " + bandStr(lo, hi) + "."
	} else {
		p.Note = strconv.Itoa(free) + " is free."
	}
	return p
}

// ─── public entry points (read the host, then derive) ────────────────────────

// AllocationNow reads the host's routes and bound sockets and returns the current port ledger.
func AllocationNow() Allocation {
	lo, hi := band()
	return allocationFrom(lo, hi, routedPorts(), listeningPorts(), time.Now().UTC().Format(time.RFC3339))
}

// Propose returns a free port for id, preferring desired (0 = no preference). Derived from the
// host's actual state; it reserves nothing — the port becomes id's the moment its route is written.
func Propose(id string, desired int) Proposal {
	lo, hi := band()
	return proposeFrom(id, desired, lo, hi, routedPorts(), listeningPorts())
}
