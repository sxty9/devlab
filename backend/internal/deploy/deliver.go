package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/atlas"
)

// installWrapper is the pinned root wrapper (uniform install path, like the devlab-exec pin in
// package workspace). Root only installs validated artifacts — it never builds and never runs
// repo-provided code (E §7.4).
const installWrapper = "/usr/local/sbin/devlab-install"

// RootInstaller is the thin seam over `sudo devlab-install` (the pinned wrapper). Install returns
// both halves' result: the InstallResult carries the UI outcome the wrapper reports, and err is the
// program/UI failure (a UI half that could not be built in fails the install with its named reason).
type RootInstaller interface {
	Install(ctx context.Context, repo, artifactDir, env string, port int, handover bool) (InstallResult, error)
}

// UIState is the outcome of a service's dashboard-UI half — one belegter Zustand, never silent.
type UIState string

const (
	UINone      UIState = "none"            // the service ships no ui/ — the half is absent, said so
	UIBuilt     UIState = "built"           // ui wired into the dashboard and the shared build passed
	UIForeign   UIState = "foreign-blocked" // this ui verified, but another service blocks the shared build
	UIFailed    UIState = "failed"          // this service's ui does not build — a named stage failure
	UIUnconfig  UIState = "unconfigured"    // ui present but no dashboard checkout configured — a deficiency
	UIPlanned   UIState = "planned"         // --check dry-run reached the ui step
	UIWouldFail UIState = "would-fail"      // --check only: the relocation would carry an outside-tree config into the dashboard
)

// InstallResult is what the wrapper reports beyond the raw error: the UI half's outcome, parsed off
// the wrapper's `MERCURY-UI:` line, so the calling stage can report program AND ui, each on its own.
type InstallResult struct {
	UI       UIState
	UIDetail string
}

// PortSource answers "which port does this service use?" from observed state: an already-routed
// service keeps its port; a first-time setup gets a validated free one (desired honored, an
// occupied desired port is an error naming the holder + a free proposal — REQ-044).
type PortSource interface {
	PortFor(ctx context.Context, service string, desired int) (port int, firstTime bool, err error)
}

// Outcome is the honest install result — both halves a service needs, each reported on its own.
type Outcome struct {
	Installed bool
	Running   bool
	Port      int
	Detail    string
	// UI is the dashboard-UI half's outcome; UIDetail carries the reason for the foreign-blocked case.
	UI       UIState
	UIDetail string
}

// Honest no-attempt reasons: these repos are never installed, and the caller can prove WHY
// (not-applicable only with evidence, REQ-031.3).
var (
	ErrExcluded      = errors.New("deploy: excluded by declaration (deliver:false) — no delivery attempt")
	ErrNotAService   = errors.New("deploy: not a service — nothing to deliver")
	ErrTemplateRepo  = errors.New("deploy: the pristine service template is never delivered")
	ErrNonconforming = errors.New("deploy: repo does not conform to the shared service structure (Code-Struktur violation) — no special path exists")

	// ErrIDMismatch names the split the install wrapper itself cannot see. The wrapper derives the
	// unit, the service account, the route and the binary name from the repository name it is handed,
	// while the port ledger and the running gate key on the DETECTED service id. When the two
	// disagree, ONE setup call would create a unit under one name and prove it under another: the
	// port is reserved for the id, the unit and the route are built for the repository name, and the
	// gate then probes a foreign or absent unit — a green delivery over a service that is not there,
	// or a second unit shadowing an existing one. There is nothing to guess between two keys, so the
	// divergence is refused BEFORE any install.
	ErrIDMismatch = errors.New("deploy: repository name and detected service id disagree — the install takes ONE key")
)

