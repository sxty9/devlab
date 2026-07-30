// The delivery chain's ONE repository-placing call, held to the contract of discover.Owner: an
// unconfigured namespace is refused with the named error, before any GitHub call.
//
// Before the repair this site read `discover.Owner()` inline as an argument. With no namespace
// configured that argument was the EMPTY STRING, and GitHub's create-repository call reads an empty
// owner as "the account this token belongs to" — so a repository the chain created would silently
// land in a foreign namespace (REQ-033.6 places it in the instance's own).
package deliver

import (
	"context"
	"errors"
	"testing"

	"devlab/backend/internal/discover"
)

func TestCreateRepoRefusesWithoutAConfiguredNamespace(t *testing.T) {
	t.Setenv("DEVLAB_GH_OWNER", "")

	// The token is deliberately unusable: the refusal must happen BEFORE the network, so a test of
	// it never needs one. A GitHub error instead of the named one would prove the opposite.
	full, err := liveGitHub{token: "not-a-token"}.CreateRepo(context.Background(), "brand-new-service", true)
	if !errors.Is(err, discover.ErrOwnerUnset) {
		t.Fatalf("CreateRepo error = %v, want discover.ErrOwnerUnset", err)
	}
	if full != "" {
		t.Errorf("CreateRepo full name = %q, want none", full)
	}
}
