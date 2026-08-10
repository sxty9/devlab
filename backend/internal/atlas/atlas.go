// Package atlas derives the Holistic service landscape from what is actually deployed on this host.
//
// The landscape is declared nowhere: there is no service registry, no OpenAPI, no .well-known. But
// every service drops two artifacts at install time, and between them they ARE the registry:
//
//	/etc/holistic/permissions.d/<id>.json  the node set + the hp_* rights it declares
//	/etc/caddy/conf.d/<id>.caddy           the address book: id -> 127.0.0.1:<port>
//
// Both are world-readable (0644 in 0755 dirs), so devlabd reads them directly as the unprivileged
// `devlab` user — no sudo, no devlab-exec verb, no sudoers change.
//
// Nothing here is stored. The graph is a projection of the host's own configuration, recomputed on
// read behind a short TTL, so it cannot drift from what is deployed: a service that runs
// `service setup` appears in Atlas without Atlas changing, because the drop-in IS the registration.
//
// This is the structural layer. Modelling the processes that run ACROSS these services (BPMN, lanes
// = services) builds on it and is not yet here.
package atlas

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A deployed Holistic service.
type Node struct {
	ID string `json:"id"` // permissions-manifest stem == caddy drop-in stem == ServicePlugin.id
	// Port it answers on, from the Caddy drop-in. 0 when it has no route.
	Port int `json:"port"`
	// The hp_* groups it declares in its rights manifest.
	Rights []string `json:"rights"`
	// It ships a rights manifest (Uniformity requires one of every service).
	HasManifest bool `json:"hasManifest"`
	// It is routed by the Caddy edge, i.e. reachable at /api/services/<id>/.
	HasRoute bool `json:"hasRoute"`
	// The repo it is built from, when the caller can see one by that name. Empty otherwise —
	// which is itself a finding (deployed, but outside DevLab's repo catalog).
	Repo string `json:"repo"`
}

// An inconsistency between what is deployed, what is declared, and what DevLab can see.
type Finding struct {
	Severity string   `json:"severity"` // "warn" | "error"
	Message  string   `json:"message"`
	Nodes    []string `json:"nodes"`
}

type Graph struct {
	Nodes     []Node    `json:"nodes"`
	Findings  []Finding `json:"findings"`
	ScannedAt string    `json:"scannedAt"`
}

// manifest is the shape every service drops for privleg.
type manifest struct {
	Service    string `json:"service"`
	Categories []struct {
		Permissions []struct {
			Group string `json:"group"`
		} `json:"permissions"`
	} `json:"categories"`
}

