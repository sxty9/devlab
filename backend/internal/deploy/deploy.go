// Package deploy is the delivery-to-host machinery (S11): honest detection, build AS THE USER
// (root never builds), install-only via the pinned root wrapper, an honest running gate, gap
// detection, the self-repo handover (B-2) and the prod send (implemented, fixture-tested, NOT
// armed in this phase). devlabd itself NEVER calls systemd-run — the handover lives in the
// root wrapper.
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"devlab/backend/internal/model"
)

// Kind classifies a repo for delivery.
type Kind string

const (
	KindService Kind = "service"
	// KindUndeclared is the repository that says NOTHING: no ./service CLI, no cmd/<id>d daemon, and no
	// declaration either way. It used to be called a "library" — a PROPERTY read off an ABSENCE, and the
	// axiom forbids exactly that: "nicht anwendbar" may only follow from a proven property of the thing,
	// and the lack of an installation is not such a property but a deficiency. The difference is not
	// academic. A repository whose service had not been built yet was classified a library, every stage
	// skipped it with that as its attested reason, and its absence from production went unnoticed for
	// seven days (sxgate, measured 2026-08-09). A repository that genuinely delivers nothing SAYS so in
	// holistic-service.json (deliver:false) and is KindExcluded — proven, evidenced, and silent by right.
	KindUndeclared    Kind = "undeclared"
	KindExcluded      Kind = "excluded"
	KindNonconforming Kind = "nonconforming"
	KindTemplate      Kind = "template"
)

// DeclarationFileName is the optional per-repo declaration (REQ-028.2): named values instead of
// code, in the repo root. It is the ONLY way a repo states delivery deviations — there are no
// per-repo scripts (B-44).
const DeclarationFileName = "holistic-service.json"

// templatePlaceholderID is the pristine template's placeholder service id: a repo still carrying
// permissions/<placeholder>.json IS the template and never counts as a delivery gap (REQ-029.4).
const templatePlaceholderID = "myservice"

// Detection is a classification WITH its evidence: the conforming ./service CLI (template
// convention) or backend/cmd/<id>d for a service; the declaration file for excluded.
type Detection struct {
	Kind     Kind
	Evidence string
	// ID is the detected service id (unit and route stem): the permissions-manifest stem, the
	// cmd/<id>d stem, or the repo directory name. Empty for non-services.
	ID string
	// Decl is the parsed declaration file, when present.
	Decl *DeclarationFile
	// Edge is HOW this delivery is reached from outside, read off the declaration (never off the
	// repository's name). Empty for a repo that is not a service at all.
	Edge EdgeRole
}

// EdgeRole is how a delivery is reached from OUTSIDE the host — a closed list of three, and the Go twin
// of the shell's SETUP_EDGE_ROLES.
//
// It exists because that property used to be derived from the repository NAME: `if [ "$repo" = devlab ]`
// decided, in two places in devlab-exec, whether a delivery got the uniform `/api/services/<id>/*` route
// or the whole `/api/*` prefix. A name comparison can only ever hold for ONE member, so when a second one
// legitimately grew into the same role (holistic, the landscape dashboard) the two collided in a single
// site block and the alphabet decided which one's API existed at all (measured on production 2026-08-09).
// The property therefore belongs to the PACKAGE and is declared, not computed.
type EdgeRole string

const (
	// EdgeRoleService is the uniform shape and the DEFAULT for every package that says nothing: reached at
	// /api/services/<id>/* under the dashboard's hostname. The default is deliberately the unprivileged
	// one — silence must never buy a whole hostname.
	EdgeRoleService EdgeRole = "service"
	// EdgeRoleApplication owns a hostname of its own and the whole /api/* space beneath it, and serves its
	// own face at the root of that name. It does NOT carry the landscape's services.
	EdgeRoleApplication EdgeRole = "application"
	// EdgeRoleDashboard is an application that additionally carries the landscape's uniform services —
	// they are reached under ITS hostname. There is exactly one per instance.
	EdgeRoleDashboard EdgeRole = "dashboard"
)

// IsRootApplication reports whether this role answers under a hostname of its own — the property both
// installers branch on, asked once here rather than spelled out as a two-value comparison at each site.
func (r EdgeRole) IsRootApplication() bool {
	return r == EdgeRoleApplication || r == EdgeRoleDashboard
}