// DeliverDev delivers a FOREIGN repo to dev: ONE generic install-only path through the pinned
// root wrapper (unit restart happens inside the wrapper), then the honest running gate (F10).
// First-time setup of template-conforming services is done by the WRAPPER from root-owned inline
// templates with validated values (port from the PortSource proposal) — no per-repo script, no
// repo code as root (REQ-028). The self repo takes SelfInstallAndHandover instead.
func DeliverDev(ctx context.Context, ri RootInstaller, ports PortSource, gate Gate, d Detection, repo, artifactDir string) (Outcome, error) {
	switch d.Kind {
	case KindExcluded:
		return Outcome{Detail: d.Evidence}, ErrExcluded
	case KindLibrary:
		return Outcome{Detail: d.Evidence}, ErrNotAService
	case KindTemplate:
		return Outcome{Detail: d.Evidence}, ErrTemplateRepo
	case KindNonconforming:
		return Outcome{Detail: d.Evidence}, fmt.Errorf("%w: %s", ErrNonconforming, d.Evidence)
	case KindService:
		// fall through
	default:
		return Outcome{}, fmt.Errorf("deploy: unknown detection kind %q", d.Kind)
	}

	// ONE key for the whole setup: port reservation, wrapper install and running proof must all
	// name the same service, so a divergence between the repository name and the detected id is a
	// named refusal before any effect (ErrIDMismatch). What follows uses `key` exclusively.
	if d.ID != repo {
		return Outcome{Detail: fmt.Sprintf("repository %q, detected service id %q (%s)", repo, d.ID, d.Evidence)},
			fmt.Errorf("%w: repository %q vs service id %q — rename the repository or its rights manifest so both name the service",
				ErrIDMismatch, repo, d.ID)
	}
	key := d.ID

	desired := 0
	if d.Decl != nil {
		desired = d.Decl.Port
	}
	port, _, err := ports.PortFor(ctx, key, desired)
	if err != nil {
		// An occupied desired port is REJECTED before any install: the error names the holder
		// and proposes a free port (REQ-044.2). No wrapper call happens.
		return Outcome{Detail: err.Error()}, err
	}

	res, err := ri.Install(ctx, key, artifactDir, "dev", port, false)
	if err != nil {
		// A reported UI state means the program install reached the ui step, i.e. the program itself
		// installed — the failure is the UI half (unconfigured/failed). Both halves are reported, and
		// the stage still fails: a ui that could not be built in is a deficiency, never green (K-4).
		o := Outcome{Port: port, Detail: err.Error(), UI: res.UI, UIDetail: res.UIDetail}
		if res.UI != "" {
			o.Installed = true
		}
		return o, fmt.Errorf("deploy: install of %s failed: %w", key, err)
	}

	// The honest gate (F10): "installed and started" only when the unit is ACTIVE and the port
	// is HELD — otherwise the setup FAILED, never green by default (REQ-044.3, K-4).
	if err := gate.VerifyRunning(ctx, key, port); err != nil {
		return Outcome{Installed: true, Port: port, UI: res.UI, UIDetail: res.UIDetail,
			Detail: "installed, but the service is not running: " + err.Error()}, fmt.Errorf("deploy: failed setup of %s: %w", key, err)
	}
	return Outcome{Installed: true, Running: true, Port: port, UI: res.UI, UIDetail: res.UIDetail,
		Detail: "installed and started (active, holding :" + strconv.Itoa(port) + ")"}, nil
}

// SelfInstallAndHandover installs the self repo's artifact immediately and hands the restart to
// a transient unit OUTSIDE the devlabd cgroup — install + `systemd-run … devlab-restart-when-free`
// happen in ONE wrapper call (B-2); devlabd itself never calls systemd-run. A failed handover is
// a failed stage, never an inline restart (K-2). The running proof for self is the restart marker
// plus the next boot — there is no port probe here.
func SelfInstallAndHandover(ctx context.Context, ri RootInstaller, repo, artifactDir string) error {
	if _, err := ri.Install(ctx, repo, artifactDir, "dev", 0, true); err != nil {
		return fmt.Errorf("deploy: self install/handover failed (stage fails, no inline restart): %w", err)
	}
	return nil
}

// ─── the production seams ────────────────────────────────────────────────────

// SudoInstaller is the production RootInstaller: `sudo -n <wrapper> <repo> <artifact> <env>
// [--port n] [--handover]`. The sudoers grant pins the service user to exactly this wrapper.
type SudoInstaller struct{}

