package api

import (
	"os"
	"strings"

	"devlab/backend/internal/discover"

	"devlab/backend/internal/statepath"
)

// Where the constitution lives. Instance-neutral: every value is configurable, and the defaults name a
// repository beside the other Holistic ones rather than anything machine-specific.

// axiomsRepo is the owner/repo holding the constitution (DEVLAB_AXIOMS_REPO overrides).
func axiomsRepo() string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_AXIOMS_REPO")); v != "" {
		return v
	}
	return discover.Owner() + "/axioms"
}

// axiomsDir is the local working copy of that repository.
func axiomsDir(p *statepath.Paths) string {
	if v := strings.TrimSpace(os.Getenv("DEVLAB_AXIOMS_DIR")); v != "" {
		return v
	}
	if p != nil {
		return p.AxiomsClone()
	}
	return ""
}

// axiomsTokenUser is the linked account whose token pushes constitution changes. It defaults to the
// autonomous runner's token user, so one linked account serves the whole pipeline.
func axiomsTokenUser() string {
	for _, k := range []string{"DEVLAB_AXIOMS_TOKEN_USER", "DEVLAB_RUNS_TOKEN_USER", "DEVLAB_RUNS_USER"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
