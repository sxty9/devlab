package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/atlas"
	"devlab/backend/internal/model"
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
	//
	// The gate probes the unit that was actually INSTALLED. Its name is carried by the delivered
	// setup product (setup/<name>.service) and may legitimately differ from the repository name — a
	// dashboard whose unit is `<repo>-dashboard` is the standing case. The gate reads that name from
	// the ONE place that determines it (DeliveredUnitName, the twin of the installer's
	// setup_delivered_unit_name), instead of deriving it a second time from the repository name. The
	// installer already made this correction (PR 106); deriving the name again here made the gate ask
	// systemd for `holistic` while the installer had installed and started `holistic-dashboard`, so a
	// LIVE delivery was booked as failed. Absent a delivered unit the name falls back to the service
	// id, and the behaviour is unchanged.
	unit := DeliveredUnitName(artifactDir, key)
	if unit == "" {
		unit = key
	}
	if err := gate.VerifyRunning(ctx, unit, port); err != nil {
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
//
// Unit is the service's OWN unit name as read from the delivered setup product (e.g. holistic-dashboard),
// which may differ from the service id. It matters for the idempotency below: a service whose unit is
// already running holds its port even when NO edge route names it (bound-but-unrouted), so that port is
// the service's OWN, never a conflict — the very case that made `holistic`, running under its divergently
// named unit, look like a foreign occupant of 8770 and blocked its own renewal. "" falls back to the
// service id. Active reports whether that unit is running; the default asks systemd, and a test supplies
// its own seam.
type LivePorts struct {
	Unit   string
	Active func(ctx context.Context, unit string) bool
}

func (lp LivePorts) PortFor(ctx context.Context, service string, desired int) (int, bool, error) {
	allocs, err := atlas.AllocationsNow()
	if err != nil {
		return 0, false, err
	}
	if port, ok := atlas.RoutedPort(allocs, service); ok {
		return port, false, nil // already provisioned by route — idempotent, its own port is never a conflict
	}
	unit := lp.Unit
	if unit == "" {
		unit = service
	}
	ownActive := desired != 0 && lp.isActive(ctx, unit)
	return decidePort(allocs, atlas.BandFromEnv(), desired, ownActive)
}

func (lp LivePorts) isActive(ctx context.Context, unit string) bool {
	if lp.Active != nil {
		return lp.Active(ctx, unit)
	}
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}

// decidePort is the pure port decision (testable without a host). A desired port that is bound but NOT
// routed, while the service's OWN unit is active, is the own service's port — idempotent (firstTime=false),
// never a conflict: the running own unit is what holds it. A port held by a ROUTED (thus named, foreign)
// service stays a conflict, and so does a bound port when the own unit is NOT active — only the delivering
// service's own running unit earns the bypass. Everything else follows ProposeDesired unchanged: a free
// desired port is granted, an occupied one is the *OccupiedError that names the holder and proposes a free
// port.
func decidePort(allocs []model.PortAllocation, band atlas.Band, desired int, ownActive bool) (int, bool, error) {
	if desired != 0 && ownActive {
		if holder, taken := atlas.Occupied(allocs, desired); taken && holder == "" {
			return desired, false, nil
		}
	}
	port, err := atlas.ProposeDesired(allocs, band, desired)
	if err != nil {
		return 0, true, err
	}
	return port, true, nil
}

// ProdSetupPort resolves the port the production unit will bind on a FIRST-TIME setup: the service's
// declared port when it has one (the coordinated, cross-machine value every host agrees on), else a
// free port proposed from the atlas band. It is baked into the delivered unit and route at build time;
// the receiver installs those bytes unchanged (it generates nothing), and the honest running gate on
// the target catches a genuine port collision as a failed delivery rather than a silent one.
func ProdSetupPort(ctx context.Context, d Detection) (int, error) {
	desired := 0
	if d.Decl != nil {
		desired = d.Decl.Port
	}
	port, _, err := LivePorts{}.PortFor(ctx, d.ID, desired)
	return port, err
}

// ─── the honest running gate (F10) ───────────────────────────────────────────

// LogLookup is the result of reading a unit's journal for the cause of a failed start. It separates
// two things a bare string conflated: the actual last log line (the service's OWN reason), and whether
// the gate could read the journal AT ALL. When the gate lacks journal access, journalctl prints "No
// journal files were opened due to insufficient permissions" — that is the gate's OWN missing right,
// not the service's failure reason, and must never be reported as the cause (task point 2/3).
type LogLookup struct {
	Line     string // the unit's real last log line, or "" when the journal held none
	Readable bool   // true only when the gate could actually read the journal; false = its own right is missing
}

// Gate proves a service is up AND STAYS up: unit active AND port held, held CONTINUOUSLY over a dwell.
// The probes are seams so the gate is testable without a host; the defaults ask systemd, dial the
// loopback port, and read the unit's own journal for the reason a failed start died.
type Gate struct {
	UnitActive func(ctx context.Context, unit string) error
	PortHeld   func(ctx context.Context, port int) error
	LogReason  func(ctx context.Context, unit string) LogLookup // the unit's log line that carries the cause, or an unreadable verdict
	Wait       time.Duration                                    // how long the service gets to COME UP
	Dwell      time.Duration                                    // how long it must then STAY up, continuously
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
		LogReason: func(ctx context.Context, unit string) LogLookup {
			// stdout (the log itself) and stderr (journalctl's own complaints) are kept apart, so the
			// gate never mistakes "I may not read this journal" for the service's last log line. The old
			// CombinedOutput() folded the two, which is exactly how "No journal files were opened due to
			// insufficient permissions" ended up reported as holistic's failure reason (2026-08-08).
			cmd := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", "5", "--no-pager", "-o", "cat")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if journalUnreadable(err, stderr.String()) {
				return LogLookup{Readable: false}
			}
			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			for i := len(lines) - 1; i >= 0; i-- {
				if s := strings.TrimSpace(lines[i]); s != "" {
					return LogLookup{Line: s, Readable: true}
				}
			}
			return LogLookup{Readable: true}
		},
		Wait:  15 * time.Second,
		Dwell: 12 * time.Second,
		Poll:  time.Second,
	}
}