var (
	idRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}$`)
	portRe = regexp.MustCompile(`reverse_proxy\s+127\.0\.0\.1:(\d{2,5})`)
)

func permsDir() string {
	if d := os.Getenv("DEVLAB_HOLISTIC_PERMS"); d != "" {
		return d
	}
	return "/etc/holistic/permissions.d"
}

func caddyDir() string {
	if d := os.Getenv("DEVLAB_CADDY_CONF"); d != "" {
		return d
	}
	return "/etc/caddy/conf.d"
}

// ─── host reflection ────────────────────────────────────────────────────────

// hostNodes reads the deployed truth off this host. A source that cannot be read yields no nodes
// rather than an error: Atlas degrades to what it can see, and says so through its findings.
func hostNodes() map[string]*Node {
	nodes := map[string]*Node{}

	node := func(id string) *Node {
		if n, ok := nodes[id]; ok {
			return n
		}
		n := &Node{ID: id, Rights: []string{}}
		nodes[id] = n
		return n
	}

	for _, e := range readDir(permsDir()) {
		id, ok := stem(e, ".json")
		if !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(permsDir(), e))
		if err != nil {
			continue
		}
		var m manifest
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		// The manifest names itself; the filename is only a convention.
		if m.Service != "" && idRe.MatchString(m.Service) {
			id = m.Service
		}
		n := node(id)
		n.HasManifest = true
		for _, c := range m.Categories {
			for _, p := range c.Permissions {
				if p.Group != "" {
					n.Rights = append(n.Rights, p.Group)
				}
			}
		}
		sort.Strings(n.Rights)
	}

	for _, dir := range caddyRouteDirs() {
		for _, e := range readDir(dir) {
			id, ok := stem(e, ".caddy")
			if !ok {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e))
			if err != nil {
				continue
			}
			n := node(id)
			n.HasRoute = true
			// A port already read stands: the same id is not expected on two shelves, and if it were, the
			// first reading is the one that answered.
			if n.Port == 0 {
				if m := portRe.FindSubmatch(raw); m != nil {
					n.Port, _ = strconv.Atoi(string(m[1]))
				}
			}
		}
	}

	return nodes
}

// caddyRouteDirs — WHERE a delivered route can stand on this host, in the order it is read.
//
// A route used to be one file, flat in the route directory. It is now filed by KIND: a uniform service's
// naked `/api/services/<id>/*` fragment goes on the `services/` shelf, a root application's whole site
// block on the `apps/` shelf — because a Caddyfile treats the two differently, and one flat directory made
// them indistinguishable, which is how two applications came to share a single site block and fight over
// `/api/*` (measured on production 2026-08-09).
//
// This reflection has to FOLLOW the routes, not the other way round. Reading only the flat directory on a
// migrated host would report every service as unrouted with port 0 — and since the production roster is
// derived from exactly these routed ids, the entire landscape would read as empty while every service ran.
// The flat directory stays in the list because a grown, hand-built edge still keeps its routes there:
// Atlas reports what is deployed, never what ought to be.
func caddyRouteDirs() []string {
	base := caddyDir()
	return []string{base, filepath.Join(base, "services"), filepath.Join(base, "apps")}
}

func readDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// stem returns the filename without its suffix, if it is a plausible service id.
func stem(name, suffix string) (string, bool) {
	if !strings.HasSuffix(name, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(name, suffix)
	if !idRe.MatchString(id) {
		return "", false
	}
	return id, true
}

// ─── evaluation ─────────────────────────────────────────────────────────────

// findings compares what is deployed against what is declared. The store stays passive: the graph
// holds facts, and every judgement about them is made here, outside it.
func findings(nodes []Node, catalogKnown bool) []Finding {
	var out []Finding

	byPort := map[int][]string{}
	for _, n := range nodes {
		if n.Port != 0 {
			byPort[n.Port] = append(byPort[n.Port], n.ID)
		}
		switch {
		case n.HasRoute && !n.HasManifest:
			out = append(out, Finding{
				Severity: "warn",
				Message:  n.ID + " ist geroutet, liefert aber kein Rechte-Manifest.",
				Nodes:    []string{n.ID},
			})
		case n.HasManifest && !n.HasRoute:
			out = append(out, Finding{
				Severity: "warn",
				Message:  n.ID + " liefert ein Rechte-Manifest, ist aber nicht geroutet.",
				Nodes:    []string{n.ID},
			})
		}
		// Only meaningful when the repo catalog actually resolved. On a GitHub outage every node
		// would otherwise look uncatalogued, and Atlas would indict the whole landscape. The wording
		// stays neutral about the cause: it may be an untagged repo, or simply one this caller has
		// no GitHub access to.
		if catalogKnown && n.Repo == "" {
			out = append(out, Finding{
				Severity: "warn",
				Message:  n.ID + " ist deployed, aber als Repository nicht sichtbar.",
				Nodes:    []string{n.ID},
			})
		}
	}

	for port, ids := range byPort {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		out = append(out, Finding{
			Severity: "error",
			Message:  "Port " + strconv.Itoa(port) + " wird von " + strings.Join(ids, " und ") + " beansprucht.",
			Nodes:    ids,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "error" // errors first
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// ─── assembly + cache ───────────────────────────────────────────────────────

// The host config is the same for every caller, so it is cached once rather than per user. Only the
// repo linkage is per-caller, and that is merged in on read. Caching is not storing: the TTL is
// short and the source of truth stays the host's own configuration.
const ttl = 60 * time.Second

var (
	mu       sync.Mutex
	cached   map[string]*Node
	cachedAt time.Time
)

func snapshot() map[string]*Node {
	mu.Lock()
	defer mu.Unlock()
	if cached == nil || time.Since(cachedAt) > ttl {
		cached = hostNodes()
		cachedAt = time.Now()
	}
	out := make(map[string]*Node, len(cached))
	for id, n := range cached {
		c := *n
		// append() to a nil slice yields nil when there is nothing to copy, and a nil slice
		// marshals to JSON `null` rather than `[]`. Every list this API returns is a list.
		c.Rights = make([]string, len(n.Rights))
		copy(c.Rights, n.Rights)
		out[id] = &c
	}
	return out
}

// RoutedServiceIDs returns the ids of every service the edge actually routes on this host — the
// services reachable at /api/services/<id>/, read straight off the Caddy drop-ins the same way the
// port ledger is (derived from the host's own configuration, never stored, recomputed behind the
// short TTL). It is the DERIVED backbone of the production landscape roster: a service is part of the
// landscape because the edge routes it, not because the delivery history happens to remember shipping
// it. The two load-bearing members that carry no route of their own — devlab itself and the central
// dashboard — are NOT here (they have no /api/services/<id>/ route); the caller adds them. A source
// that cannot be read yields fewer ids rather than an error, exactly as the rest of Atlas degrades.
func RoutedServiceIDs() []string {
	byID := snapshot()
	out := make([]string, 0, len(byID))
	for id, n := range byID {
		if n.HasRoute {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// GraphFor builds the landscape as this caller can see it. repoIDs are the repos DevLab resolved for
// them from GitHub — used only to link a deployed service back to its repo, never to filter the host
// nodes, so a service the caller cannot see on GitHub is still shown as deployed. catalogKnown says
// whether that resolution actually succeeded; when it did not, findings that reason about the repo
// catalog are suppressed rather than reported against every node.
func GraphFor(repoIDs []string, catalogKnown bool) Graph {
	known := make(map[string]bool, len(repoIDs))
	for _, id := range repoIDs {
		known[id] = true
	}

	byID := snapshot()
	nodes := make([]Node, 0, len(byID))
	for _, n := range byID {
		if known[n.ID] {
			n.Repo = n.ID
		}
		nodes = append(nodes, *n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	return Graph{
		Nodes:     nodes,
		Findings:  findings(nodes, catalogKnown),
		ScannedAt: time.Now().UTC().Format(time.RFC3339),
	}
}
