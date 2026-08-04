package api

// The production path was never run as a WHOLE against a real repository — only each block against
// placeholders — so the FORMS in which the blocks hand each other their values went unchecked, and a
// chain of form-mismatches surfaced one at a time in production (02.08.2026): the full service name
// where the short was expected, then the remote-tracking ref where a local branch was expected.
//
// This test drives the whole path — from a MERGED entry in the delivery ledger, through the REAL
// MaintainProd → DeployProd, against a REAL local repository (a bare "remote" with a committed merged
// state, cloned into the runner's workspace) — up to IMMEDIATELY before the send to the foreign host.
// Everything before the send runs for real: resolve the merged default branch, fetch it, check it out
// DETACHED, detect the service, name the artifact. The send and its receiver stay placeholders, and
// the build is substituted too (it runs root-only through the pinned wrapper, which a hermetic test
// has not) — each substitution is named where it is made.
//
// The values that travel this path, and the FORM each block expects (WHAT-4):
//   - service name: FULL "owner/repo" from the ledger → the workbench and github.DefaultBranch take
//     it whole; the workspace and the send take the SHORT bare id (their name grammar rejects a slash).
//   - branch: the default-branch NAME ("main") to github.DefaultBranch; the merged state as a
//     REMOTE-TRACKING ref ("origin/main") to the DETACHED checkout — never a local branch.
//   - paths: the working tree (absolute) to fetch/checkout/detect/build; the built artifact directory
//     (<wt>/.mercury-artifact) to the send.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/deliver"
	"devlab/backend/internal/deploy"
	"devlab/backend/internal/github"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/statepath"
	"devlab/backend/internal/workspace"
)