// VerifyRunning proves a service is up AND STAYS up (F10): success is proven, never assumed, and a
// restart is not a start. It runs in two phases with the SAME signals — unit active AND (when known)
// the port held — applied over TIME rather than at one instant:
//  1. COME UP: poll until the service is up, or Wait runs out (then return the last honest failure).
//  2. STAY UP: require it to REMAIN up continuously for Dwell. A service that starts and dies (a
//     restart loop) drops its port every cycle, so a continuous port-held requirement catches it; if
//     it drops during the dwell it did not stay, and that is a FAILED delivery.
//
// A failure carries the line from the unit's own log that names the cause, so the caller records the
// real reason (e.g. "no JWT secret …") and never a bare "not active".
func (g Gate) VerifyRunning(ctx context.Context, unit string, port int) error {
	if g.Wait <= 0 {
		g.Wait = time.Millisecond
	}
	if g.Poll <= 0 {
		g.Poll = time.Millisecond
	}
	// phase 1 — come up
	deadline := time.Now().Add(g.Wait)
	var last error
	for {
		last = g.probeOnce(ctx, unit, port)
		if last == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return g.withReason(ctx, unit, last)
		}
		select {
		case <-ctx.Done():
			return g.withReason(ctx, unit, last)
		case <-time.After(g.Poll):
		}
	}
	// phase 2 — stay up: continuous over the dwell, or it was a restart loop, not a start
	if g.Dwell <= 0 {
		return nil
	}
	dwellEnd := time.Now().Add(g.Dwell)
	for time.Now().Before(dwellEnd) {
		select {
		case <-ctx.Done():
			return g.withReason(ctx, unit, ctx.Err())
		case <-time.After(g.Poll):
		}
		if err := g.probeOnce(ctx, unit, port); err != nil {
			return g.withReason(ctx, unit,
				fmt.Errorf("came up but did not stay (a restart loop, not a start): %w", err))
		}
	}
	return nil
}

// withReason appends the unit's own log line to a gate failure, so a failed delivery names the cause
// from the service's protocol rather than a bare probe error. When the journal could not be READ at
// all, the failure says so explicitly ("reason unknown, log not readable") instead of dressing the
// gate's own missing right up as the service's cause — a wrong cause that points elsewhere is worse
// than an admitted unknown (task point 2/3).
func (g Gate) withReason(ctx context.Context, unit string, err error) error {
	if err == nil || g.LogReason == nil {
		return err
	}
	look := g.LogReason(ctx, unit)
	if !look.Readable {
		return fmt.Errorf("%w — reason unknown, log not readable: the gate lacks journal access for unit %s (add it to the systemd-journal group)", err, unit)
	}
	if reason := strings.TrimSpace(look.Line); reason != "" {
		return fmt.Errorf("%w — last log line: %s", err, reason)
	}
	return err
}

// journalUnreadable reports whether journalctl failed because the gate itself may not read the unit's
// journal, as opposed to the journal simply being empty. The signature is journalctl's own on a host
// where the service account is not in the systemd-journal group: it prints "No journal files were
// opened due to insufficient permissions" (or a plain permission-denied) to STDERR. That message is
// the gate's deficiency, never the delivered service's failure cause, so it is recognised here and
// turned into an honest "unreadable" verdict rather than passed off as a log line.
func journalUnreadable(runErr error, stderr string) bool {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "insufficient permissions"),
		strings.Contains(s, "no journal files were opened"),
		strings.Contains(s, "permission denied"):
		return true
	}
	// journalctl exited non-zero AND complained on stderr for some other reason (e.g. the journal is
	// unavailable): the reason it would have carried cannot be trusted, so treat the log as unreadable
	// rather than invent a cause. A clean non-zero exit with no stderr (an empty journal) stays
	// readable — there is simply no line, which withReason reports as no appended cause.
	return runErr != nil && strings.TrimSpace(stderr) != ""
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
