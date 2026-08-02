package api

// The production send bounced on the service NAME and never got past the first step: the delivery
// ledger records a service under its FULL "owner/repo" name, MaintainProd hands that value to
// DeployProd, and the runner's workbench — addressed by the BARE repo id, whose grammar rejects a
// slash — refused it with "invalid user/repo" before any branch was resolved, fetched, built or
// shipped (measured 02.08.2026, for every service, on every maintenance pass).
//
// These tests run the path the per-building-block tests never did: from a MERGED ledger entry that
// carries the full name, through the REAL workspace preparation the production chain uses. The
// existing prod-pass tests substitute a fake deployer, so the workbench never sees the name and the
// confusion stayed invisible.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/deliver"
	"devlab/backend/internal/github"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workspace"
)

// prodNameServer builds a Server with a REAL workspace manager over a temp root plus the ledger,
// result and notice pools the production pass writes. DEVLAB_WORKSPACES points the manager at the
// temp root so nothing touches a real checkout.
func prodNameServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	wsRoot := filepath.Join(dir, "ws")
	t.Setenv("DEVLAB_WORKSPACES", wsRoot)
	t.Setenv("DEVLAB_MERCURY_RUNS_DELIVERIES", filepath.Join(dir, "deliveries.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", filepath.Join(dir, "results"))
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", filepath.Join(dir, "executions"))
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(dir, "notices.json"))
	s := &Server{
		workspaces: workspace.NewManager(nil),
		deliveries: runs.NewDeliveryStore(nil),
		results:    runs.NewResultStore(nil),
		runNotices: runs.NewNoticeStore(nil),
	}
	return s, wsRoot
}

// seedWorkspace makes <root>/<user>/<repo> look like an already-cloned checkout (a .git entry), so
// the workspace preparation returns it without a network clone or the root provisioning helper — the
// parts that cannot run in a test. It does NOT change what is under test: the name handling that
// decides WHICH directory is addressed runs exactly as in production.
func seedWorkspace(t *testing.T, root, user, repo string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, user, repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestRunnerBench_AcceptsLedgerFullName pins the fix at the exact seam that bounced: the runner's
// workbench preparation, handed the ledger's full "owner/repo" name (the form the production and
// rollback passes read), returns a prepared workbench keyed by the BARE id instead of refusing the
// name. Before the fix this returned "workspace: invalid user/repo".
func TestRunnerBench_AcceptsLedgerFullName(t *testing.T) {
	s, wsRoot := prodNameServer(t)
	const user, full, short = "runner", "acme/service", "service"
	seedWorkspace(t, wsRoot, user, short)

	bench, wt, unlock, err := s.runnerBench(context.Background(), user, "", full, full)
	if err != nil {
		t.Fatalf("the ledger's full name %q must reach a prepared workbench, not bounce: %v", full, err)
	}
	defer unlock()
	if bench == nil {
		t.Fatal("a prepared workbench must be returned")
	}
	// The workspace is addressed by the bare id: <root>/<user>/<short>, never <root>/<user>/owner/repo.
	if got := filepath.Join(wsRoot, user, short); wt != got {
		t.Fatalf("workbench working tree = %q, want the bare-id path %q", wt, got)
	}
}

// TestMaintainProd_LedgerFullNameReachesSend is the nachweis (WHAT-4): a MERGED entry from the
// delivery ledger — carrying the full name exactly as the ledger stores it — travels the whole path
// (MaintainProd → the REAL DeployProd → the REAL workbench) and reaches the deploy chain proper
// instead of being refused at the workbench. It stops later, at the fetch/checkout of the merged
// state (there is no real remote in a test), which PROVES it got past the first step — the name no
// longer aborts the send.
func TestMaintainProd_LedgerFullNameReachesSend(t *testing.T) {
	s, wsRoot := prodNameServer(t)
	const user, full, short = "runner", "acme/service", "service"
	seedWorkspace(t, wsRoot, user, short)

	// The merged state's default branch is the only network read before the send; point the whole
	// chain at a fixture GitHub so the test stays hermetic and fast.
	restore := github.SetAPIBase(fixtureGitHub(t))
	defer restore()

	merged := time.Now().UTC().Add(-time.Hour)
	if err := s.deliveries.Put(runs.Delivery{
		ID: "dlv_1", Repo: full, Branch: "fix/x-1", ExecutionID: "exec_1",
		CreatedAt: merged.Add(-time.Hour), MergedAt: &merged,
	}); err != nil {
		t.Fatal(err)
	}
	d0, _, _ := s.deliveries.ByID("dlv_1")
	if !d0.NeedsProd() {
		t.Fatalf("a merged, not-yet-produced delivery must owe production: %+v", d0)
	}

	deps := &ChainDeps{s: s, user: user, benches: map[string]*repoBench{}, full: map[string]string{}}
	err := deliver.MaintainProd(context.Background(), chainDeploy{d: deps}, s.deliveries, s.results, s.runNotices, nil)
	if err == nil {
		t.Fatal("the send cannot complete against a fixture (no real remote/target) — it must surface a failure")
	}

	d, ok, _ := s.deliveries.ByID("dlv_1")
	if !ok {
		t.Fatal("the delivery must still be in the ledger")
	}
	reason := d.ProdFailedReason
	if strings.Contains(reason, "invalid user/repo") {
		t.Fatalf("the full name still bounced at the workbench — the name confusion is not fixed: %q", reason)
	}
	if strings.Contains(reason, "prepare the runner workspace") {
		t.Fatalf("the workbench preparation still failed on the name — it must be passed: %q", reason)
	}
	if !strings.HasPrefix(reason, "deploy:") {
		t.Fatalf("the send must reach the deploy chain proper (a %q step), got %q", "deploy:", reason)
	}
	if d.ProdDeployedAt != nil {
		t.Fatal("no send actually completed, so production must not be stamped as done")
	}
}

// fixtureGitHub serves the one read the production send makes before shipping — the merged repo's
// default branch — and returns its base URL. Any repo answers with default branch "main".
func fixtureGitHub(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_branch":"main"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