func (SudoInstaller) Install(ctx context.Context, repo, artifactDir, env string, port int, handover bool) (InstallResult, error) {
	args := []string{"-n", installWrapper, repo, artifactDir, env}
	if port > 0 {
		args = append(args, "--port", strconv.Itoa(port))
	}
	if handover {
		args = append(args, "--handover")
	}
	out, err := exec.CommandContext(ctx, "sudo", args...).CombinedOutput()
	res := parseUILine(string(out))
	if err != nil {
		return res, fmt.Errorf("%w: %s", err, tail(strings.TrimSpace(string(out)), 2000))
	}
	return res, nil
}

// parseUILine reads the wrapper's single `MERCURY-UI: <state> [| detail]` line off its output — the
// machine-readable half-report that lets the stage name program AND ui separately. Absent (a wrapper
// that predates the ui half, or a program that never reached the ui step) yields the zero UIState.
func parseUILine(out string) InstallResult {
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "MERCURY-UI:")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		state, detail, _ := strings.Cut(rest, "|")
		return InstallResult{UI: UIState(strings.TrimSpace(state)), UIDetail: strings.TrimSpace(detail)}
	}
	return InstallResult{}
}

// LivePorts is the production PortSource: the ledger is derived fresh from the host's routes and
// bound sockets on every call — no stored port state (REQ-044.4).
type LivePorts struct{}

func (LivePorts) PortFor(ctx context.Context, service string, desired int) (int, bool, error) {
	allocs, err := atlas.AllocationsNow()
	if err != nil {
		return 0, false, err
	}
	if port, ok := atlas.RoutedPort(allocs, service); ok {
		return port, false, nil // already provisioned — idempotent, its own port is never a conflict
	}
	port, err := atlas.ProposeDesired(allocs, atlas.BandFromEnv(), desired)
	if err != nil {
		return 0, true, err
	}
	return port, true, nil
}

// ─── the honest running gate (F10) ───────────────────────────────────────────

// Gate proves a service is up: unit active AND port held. The probes are seams so the gate is
// testable without a host; the defaults ask systemd and dial the loopback port.
type Gate struct {
	UnitActive func(ctx context.Context, unit string) error
	PortHeld   func(ctx context.Context, port int) error
	Wait       time.Duration // how long the service gets to come up
	Poll       time.Duration
}

// DefaultGate probes the real host.
func DefaultGate() Gate {
	return Gate{
		UnitActive: func(ctx context.Context, unit string) error {
			out, err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).CombinedOutput()
			if err != nil {
				return fmt.Errorf("unit %s is not active: %s", unit, strings.TrimSpace(string(out)))
			}
			return nil
		},
		PortHeld: func(ctx context.Context, port int) error {
			d := net.Dialer{Timeout: 2 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				return fmt.Errorf("port %d is not held: %w", port, err)
			}
			return conn.Close()
		},
		Wait: 15 * time.Second,
		Poll: time.Second,
	}
}

// VerifyRunning polls until the unit is active AND (when known) the port is held, or Wait runs
// out — then it returns the LAST honest failure. A restart is not a start (F10): success is
// proven, never assumed.
func (g Gate) VerifyRunning(ctx context.Context, unit string, port int) error {
	if g.Wait <= 0 {
		g.Wait = time.Millisecond
	}
	if g.Poll <= 0 {
		g.Poll = time.Millisecond
	}
	deadline := time.Now().Add(g.Wait)
	var last error
	for {
		last = g.probeOnce(ctx, unit, port)
		if last == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(g.Poll):
		}
	}
}

func (g Gate) probeOnce(ctx context.Context, unit string, port int) error {
	if g.UnitActive != nil {
		if err := g.UnitActive(ctx, unit); err != nil {
			return err
		}
	}
	if port > 0 && g.PortHeld != nil {
		if err := g.PortHeld(ctx, port); err != nil {
			return err
		}
	}
	if g.UnitActive == nil && (port <= 0 || g.PortHeld == nil) {
		return errors.New("gate has no probes — refusing to report running")
	}
	return nil
}

// VerifyRunning is the default-gate convenience over a Detection (unit name == service id).
func VerifyRunning(ctx context.Context, d Detection, port int) error {
	return DefaultGate().VerifyRunning(ctx, d.ID, port)
}
