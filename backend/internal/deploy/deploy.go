// Package deploy is the delivery-to-host machinery (S11): honest detection, build AS THE USER
// (root never builds), install-only via the pinned root wrapper, an honest running gate, gap
// detection, the self-repo handover (B-2) and the prod send (implemented, fixture-tested, NOT
// armed in this phase). devlabd itself NEVER calls systemd-run — the handover lives in the
// root wrapper.
package deploy

import (
	"context"

	"devlab/backend/internal/model"
	"devlab/backend/internal/workspace"
)

// Kind classifies a repo for delivery.
type Kind string

const (
	KindService       Kind = "service"
	KindLibrary       Kind = "library"
	KindExcluded      Kind = "excluded"
	KindNonconforming Kind = "nonconforming"
	KindTemplate      Kind = "template"
)

// Detection is a classification WITH its evidence: the conforming ./service CLI (template
// convention) or backend/cmd/<id>d for a service; the declaration file for excluded.
type Detection struct {
	Kind     Kind
	Evidence string
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
}

// Detect classifies one repo directory with evidence.
func Detect(repoDir string) (Detection, error) {
	panic("TODO(B5)")
}

// Build builds the repo's artifact AS THE USER via devlab-exec artifact-build (root never
// builds) and returns the artifact directory.
func Build(ctx context.Context, ex *workspace.Executor, repo string) (string, error) {
	panic("TODO(B5)")
}

// RootInstaller is the thin seam over `sudo devlab-install` (the pinned wrapper).
type RootInstaller interface {
	Install(ctx context.Context, repo, artifactDir, env string, port int, handover bool) error
}

// PortSource proposes a validated free port for first-time setup.
type PortSource interface {
	Propose(ctx context.Context) (int, error)
}

// Outcome is the honest install result.
type Outcome struct {
	Installed bool
	Running   bool
	Port      int
	Detail    string
}

// DeliverDev delivers a repo to dev: foreign repo ⇒ install + unit restart inside the wrapper;
// self repo ⇒ install + --handover (B-2). First-time setup of template-conforming services is
// done by the WRAPPER from root-owned inline templates with validated values (port from
// atlas.Propose) — no per-repo script, no repo code as root (REQ-028).
func DeliverDev(ctx context.Context, ri RootInstaller, ports PortSource, d Detection, repo, artifactDir string) (Outcome, error) {
	panic("TODO(B5)")
}

// VerifyRunning is the honest gate (F10): unit active AND port held, otherwise failed
// (REQ-044.3). Never green by default.
func VerifyRunning(ctx context.Context, d Detection, port int) error {
	panic("TODO(B5)")
}

// Gap names a repo whose delivery is possible but not yet set up — "delivery not yet set up"
// is DISTINCT from "no service" and never green (REQ-029). Template repos do not count;
// excluded never triggers an attempt.
type Gap struct {
	Repo   string
	Kind   Kind
	Detail string
}

// FindGaps derives the delivery gaps from detections and observed ports.
func FindGaps(dets map[string]Detection, allocs []model.PortAllocation) []Gap {
	panic("TODO(B5)")
}

// ProdConfig configures the prod send: rsync to an rrsync staging path behind a
// forced-command receiver; the TARGET is server-side, never in the request.
type ProdConfig struct {
	RsyncTarget string
}

// SendProd ships an artifact to prod staging. In this phase it is exercised ONLY against a
// fixture target — never armed.
func SendProd(ctx context.Context, cfg ProdConfig, repo, artifactDir string) error {
	panic("TODO(B5)")
}
