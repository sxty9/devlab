// The two api-side callers of discover.Owner, held to the ONE answer an unconfigured instance may
// give: the named error. Neither may turn an unset namespace into an empty one.
//
// Before the repair the accessor answered `string` alone and both of these sites ignored the
// contract stated in its doc comment: the constitution was named "/axioms" (a LEADING SLASH, which
// the store reads as a filesystem remote — a stray directory of that name would have been served as
// this instance's constitution), and the chain's target resolution reported a message of its own
// that no caller could match on.
package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"devlab/backend/internal/axiomrepo"
	"devlab/backend/internal/discover"
	"devlab/backend/internal/model"
	"devlab/backend/internal/statepath"
)

// noNamespace unsets every variable that could name a namespace or an explicit repository, so the
// unconfigured instance is what the test observes (and never the developer's own environment).
func noNamespace(t *testing.T) {
	t.Helper()
	t.Setenv("DEVLAB_GH_OWNER", "")
	t.Setenv("DEVLAB_AXIOMS_REPO", "")
	t.Setenv("DEVLAB_AXIOMS_DIR", "")
	t.Setenv("DEVLAB_AXIOMS_TOKEN_USER", "")
	t.Setenv("DEVLAB_RUNS_TOKEN_USER", "")
	t.Setenv("DEVLAB_RUNS_USER", "")
}

// TestAxiomsRepoRefusesWithoutANamespace is call site one: the constitution's repository name.
func TestAxiomsRepoRefusesWithoutANamespace(t *testing.T) {
	noNamespace(t)

	full, err := axiomsRepo()
	if !errors.Is(err, discover.ErrOwnerUnset) {
		t.Fatalf("axiomsRepo() error = %v, want discover.ErrOwnerUnset", err)
	}
	if full != "" {
		t.Errorf("axiomsRepo() = %q, want no repository at all", full)
	}
	if strings.HasPrefix(full, "/") {
		t.Errorf("axiomsRepo() = %q — a leading slash is read as a FILESYSTEM remote, so an unset owner would serve a local directory as the constitution", full)
	}

	// With a namespace it names the repository beside the instance's other ones — the fallback the
	// refusal above replaces, not removes.
	t.Setenv("DEVLAB_GH_OWNER", "an-org")
	full, err = axiomsRepo()
	if err != nil || full != "an-org/axioms" {
		t.Errorf("axiomsRepo() = %q,%v, want an-org/axioms and no error", full, err)
	}

	// An explicit repository still wins outright — it needs no namespace of its own.
	t.Setenv("DEVLAB_GH_OWNER", "")
	t.Setenv("DEVLAB_AXIOMS_REPO", "other-org/constitution")
	full, err = axiomsRepo()
	if err != nil || full != "other-org/constitution" {
		t.Errorf("axiomsRepo() with an explicit repository = %q,%v", full, err)
	}
}

// TestServerWithoutANamespaceHasNoConstitutionStore is the consequence at the boot seam: no
// namespace means there is no constitution repository, so the store is absent and says so. "Not
// configured" must stay distinguishable from "no axioms" and from "unreachable" (REQ-001).
func TestServerWithoutANamespaceHasNoConstitutionStore(t *testing.T) {
	noNamespace(t)
	t.Setenv("DEVLAB_STATIC_DIR", t.TempDir())

	s := New(nil, &statepath.Paths{Root: t.TempDir()})
	if s.axioms != nil {
		t.Fatalf("the constitution store was built as %q — without a namespace there is no repository to build it from", s.axioms.FullName())
	}
	if _, err := s.axioms.List(context.Background(), ""); !errors.Is(err, axiomrepo.ErrNoStore) {
		t.Errorf("List error = %v, want axiomrepo.ErrNoStore — never an empty constitution", err)
	}
	if _, _, err := s.axioms.Get(context.Background(), "architecture/minimalism/x.md"); !errors.Is(err, axiomrepo.ErrNoStore) {
		t.Errorf("Get error = %v, want axiomrepo.ErrNoStore", err)
	}
}

// TestChainFullNameReportsTheNamedError is call site two: the chain's resolution of a target that
// GitHub does not (yet) know. It already refused — but with a message of its own, so no caller could
// tell "no namespace configured" from any other failure. Now the named error is wrapped.
func TestChainFullNameReportsTheNamedError(t *testing.T) {
	noNamespace(t)
	// The repo set the resolution reads is a seam; an empty answer is "GitHub does not know this id".
	old := runnerRepoSet
	runnerRepoSet = func(context.Context, string, string) ([]model.Repo, error) { return nil, nil }
	t.Cleanup(func() { runnerRepoSet = old })

	d := &ChainDeps{s: &Server{}, benches: map[string]*repoBench{}, full: map[string]string{}}
	full, err := d.fullName(context.Background(), "brand-new-service")
	if !errors.Is(err, discover.ErrOwnerUnset) {
		t.Fatalf("fullName error = %v, want it to wrap discover.ErrOwnerUnset", err)
	}
	if full != "" {
		t.Errorf("fullName = %q, want no full name — %q would be a namespace nobody configured", full, full)
	}

	// With a namespace the same call resolves into it, so the creating stage can name the target.
	t.Setenv("DEVLAB_GH_OWNER", "an-org")
	d = &ChainDeps{s: &Server{}, benches: map[string]*repoBench{}, full: map[string]string{}}
	if full, err = d.fullName(context.Background(), "brand-new-service"); err != nil || full != "an-org/brand-new-service" {
		t.Errorf("fullName = %q,%v, want an-org/brand-new-service", full, err)
	}
}
