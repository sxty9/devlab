package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// prodRepoRe mirrors the wrapper's name grammar: nothing else ever reaches a path.
var prodRepoRe = regexp.MustCompile(`^[a-z][a-z0-9-]{2,30}$`)

// ProdConfig configures the prod send. The target comes EXCLUSIVELY from server-side
// configuration — never from a request — so a manipulated caller cannot redirect a prod deploy
// to a host it controls (E §7.4).
type ProdConfig struct {
	// RsyncTarget is the rsync destination root: "user@host:path" against the rrsync-confined
	// staging behind the forced-command receiver in production; a local directory in tests.
	RsyncTarget string
	// RsyncArgs overrides the transport flags (test seam; empty means the production set).
	RsyncArgs []string
	// Trigger fires the install-only receiver on the target (the forced-command ssh call).
	// nil = NOT ARMED: the artifact is staged, nothing is installed — the state of this phase.
	Trigger func(ctx context.Context, repo string) error
}

// SendProd ships a PREBUILT artifact to the prod staging (install-only doctrine: this host built
// nothing as root, the target builds nothing at all). In this phase it is exercised ONLY against
// a fixture target — the Trigger stays nil, so no receiver ever fires (NOT armed). Prod is fed
// exclusively from the default branch — that policy sits with the caller of this function.
func SendProd(ctx context.Context, cfg ProdConfig, repo, artifactDir string) error {
	if !prodRepoRe.MatchString(repo) {
		return fmt.Errorf("deploy: invalid repo name %q", repo)
	}
	if strings.TrimSpace(cfg.RsyncTarget) == "" {
		return fmt.Errorf("deploy: prod target is not configured (server-side)")
	}
	if fi, err := os.Stat(artifactDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("deploy: no prebuilt artifact at %s", artifactDir)
	}

	args := cfg.RsyncArgs
	if args == nil {
		args = []string{"-az", "--delete"}
	}
	// Trailing slashes: ship the CONTENT of artifactDir into <target>/<repo>/.
	dest := strings.TrimRight(cfg.RsyncTarget, "/") + "/" + repo + "/"
	full := append(append([]string{}, args...), strings.TrimRight(artifactDir, "/")+"/", dest)
	if out, err := exec.CommandContext(ctx, "rsync", full...).CombinedOutput(); err != nil {
		return fmt.Errorf("deploy: prod send failed: %w: %s", err, tail(strings.TrimSpace(string(out)), 2000))
	}

	if cfg.Trigger == nil {
		return nil // staged only — the prod path is implemented but NOT armed in this phase
	}
	if err := cfg.Trigger(ctx, repo); err != nil {
		return fmt.Errorf("deploy: prod install trigger failed: %w", err)
	}
	return nil
}