// gitAt runs a git command in dir with a fixed identity, failing the test on error.
func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedRealWorkspace lays down what the runner's workspace looks like when the production send reaches
// it: a real clone at <wsRoot>/<user>/<short> whose origin points at a bare "remote" carrying a
// committed merged state on the default branch. The committed tree is a conforming service (an
// executable ./service file) so Detect classifies it as a service and the path proceeds to build+send.
// Returns the merged commit SHA and the working-tree path.
func seedRealWorkspace(t *testing.T, wsRoot, user, short string) (mergedSHA, wt string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitAt(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")

	src := filepath.Join(root, "src")
	gitAt(t, root, "init", "-q", "-b", "main", "src")
	svc := filepath.Join(src, "service")
	if err := exec.Command("sh", "-c", "printf '#!/bin/sh\\n' > "+svc+" && chmod +x "+svc).Run(); err != nil {
		t.Fatal(err)
	}
	gitAt(t, src, "add", "-A")
	gitAt(t, src, "commit", "-qm", "merged state")
	gitAt(t, src, "remote", "add", "origin", remote)
	gitAt(t, src, "push", "-q", "origin", "main")
	mergedSHA = gitAt(t, src, "rev-parse", "HEAD")

	userDir := filepath.Join(wsRoot, user)
	if err := exec.Command("mkdir", "-p", userDir).Run(); err != nil {
		t.Fatal(err)
	}
	wt = filepath.Join(userDir, short)
	gitAt(t, userDir, "clone", "-q", remote, short)
	// Park the working tree off the merged ref, as a fresh clone's default checkout would be its own
	// local branch — the detached checkout of origin/main is what must move it onto the merged commit.
	return mergedSHA, wt
}

// prodSendCapture records the values the (placeholdered) send received, so the test can assert the
// FORM each block handed on.
type prodSendCapture struct {
	called   bool
	repo     string
	artifact string
	wt       string
}

// TestMaintainProd_WholePathAgainstRealRepo runs the production path end to end against a real local
// repository, up to immediately before the send, and asserts every value arrives in the expected form.
func TestMaintainProd_WholePathAgainstRealRepo(t *testing.T) {
	s, wsRoot := prodNameServer(t)
	s.paths = &statepath.Paths{Root: t.TempDir()}
	const user, full, short = "runner", "acme/svc", "svc"
	mergedSHA, wt := seedRealWorkspace(t, wsRoot, user, short)

	// The only network read before the send is the merged repo's default branch — served by a fixture.
	restore := github.SetAPIBase(fixtureGitHub(t))
	defer restore()

	// Production is armed (target, receiver, key all named) so the path reaches the send seam; the
	// values themselves are never used because the send is a placeholder.
	t.Setenv("DEVLAB_RUNS_PROD_TARGET", "deploy@host:/srv/stage")
	t.Setenv("DEVLAB_RUNS_PROD_RECV", "deploy@host")
	t.Setenv("DEVLAB_RUNS_PROD_KEY", filepath.Join(t.TempDir(), "key"))

	merged := time.Now().UTC().Add(-time.Hour)
	if err := s.deliveries.Put(runs.Delivery{
		ID: "dlv_1", Repo: full, Branch: "fix/x-1", ExecutionID: "exec_1",
		CreatedAt: merged.Add(-time.Hour), MergedAt: &merged,
	}); err != nil {
		t.Fatal(err)
	}

	sent := &prodSendCapture{}
	deps := &ChainDeps{
		s: s, user: user, benches: map[string]*repoBench{}, full: map[string]string{},
		// Run the real git side directly — the hermetic test has no sudo wrapper, but it has a real
		// clone with a real origin, so fetch and the DETACHED checkout of the merged state run for real.
		execBypass: true,
		// The build runs root-only through the pinned wrapper (absent here): substitute it with a
		// placeholder that produces a real artifact directory FROM the checked-out working tree, so the
		// path/name handed to the send is genuinely derived from the merged state that was checked out.
		prodBuild: func(_ context.Context, _ workspace.Executor, wt string) (string, error) {
			art := filepath.Join(wt, deploy.ArtifactDirName)
			if err := exec.Command("mkdir", "-p", art).Run(); err != nil {
				return "", err
			}
			return art, nil
		},
		// The setup emission runs root-only through the pinned wrapper (absent here) and resolves a port
		// via atlas — both substituted with a placeholder, like the build, so the path reaches the send.
		prodEmit: func(_ context.Context, _ workspace.Executor, _, _ string, _ deploy.Detection) error {
			return nil
		},
		// The send to the foreign host and its receiver stay placeholders (WHAT-2): record the values
		// and stop here — this is the "immediately before the send" boundary.
		prodSend: func(_ context.Context, _ deploy.ProdConfig, repo, artifact string) error {
			sent.called, sent.repo, sent.artifact, sent.wt = true, repo, artifact, wt
			return nil
		},
	}

	if err := deliver.MaintainProd(context.Background(), chainDeploy{d: deps}, s.deliveries, s.prodState, s.results, s.runNotices, nil); err != nil {
		t.Fatalf("the whole path up to the send must complete against a real repository: %v", err)
	}

	// The send boundary was reached — everything before it produced valid values.
	if !sent.called {
		d, _, _ := s.deliveries.ByID("dlv_1")
		t.Fatalf("the path never reached the send — it stopped earlier: %q", d.ProdFailedReason)
	}
	// The send takes the SHORT bare id (its grammar rejects a slash), reduced from the ledger's full name.
	if sent.repo != short {
		t.Fatalf("the send received repo %q, want the short bare id %q reduced from %q", sent.repo, short, full)
	}
	// The artifact path is the built directory inside the working tree.
	if want := filepath.Join(wt, deploy.ArtifactDirName); sent.artifact != want {
		t.Fatalf("the send received artifact %q, want %q", sent.artifact, want)
	}
	// The working tree is DETACHED on exactly the merged commit — the detached checkout did its job.
	if head := gitAt(t, wt, "rev-parse", "HEAD"); head != mergedSHA {
		t.Fatalf("the build state is at %q, want the merged commit %q (the detached checkout did not land)", head, mergedSHA)
	}
	if branch := gitAt(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); branch != "HEAD" {
		t.Fatalf("the build state must be DETACHED, got branch %q", branch)
	}

	// A completed path settles the delivery: production stamped, no failure recorded.
	d, ok, _ := s.deliveries.ByID("dlv_1")
	if !ok {
		t.Fatal("the delivery must still be in the ledger")
	}
	if d.ProdDeployedAt == nil {
		t.Fatalf("the whole path completed, so production must be stamped; failure reason: %q", d.ProdFailedReason)
	}
	if d.ProdFailedReason != "" {
		t.Fatalf("no step failed, yet a failure was recorded: %q", d.ProdFailedReason)
	}
}