// DeclarationFile is the optional per-repo declaration (REQ-028.2 — named values instead of
// code): holistic-service.json in the repo root.
type DeclarationFile struct {
	Deliver  *bool  `json:"deliver,omitempty"`
	Port     int    `json:"port,omitempty"`
	Health   string `json:"health,omitempty"`
	Artifact *struct {
		Binary string `json:"binary,omitempty"`
		Web    string `json:"web,omitempty"`
	} `json:"artifact,omitempty"`
	// Edge states how the delivery is reached from outside. It belongs HERE, in the one file a repo
	// states delivery deviations in, and not in a second file of its own: a parallel declaration file
	// beside this one would be an ähnliches Geschwister to it (Keine Redundanz / Reuse before Build).
	// Neither field names a hostname or a zone — WHICH name a root application answers to is the HOST's
	// to declare, in runtime configuration, and a package can never state it.
	Edge *struct {
		Role string `json:"role,omitempty"`
		// ServeRoot is the absolute directory this package's own face is served from on the target host.
		// Absent means the landscape's convention (/opt/<id>/www); it is stated only by a package whose
		// face genuinely lives elsewhere. The installer re-validates it against the service's own
		// territory either way — the declaration says WHERE, it does not grant where.
		ServeRoot string `json:"serveRoot,omitempty"`
	} `json:"edge,omitempty"`
}

// edgeRoleOf reads the declared edge role, defaulting to the unprivileged EdgeRoleService when the
// declaration is absent or says nothing. An unknown word is NOT defaulted: it is returned as an error so
// the caller reports a nonconforming repository rather than guessing what its author meant.
func edgeRoleOf(decl *DeclarationFile) (EdgeRole, error) {
	if decl == nil || decl.Edge == nil || decl.Edge.Role == "" {
		return EdgeRoleService, nil
	}
	switch r := EdgeRole(decl.Edge.Role); r {
	case EdgeRoleService, EdgeRoleApplication, EdgeRoleDashboard:
		return r, nil
	default:
		return "", fmt.Errorf("edge.role is %q, which is not one of %q, %q or %q",
			decl.Edge.Role, EdgeRoleService, EdgeRoleApplication, EdgeRoleDashboard)
	}
}

// Detect classifies one repo directory with evidence. Order of judgement: the pristine template,
// an explicit exclusion, a conforming service (the ./service CLI or a cmd/<id>d daemon), a repo
// that CLAIMS delivery without conforming, and — only then — a library.
func Detect(repoDir string) (Detection, error) {
	if fi, err := os.Stat(repoDir); err != nil || !fi.IsDir() {
		return Detection{}, fmt.Errorf("deploy: not a repo directory: %s", repoDir)
	}

	decl, declErr := readDeclaration(repoDir)

	// 1) The pristine template: still carries the placeholder id — never a service, never a gap.
	if _, err := os.Stat(filepath.Join(repoDir, "permissions", templatePlaceholderID+".json")); err == nil {
		return Detection{Kind: KindTemplate, Decl: decl,
			Evidence: "pristine service template (placeholder permissions/" + templatePlaceholderID + ".json)"}, nil
	}

	// 2) A malformed declaration is a violation, not a guess.
	if declErr != nil {
		return Detection{Kind: KindNonconforming,
			Evidence: DeclarationFileName + " is unreadable: " + declErr.Error()}, nil
	}

	// 3) Explicitly declared not-to-deliver: abbildbar, and it suppresses every attempt.
	if decl != nil && decl.Deliver != nil && !*decl.Deliver {
		return Detection{Kind: KindExcluded, Decl: decl,
			Evidence: DeclarationFileName + " declares deliver:false"}, nil
	}

	// 3a) A declared edge role we do not know is a violation, not a guess. It is judged AFTER the
	// exclusion above, because a repository that delivers nothing is never reached from outside and its
	// edge role would be a field about nothing.
	role, roleErr := edgeRoleOf(decl)
	if roleErr != nil {
		return Detection{Kind: KindNonconforming, Decl: decl,
			Evidence: DeclarationFileName + ": " + roleErr.Error() +
				" — how a delivery is reached from outside is stated, never inferred"}, nil
	}

	// 4) A conforming service: the uniform ./service CLI, or the template daemon layout.
	id := manifestID(repoDir)
	if fi, err := os.Stat(filepath.Join(repoDir, "service")); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		if id == "" {
			id = filepath.Base(repoDir)
		}
		return Detection{Kind: KindService, ID: id, Decl: decl, Edge: role,
			Evidence: "./service CLI (template convention)"}, nil
	}
	if cmdID, dir := daemonCmdID(repoDir); cmdID != "" {
		return Detection{Kind: KindService, ID: cmdID, Decl: decl, Edge: role,
			Evidence: dir + "/" + cmdID + "d (template daemon layout)"}, nil
	}

	// 5) Claims delivery (declaration present) or looks daemon-shaped, but conforms to nothing:
	// a "Code-Struktur" violation to report — never a special path (REQ-028.4).
	if decl != nil {
		return Detection{Kind: KindNonconforming, Decl: decl,
			Evidence: DeclarationFileName + " present, but neither a ./service CLI nor a cmd/<id>d daemon conforms to the shared structure"}, nil
	}
	if hasNonconformingCmd(repoDir) {
		return Detection{Kind: KindNonconforming,
			Evidence: "has a cmd/ tree, but neither a ./service CLI nor a cmd/<id>d daemon conforms to the shared structure"}, nil
	}

	// 6) Everything else has said NOTHING — and silence is not a property. The repository carries no
	// deliverable daemon AND no declaration that it is not supposed to carry one, so what is true of it
	// is unknown, not "no service". It is reported as the deficiency it is; one line of
	// holistic-service.json (deliver:false) turns it into a proven, evidenced, silent exclusion.
	return Detection{Kind: KindUndeclared,
		Evidence: "neither delivers nor declares itself: no ./service CLI, no cmd/<id>d daemon, and no " +
			DeclarationFileName + " saying it delivers nothing — an absence is not a proven property, so this " +
			"is reported rather than passed over; declare deliver:false to exclude it by right"}, nil
}

