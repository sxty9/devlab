package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/runs"
)

// TestDeployTransientVsPermanent pins the classification behind "retry vs block": only a connectivity
// signature (in the deploy OUTPUT or the Go error) is transient; everything else — a missing unit, a
// missing script/target, a bare exit code — is permanent and counts toward a block.
func TestDeployTransientVsPermanent(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		want bool // true = transient (retry, never counts)
	}{
		{"ssh connection refused", "ssh: connect to host prod port 22: Connection refused", errors.New("exit status 255"), true},
		{"no route to host", "ssh: connect to host prod: No route to host", errors.New("exit status 255"), true},
		{"rsync timeout", "rsync: connection timed out (code 30)", errors.New("exit status 30"), true},
		{"could not resolve", "ssh: Could not resolve hostname prod: Name or service not known", errors.New("exit status 255"), true},
		{"go error only i/o timeout", "", errors.New("dial tcp: i/o timeout"), true},
		{"unit not found is permanent", "Failed to restart svc.service: Unit svc.service not found.", errors.New("exit status 1"), false},
		{"missing prod target is permanent", "prod: missing /etc/devlab/prod-target", errors.New("exit status 11"), false},
		{"no deploy script is permanent", "no deploy script for 'prizm'", errors.New("exit status 3"), false},
		{"bare exit code is permanent", "", errors.New("exit status 1"), false},
	}
	for _, c := range cases {
		if got := deployTransient(c.out, c.err); got != c.want {
			t.Errorf("%s: deployTransient=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestDeployBlockReason pins the honest phrasing: a "service not set up on target" failure is named as
// such (service + target + "nicht eingerichtet") rather than left as a raw exit code; a generic failure
// names service + target and carries the output tail without claiming a setup problem.
func TestDeployBlockReason(t *testing.T) {
	setup := deployBlockReason("scrapr", "Failed to restart scrapr.service: Unit scrapr.service not found.", nil)
	for _, want := range []string{"scrapr", "prod", "nicht eingerichtet"} {
		if !strings.Contains(setup, want) {
			t.Errorf("setup-missing reason %q must contain %q", setup, want)
		}
	}
	if strings.Contains(setup, "exit status") {
		t.Errorf("a setup-missing reason must not be a bare exit code: %q", setup)
	}

	generic := deployBlockReason("app", "deploying app\ndisk full: rsync error (code 11)", errors.New("exit status 11"))
	for _, want := range []string{"app", "prod", "rsync error"} {
		if !strings.Contains(generic, want) {
			t.Errorf("generic reason %q must contain %q", generic, want)
		}
	}
	if strings.Contains(generic, "nicht eingerichtet") {
		t.Errorf("a generic failure must NOT be phrased as a setup problem: %q", generic)
	}
}

// TestShipToProd pins the single gate: a deployable, non-excluded repo ships; a repo without a deploy
// target does not; a repo named in DEVLAB_RUNS_NO_DEPLOY does not (case-insensitively), citing the list.
func TestShipToProd(t *testing.T) {
	x := &runExecutor{deployTargetFn: func(name string) bool { return name != "nodeploytarget" }}

	if ship, _ := x.shipToProd("studiq"); !ship {
		t.Errorf("a deployable, non-excluded repo must ship")
	}
	if ship, why := x.shipToProd("nodeploytarget"); ship || why == "" {
		t.Errorf("a repo without a deploy target must not ship, with a reason; got ship=%v why=%q", ship, why)
	}

	t.Setenv("DEVLAB_RUNS_NO_DEPLOY", "phantom, aigentic ;other")
	for _, name := range []string{"aigentic", "AIGENTIC"} {
		ship, why := x.shipToProd(name)
		if ship {
			t.Errorf("%s is not-to-be-delivered and must not ship", name)
		}
		if !strings.Contains(why, "nicht auszuliefern") {
			t.Errorf("exclusion reason for %s should cite the not-to-be-delivered list, got %q", name, why)
		}
	}
	if ship, _ := x.shipToProd("studiq"); !ship {
		t.Errorf("a repo NOT on the exclusion list must still ship")
	}
}

// TestMaxDeployAttempts pins the knob: default, a valid override, and rejection of a non-positive value.
func TestMaxDeployAttempts(t *testing.T) {
	if got := maxDeployAttempts(); got != defaultMaxDeployAttempts {
		t.Errorf("default = %d, want %d", got, defaultMaxDeployAttempts)
	}
	t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", "5")
	if got := maxDeployAttempts(); got != 5 {
		t.Errorf("override 5 = %d, want 5", got)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", bad)
		if got := maxDeployAttempts(); got != defaultMaxDeployAttempts {
			t.Errorf("invalid %q = %d, want default %d", bad, got, defaultMaxDeployAttempts)
		}
	}
}

// TestMaintainPermanentDeployFailureBlocks drives Maintain repeatedly against a permanently-failing
// prod-deploy and asserts it retries exactly maxDeployAttempts times, then blocks and stops attempting.
func TestMaintainPermanentDeployFailureBlocks(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", "3")
	store := tempPRStore(t)
	if err := store.Add(runs.PendingPR{Repo: "o/svc", Number: 5, MergeBy: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var deploys int
	x := &runExecutor{
		s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: true, State: "closed"}, nil
		},
		prodDeployFn: func(context.Context, string, runs.PendingPR) (string, error) {
			deploys++
			return "Failed to restart svc.service: Unit svc.service not found.", errors.New("exit status 1")
		},
		deployTargetFn: func(string) bool { return true },
	}

	for i := 0; i < 6; i++ { // more passes than the limit — a blocked PR must stop being attempted
		x.Maintain(context.Background())
	}

	if deploys != 3 {
		t.Errorf("expected exactly 3 deploy attempts before blocking, got %d", deploys)
	}
	prs, _ := store.List()
	if len(prs) != 1 {
		t.Fatalf("a blocked PR stays tracked, got %+v", prs)
	}
	p := prs[0]
	if !p.Blocked || p.DeployAttempts != 3 {
		t.Errorf("PR should be blocked with 3 attempts, got Blocked=%v Attempts=%d", p.Blocked, p.DeployAttempts)
	}
	if !strings.Contains(p.BlockedReason, "svc") || !strings.Contains(p.BlockedReason, "nicht eingerichtet") {
		t.Errorf("blocked reason should name the service and the setup gap, got %q", p.BlockedReason)
	}
	if p.BlockedAt.IsZero() {
		t.Errorf("BlockedAt must be stamped")
	}
}

// TestMaintainTransientDeployFailureRetries pins the transient path: a network failure is retried forever
// and never counts toward a block.
func TestMaintainTransientDeployFailureRetries(t *testing.T) {
	store := tempPRStore(t)
	if err := store.Add(runs.PendingPR{Repo: "o/svc", Number: 6, MergeBy: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var deploys int
	x := &runExecutor{
		s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: true, State: "closed"}, nil
		},
		prodDeployFn: func(context.Context, string, runs.PendingPR) (string, error) {
			deploys++
			return "ssh: connect to host prod port 22: Connection refused", errors.New("exit status 255")
		},
		deployTargetFn: func(string) bool { return true },
	}

	for i := 0; i < 5; i++ {
		x.Maintain(context.Background())
	}

	if deploys != 5 {
		t.Errorf("a transient failure must be retried every pass, got %d attempts over 5 passes", deploys)
	}
	prs, _ := store.List()
	if len(prs) != 1 || prs[0].Blocked || prs[0].DeployAttempts != 0 {
		t.Errorf("a transient failure must never block or count, got %+v", prs)
	}
}

// TestMaintainBlockedRepoDoesNotHoldUpOthers pins req 3: a repo blocked on a permanent failure does not
// stop a healthy repo from being delivered.
func TestMaintainBlockedRepoDoesNotHoldUpOthers(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", "1") // block on the first permanent failure
	store := tempPRStore(t)
	now := time.Now()
	if err := store.Add(runs.PendingPR{Repo: "o/broken", Number: 1, CreatedAt: now.Add(-2 * time.Hour), MergeBy: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(runs.PendingPR{Repo: "o/healthy", Number: 2, CreatedAt: now.Add(-time.Hour), MergeBy: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var brokenDeploys, healthyDeploys int
	x := &runExecutor{
		s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: true, State: "closed"}, nil
		},
		prodDeployFn: func(_ context.Context, _ string, p runs.PendingPR) (string, error) {
			if p.Repo == "o/broken" {
				brokenDeploys++
				return "Unit broken.service not found.", errors.New("exit status 1")
			}
			healthyDeploys++
			return "ok", nil
		},
		deployTargetFn: func(string) bool { return true },
	}

	x.Maintain(context.Background()) // broken blocks; healthy ships and untracks
	x.Maintain(context.Background()) // broken is skipped (blocked); nothing else to do

	if healthyDeploys != 1 {
		t.Errorf("the healthy repo must be delivered despite the blocked one, got %d deploys", healthyDeploys)
	}
	if brokenDeploys != 1 {
		t.Errorf("the broken repo is attempted once then blocked-and-skipped, got %d deploys", brokenDeploys)
	}
	prs, _ := store.List()
	if len(prs) != 1 || prs[0].Repo != "o/broken" || !prs[0].Blocked {
		t.Errorf("only the blocked broken PR should remain tracked, got %+v", prs)
	}
}

// TestMaintainNotToBeDeliveredNeverAttempts pins req 2: a repo explicitly marked not-to-be-delivered
// triggers no deploy attempt at all, even when a deploy target exists.
func TestMaintainNotToBeDeliveredNeverAttempts(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_NO_DEPLOY", "excluded")
	store := tempPRStore(t)
	if err := store.Add(runs.PendingPR{Repo: "o/excluded", Number: 3, MergeBy: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var deploys int
	x := &runExecutor{
		s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: true, State: "closed"}, nil
		},
		prodDeployFn:   func(context.Context, string, runs.PendingPR) (string, error) { deploys++; return "ok", nil },
		deployTargetFn: func(string) bool { return true }, // a target EXISTS, but the exclusion wins
	}

	x.Maintain(context.Background())

	if deploys != 0 {
		t.Errorf("a not-to-be-delivered repo must trigger no deploy attempt, got %d", deploys)
	}
	if prs, _ := store.List(); len(prs) != 0 {
		t.Errorf("the excluded PR must be untracked with no attempt, still tracked: %+v", prs)
	}
}

// TestRunDeploysBlockedAndResume pins the API: the list returns only blocked deliveries, and resume
// clears exactly one (404 for an untracked or non-blocked PR).
func TestRunDeploysBlockedAndResume(t *testing.T) {
	store := tempPRStore(t)
	now := time.Now()
	if err := store.Add(runs.PendingPR{Repo: "o/broken", Number: 1, URL: "http://pr/1", RunID: "run_a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("o/broken", 1, func(p *runs.PendingPR) {
		p.Blocked, p.BlockedReason, p.BlockedAt, p.DeployAttempts = true, "svc nicht eingerichtet", now, 3
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(runs.PendingPR{Repo: "o/healthy", Number: 2}); err != nil { // not blocked
		t.Fatal(err)
	}
	s := &Server{runPRs: store}

	rec := httptest.NewRecorder()
	s.runDeploysBlocked(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/deploys", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "o/broken") || strings.Contains(body, "o/healthy") {
		t.Errorf("list must contain only the blocked delivery, got %s", body)
	}

	resume := func(bodyJSON string) int {
		rec := httptest.NewRecorder()
		s.runDeployResume(rec, httptest.NewRequest(http.MethodPost, "/api/mercury/runs/deploys/resume", strings.NewReader(bodyJSON)))
		return rec.Code
	}
	if code := resume(`{"repo":"o/broken","number":999}`); code != http.StatusNotFound {
		t.Errorf("resume of an untracked PR = %d, want 404", code)
	}
	if code := resume(`{"repo":"o/healthy","number":2}`); code != http.StatusNotFound {
		t.Errorf("resume of a non-blocked PR = %d, want 404", code)
	}
	if code := resume(`{"repo":"o/broken","number":1}`); code != http.StatusOK {
		t.Errorf("resume of the blocked PR = %d, want 200", code)
	}

	p, _, _ := storeGet(t, store, "o/broken", 1)
	if p.Blocked || p.DeployAttempts != 0 || p.BlockedReason != "" || !p.BlockedAt.IsZero() {
		t.Errorf("resume must fully clear the block, got %+v", p)
	}
	rec = httptest.NewRecorder()
	s.runDeploysBlocked(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/deploys", nil))
	if !strings.Contains(rec.Body.String(), "\"blocked\":[]") {
		t.Errorf("after resume the blocked list must be empty, got %s", rec.Body.String())
	}
}

// storeGet is a tiny test helper to read one tracked PR back out of the store.
func storeGet(t *testing.T, s *runs.PRStore, repo string, number int) (runs.PendingPR, bool, error) {
	t.Helper()
	prs, err := s.List()
	if err != nil {
		return runs.PendingPR{}, false, err
	}
	for _, p := range prs {
		if p.Repo == repo && p.Number == number {
			return p, true, nil
		}
	}
	return runs.PendingPR{}, false, nil
}
