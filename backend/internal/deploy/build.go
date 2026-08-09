package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"devlab/backend/internal/workspace"
)

// ArtifactDirName is the fixed artifact directory the per-user build produces inside a working
// tree (written by the devlab-exec artifact-build verb; consumed install-only by the wrapper).
const ArtifactDirName = ".mercury-artifact"

// Build builds the repo's artifact AS THE USER via devlab-exec artifact-build (root never
// builds, REQ-028.5) in the working tree wt and returns the artifact directory. The build output
// is folded into the error for honest context — a failed build names what the toolchain said.
func Build(ctx context.Context, ex workspace.Executor, wt string) (string, error) {
	out, err := ex.ArtifactBuild(ctx, wt)
	if err != nil {
		// The failed step's output IS the reason. When the toolchain said nothing, name exactly that —
		// which step, which status, no output — rather than folding an empty string that reads as a
		// blank protocol (task point 2, "Kein stummes Ausbleiben").
		if strings.TrimSpace(out) == "" {
			return "", fmt.Errorf("deploy: artifact build failed with no output: %w", err)
		}
		return "", fmt.Errorf("deploy: artifact build failed: %w\n%s", err, tail(out, 4000))
	}
	art := filepath.Join(wt, ArtifactDirName)
	if fi, statErr := os.Stat(art); statErr != nil || !fi.IsDir() {
		return "", fmt.Errorf("deploy: build reported success but produced no %s in %s", ArtifactDirName, wt)
	}
	return art, nil
}

// tail keeps error payloads portioned: the last n bytes carry the failure, the rest is noise.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