func readDeclaration(repoDir string) (*DeclarationFile, error) {
	raw, err := os.ReadFile(filepath.Join(repoDir, DeclarationFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d DeclarationFile
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// manifestID returns the service id from permissions/<id>.json, the template's source of truth.
func manifestID(repoDir string) string {
	entries, err := os.ReadDir(filepath.Join(repoDir, "permissions"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() {
			return name
		}
	}
	return ""
}

// daemonCmdID finds the template daemon layout cmd/<id>d (repo root or backend/), returning the
// id and which cmd tree carried it.
func daemonCmdID(repoDir string) (id, dir string) {
	for _, base := range []string{"backend/cmd", "cmd"} {
		entries, err := os.ReadDir(filepath.Join(repoDir, base))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasSuffix(e.Name(), "d") && len(e.Name()) > 1 {
				return strings.TrimSuffix(e.Name(), "d"), base
			}
		}
	}
	return "", ""
}

func hasNonconformingCmd(repoDir string) bool {
	for _, base := range []string{"backend/cmd", "cmd"} {
		entries, err := os.ReadDir(filepath.Join(repoDir, base))
		if err != nil {
			continue
		}
		if len(entries) > 0 {
			return true
		}
	}
	return false
}

// DeliveredUnitName reads the unit name the delivered setup product carries, mirroring the shell rule
// in devlab-setup-lib.sh (setup_delivered_unit_name): a service whose unit is legitimately named
// otherwise ships setup/<name>.service, and the installer reads that name rather than assuming
// <service>.service. It returns the bare unit name (no ".service"), or "" when the package ships no
// unit or is ambiguous — in which case the caller falls back to the service id. The precedence:
// setup/<service>.service (the conventional generated name) wins; else a lone *.service is taken; more
// than one and none conventional is ambiguous and yields "" (never a guess). This is the Go twin the
// port source consults so it can recognise the service's OWN unit when deciding a conflict.
func DeliveredUnitName(artifactDir, service string) string {
	setup := filepath.Join(artifactDir, "setup")
	if fi, err := os.Stat(setup); err != nil || !fi.IsDir() {
		return ""
	}
	if _, err := os.Stat(filepath.Join(setup, service+".service")); err == nil {
		return service
	}
	entries, err := os.ReadDir(setup)
	if err != nil {
		return ""
	}
	var only string
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name, ok := strings.CutSuffix(e.Name(), ".service"); ok {
			count++
			only = name
		}
	}
	if count != 1 {
		return ""
	}
	return only
}

// unitListenPortRe and unitFlagPortRe read the loopback port a delivered unit's ExecStart binds, in the
// two forms that occur across the build kinds. The go-daemon template writes `--listen 127.0.0.1:<port>`
// (a colon, matched first because a loopback bind is unambiguous); a python-app names the port the
// interpreter's way, `--port <port>` or `--port=<port>` (uvicorn). Both are read for exactly the reason
// setup_unit_listen_port reads them in the shell — the honest gate must dial the port the UNIT binds.
var (
	unitListenPortRe = regexp.MustCompile(`127\.0\.0\.1:(\d{1,5})`)
	unitFlagPortRe   = regexp.MustCompile(`--port[=\s]+(\d{1,5})`)
)

// DeliveredUnitPort reads the loopback port the delivered unit's ExecStart binds, the Go twin of the
// shell setup_unit_listen_port the production receiver already dials. It is consulted BESIDE
// DeliveredUnitName, from the same delivered setup product (setup/<unit>.service), so the dev gate proves
// the port the DELIVERED unit binds rather than one the chain's port allocation computed — the third
// place a value the package NAMES was being RECHNET instead (unit name PR 106, probed unit PR 110). It
// returns 0 when the package ships no such unit, the unit names no port, or the value is out of range —
// the caller then falls back to the allocation, exactly as before, for a service that fixes no port.
func DeliveredUnitPort(artifactDir, unit string) int {
	if unit == "" {
		return 0
	}
	raw, err := os.ReadFile(filepath.Join(artifactDir, "setup", unit+".service"))
	if err != nil {
		return 0
	}
	for _, re := range []*regexp.Regexp{unitListenPortRe, unitFlagPortRe} {
		if m := re.FindSubmatch(raw); m != nil {
			if p, err := strconv.Atoi(string(m[1])); err == nil && p > 0 && p <= 65535 {
				return p
			}
		}
	}
	return 0
}

// Gap names a repo whose delivery is possible but not yet set up — "delivery not yet set up"
// is DISTINCT from "no service" and never green (REQ-029). Template repos do not count;
// excluded never triggers an attempt.
type Gap struct {
	Repo   string
	Kind   Kind
	Detail string
}

// EdgeHostsDir is where a host declares WHICH hostname each root application answers to — one file per
// application, named after it. The Go twin of the shell's SETUP_EDGE_HOSTS_DIR, resolved the same way:
// an instance-neutral default under the shared holistic config dir, with the same env seam the shell
// carries so a test can point both halves at one fixture directory.
func EdgeHostsDir() string {
	if d := os.Getenv("DEVLAB_EDGE_HOSTS_DIR"); d != "" {
		return d
	}
	base := os.Getenv("DEVLAB_HOLISTIC_DIR")
	if base == "" {
		base = "/etc/holistic"
	}
	return filepath.Join(base, "edge", "hosts")
}

// EdgeHostDeclared reports whether THIS host names a hostname for the root application <id>. It only
// asks whether the declaration is there and non-empty; the shell reader (setup_edge_host) remains the
// one that judges its shape and hands it to the edge. Reading it here is what lets the deficiency be
// NAMED before a delivery is attempted, instead of only when the delivery dies on it.
func EdgeHostDeclared(id string) bool {
	if id == "" {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(EdgeHostsDir(), id))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return true
		}
	}
	return false
}

// FindGaps derives the delivery gaps from detections, observed ports and the hostnames THIS host
// declares: a detected service with no routed port has no delivery path yet (REQ-029.1); a nonconforming
// repo is reported as the "Code-Struktur" violation it is (REQ-028.4); an undeclared repo is reported
// because nothing about it has been proven; and a ROOT APPLICATION for which this host names no hostname
// is reported as delivered-but-unreachable. The template and repos that DECLARE deliver:false produce no
// gap — those two rest on evidence, which is what buys silence.
//
// declaredHost answers "does this host name a hostname for <id>?" — EdgeHostDeclared in operation, a
// fixture in a test. It is a parameter rather than a direct read so the judgement can be measured
// without a host, exactly like allocs.
func FindGaps(dets map[string]Detection, allocs []model.PortAllocation, declaredHost func(id string) bool) []Gap {
	if declaredHost == nil {
		declaredHost = EdgeHostDeclared
	}
	repos := make([]string, 0, len(dets))
	for r := range dets {
		repos = append(repos, r)
	}
	sort.Strings(repos)

	var gaps []Gap
	for _, repo := range repos {
		d := dets[repo]
		switch d.Kind {
		case KindService:
			// A ROOT APPLICATION is reached under a name of its own, so a host that names none for it
			// serves it to nobody — and it would otherwise be the quietest possible failure: the program
			// runs, the port answers, every stage is green, and the application is simply not on the
			// internet. It is reported BEFORE the routed-port gap because it is the more specific fact
			// about the same repository.
			if d.Edge.IsRootApplication() && !declaredHost(d.ID) {
				gaps = append(gaps, Gap{Repo: repo, Kind: KindService,
					Detail: "delivered, but unreachable: '" + repo + "' declares itself a root application (edge.role=" +
						string(d.Edge) + "), and a root application is reached under a hostname — this host declares none for it in " +
						filepath.Join(EdgeHostsDir(), d.ID) + " (declare it with devlab-install-recv --edge-host " + d.ID + "=<name>)"})
				continue
			}
			if _, routed := routedPortOf(allocs, d.ID); !routed {
				gaps = append(gaps, Gap{Repo: repo, Kind: KindService,
					Detail: "delivery not yet set up: service detected (" + d.Evidence + ") but no route/port is provisioned"})
			}
		case KindNonconforming:
			gaps = append(gaps, Gap{Repo: repo, Kind: KindNonconforming,
				Detail: "violates the Code-Struktur axiom: " + d.Evidence})
		case KindUndeclared:
			gaps = append(gaps, Gap{Repo: repo, Kind: KindUndeclared,
				Detail: "nothing is proven about this repository: " + d.Evidence})
		}
	}
	return gaps
}

func routedPortOf(allocs []model.PortAllocation, service string) (int, bool) {
	if service == "" {
		return 0, false
	}
	for _, a := range allocs {
		if a.Routed && a.Service == service {
			return a.Port, true
		}
	}
	return 0, false
}
