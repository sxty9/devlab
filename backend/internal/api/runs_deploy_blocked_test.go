package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/github"
	"devlab/backend/internal/runs"
)

// ─── prod-deploy failure classification (TEIL A) ────────────────────────────

// TestDeployTransientVsPermanent pins the transient/permanent split that decides retry-forever vs
// eventually-block. Connectivity failures (in the wrapper OUTPUT or the Go error) are transient; a
// missing unit, missing config, bad argument or unknown env is permanent.
func TestDeployTransientVsPermanent(t *testing.T) {
	cases := []struct {
		name          string
		out           string
		err           error
		wantTransient bool
	}{
		{"ssh connection refused (output)", "ssh: connect to host 10.0.0.1 port 22: Connection refused", errors.New("exit status 255"), true},
		{"no route to host (output)", "ssh: connect to host vps port 22: No route to host", errors.New("exit status 255"), true},
		{"rsync timed out (output)", "rsync error: timeout in data send/receive (code 30)", errors.New("exit status 30"), true},
		{"could not resolve host (output)", "ssh: Could not resolve hostname vps: Name or service not known", errors.New("exit status 255"), true},
		{"infra error (go error only)", "", errors.New("dial tcp: i/o timeout"), true},
		{"missing unit on target", "Failed to restart devlab.service: Unit devlab.service not found.", errors.New("exit status 1"), false},
		{"missing prod config", "[deploy devlab] prod: missing /etc/devlab/prod-target", errors.New("exit status 11"), false},
		{"no deploy script (wrapper exit 3)", "devlab-deploy: no deploy script for 'prizm' (/etc/devlab/deploy.d/prizm) — skipped", errors.New("exit status 3"), false},
		{"bare exit status, no clue", "", errors.New("exit status 1"), false},
	}
	for _, c := range cases {
		if got := deployTransient(c.out, c.err); got != c.wantTransient {
			t.Errorf("%s: deployTransient=%v, want %v", c.name, got, c.wantTransient)
		}
	}
}

// TestDeployBlockReason pins requirement 2: a block reason NAMES the service and the target and, for a
// "not set up on the target" failure, says exactly that — never leaving a bare exit code.
func TestDeployBlockReason(t *testing.T) {
	unit := deployBlockReason("aigentic", "Failed to restart aigentic.service: Unit aigentic.service not found.", errors.New("exit status 1"))
	if !strings.Contains(unit, "aigentic") || !strings.Contains(unit, "prod") || !strings.Contains(unit, "nicht eingerichtet") {
		t.Errorf("setup-missing reason must name service+target and say 'nicht eingerichtet': %q", unit)
	}
	if strings.TrimSpace(unit) == "exit status 1" {
		t.Errorf("reason must not be a bare exit code: %q", unit)
	}

	generic := deployBlockReason("studiq", "[deploy studiq] copy failed: disk full", errors.New("exit status 1"))
	if !strings.Contains(generic, "studiq") || !strings.Contains(generic, "prod") {
		t.Errorf("generic reason must still name service+target: %q", generic)
	}
	if strings.Contains(generic, "nicht eingerichtet") {
		t.Errorf("a non-setup failure must NOT claim 'nicht eingerichtet': %q", generic)
	}
	if !strings.Contains(generic, "disk full") {
		t.Errorf("generic reason should carry the output tail as detail: %q", generic)
	}
}

// TestShipToProd pins the single no-ship gate: an explicitly not-to-be-delivered repo and a repo with
// no deploy script both return ship=false with a reason (no attempt); a deployable, non-excluded repo
// ships. The concrete case — a service marked not-to-be-delivered — never reaches a deploy.
func TestShipToProd(t *testing.T) {
	x := &runExecutor{deployTargetFn: func(name string) bool { return name != "nodeploytarget" }}

	if ship, _ := x.shipToProd("aigentic"); !ship {
		t.Errorf("a deployable, non-excluded repo must ship")
	}
	if ship, why := x.shipToProd("nodeploytarget"); ship || why == "" {
		t.Errorf("a repo with no deploy script must not ship, with a reason (got ship=%v why=%q)", ship, why)
	}

	t.Setenv("DEVLAB_RUNS_NO_DEPLOY", "phantom, aigentic ;other")
	if ship, why := x.shipToProd("aigentic"); ship || !strings.Contains(why, "nicht auszuliefern") {
		t.Errorf("an excluded repo must not ship, citing the exclusion (got ship=%v why=%q)", ship, why)
	}
	if ship, why := x.shipToProd("AIGENTIC"); ship { // case-insensitive
		t.Errorf("exclusion must be case-insensitive, but %q shipped (why=%q)", "AIGENTIC", why)
	}
	if ship, _ := x.shipToProd("studiq"); !ship {
		t.Errorf("a repo NOT on the exclusion list must still ship")
	}
}

// TestMaxDeployAttempts pins the threshold knob: default 3, overridable, non-positive ignored.
func TestMaxDeployAttempts(t *testing.T) {
	if got := maxDeployAttempts(); got != defaultMaxDeployAttempts {
		t.Errorf("default maxDeployAttempts=%d, want %d", got, defaultMaxDeployAttempts)
	}
	t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", "5")
	if got := maxDeployAttempts(); got != 5 {
		t.Errorf("override maxDeployAttempts=%d, want 5", got)
	}
	t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", "0")
	if got := maxDeployAttempts(); got != defaultMaxDeployAttempts {
		t.Errorf("non-positive override ignored: got %d, want %d", got, defaultMaxDeployAttempts)
	}
}

// ─── Blocked-deploy API (visibility + resume) ───────────────────────────────

// TestRunDeploysBlockedAndResume pins requirement 1 (visibility) and the explicit resume: GET /deploys
// returns exactly the blocked deliveries with repo/reason/attempts, resume clears the block (resetting
// the attempt budget), and a resume of an untracked/non-blocked PR is a 404.
func TestRunDeploysBlockedAndResume(t *testing.T) {
	store := tempPRStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.Add(runs.PendingPR{
		Repo: "o/svc", Number: 9, URL: "http://pr/9", RunID: "run_x",
		Blocked: true, BlockedReason: "Dienst »svc« ist im Ziel »prod« nicht eingerichtet", BlockedAt: now, DeployAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(runs.PendingPR{Repo: "o/other", Number: 1, MergeBy: now.Add(time.Hour)}); err != nil {
		t.Fatal(err) // a healthy pending PR must NOT show up as blocked
	}
	s := &Server{runPRs: store}

	listBlocked := func() []blockedDeployView {
		rec := httptest.NewRecorder()
		s.runDeploysBlocked(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/runs/deploys", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET deploys status %d", rec.Code)
		}
		var body struct {
			Blocked []blockedDeployView `json:"blocked"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Blocked
	}
	resume := func(payload string) int {
		rec := httptest.NewRecorder()
		s.runDeployResume(rec, httptest.NewRequest(http.MethodPost, "/api/mercury/runs/deploys/resume", strings.NewReader(payload)))
		return rec.Code
	}

	blocked := listBlocked()
	if len(blocked) != 1 {
		t.Fatalf("want exactly 1 blocked delivery, got %d (%+v)", len(blocked), blocked)
	}
	if b := blocked[0]; b.Repo != "o/svc" || b.Number != 9 || b.Attempts != 3 || b.Reason == "" || b.URL != "http://pr/9" {
		t.Errorf("blocked view carries wrong data: %+v", b)
	}

	if code := resume(`{"repo":"o/svc","number":999}`); code != http.StatusNotFound {
		t.Errorf("resume of an untracked PR: want 404, got %d", code)
	}
	if code := resume(`{"repo":"o/other","number":1}`); code != http.StatusNotFound {
		t.Errorf("resume of a non-blocked PR: want 404, got %d", code)
	}
	if code := resume(`{"repo":"o/svc","number":9}`); code != http.StatusOK {
		t.Fatalf("resume of the blocked PR: want 200, got %d", code)
	}

	// The block is fully cleared and the attempt budget reset.
	prs, _ := store.List()
	for _, p := range prs {
		if p.Repo == "o/svc" && p.Number == 9 {
			if p.Blocked || p.DeployAttempts != 0 || p.BlockedReason != "" || !p.BlockedAt.IsZero() {
				t.Errorf("resume must fully clear the block, got %+v", p)
			}
		}
	}
	if after := listBlocked(); len(after) != 0 {
		t.Errorf("after resume nothing should be blocked, got %+v", after)
	}
}

// ─── Maintain orchestration: blocking behavior ──────────────────────────────

// TestMaintainPermanentDeployFailureBlocks pins requirement 1: a merged PR whose prod-deploy fails for
// a PERMANENT reason (service not set up on the target) is attempted only a few times and then BLOCKED —
// recorded with a service-naming reason + timestamp — instead of being retried on every tick forever.
func TestMaintainPermanentDeployFailureBlocks(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", "3")
	store := tempPRStore(t)
	if err := store.Add(runs.PendingPR{Repo: "o/svc", Number: 9, MergeBy: time.Now().Add(-time.Hour)}); err != nil {
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

	for i := 0; i < 6; i++ { // more ticks than the threshold
		x.Maintain(context.Background())
	}

	if deploys != 3 {
		t.Errorf("a permanent failure must be attempted exactly maxDeployAttempts (3) times, got %d", deploys)
	}
	got, _ := store.List()
	if len(got) != 1 || !got[0].Blocked {
		t.Fatalf("the PR must stay tracked AND be blocked, got %+v", got)
	}
	if got[0].DeployAttempts != 3 {
		t.Errorf("expected 3 recorded attempts, got %d", got[0].DeployAttempts)
	}
	if got[0].BlockedReason == "" || got[0].BlockedAt.IsZero() {
		t.Errorf("a blocked PR must carry a reason and a timestamp, got reason=%q at=%v", got[0].BlockedReason, got[0].BlockedAt)
	}
	if !strings.Contains(got[0].BlockedReason, "svc") || !strings.Contains(got[0].BlockedReason, "nicht eingerichtet") {
		t.Errorf("the reason must name the service and the not-set-up cause, got %q", got[0].BlockedReason)
	}
}

// TestMaintainTransientDeployFailureRetries pins requirement 1's other half: a TRANSIENT deploy failure
// (the VPS briefly unreachable) keeps being retried and NEVER blocks — the two cases are distinguished.
func TestMaintainTransientDeployFailureRetries(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", "3")
	store := tempPRStore(t)
	if err := store.Add(runs.PendingPR{Repo: "o/svc", Number: 10, MergeBy: time.Now().Add(-time.Hour)}); err != nil {
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
			return "ssh: connect to host vps port 22: Connection refused", errors.New("exit status 255")
		},
		deployTargetFn: func(string) bool { return true },
	}

	for i := 0; i < 5; i++ {
		x.Maintain(context.Background())
	}

	if deploys != 5 {
		t.Errorf("a transient failure must keep retrying every tick, got %d attempts across 5 ticks", deploys)
	}
	got, _ := store.List()
	if len(got) != 1 || got[0].Blocked {
		t.Fatalf("a transient failure must NOT block; PR stays tracked+unblocked, got %+v", got)
	}
	if got[0].DeployAttempts != 0 {
		t.Errorf("transient failures must not count toward the block, got %d", got[0].DeployAttempts)
	}
}

// TestMaintainBlockedRepoDoesNotHoldUpOthers pins requirement 3: with a permanently-broken repo blocked,
// a healthy repo still deploys and untracks in the same sweep, and the broken repo stops being retried.
func TestMaintainBlockedRepoDoesNotHoldUpOthers(t *testing.T) {
	t.Setenv("DEVLAB_RUNS_MAX_DEPLOY_ATTEMPTS", "1") // block on the first permanent failure
	store := tempPRStore(t)
	if err := store.Add(runs.PendingPR{Repo: "o/broken", Number: 1, MergeBy: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(runs.PendingPR{Repo: "o/healthy", Number: 2, MergeBy: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	deploys := map[string]int{}
	x := &runExecutor{
		s: &Server{runPRs: store}, mode: "full", tokenUser: "owner",
		tokenFn: func(string) (string, error) { return "tok", nil },
		getPRFn: func(context.Context, string, string, int) (github.PullRequest, error) {
			return github.PullRequest{Merged: true, State: "closed"}, nil
		},
		prodDeployFn: func(_ context.Context, _ string, p runs.PendingPR) (string, error) {
			deploys[p.Repo]++
			if p.Repo == "o/broken" {
				return "Unit broken.service not found.", errors.New("exit status 1")
			}
			return "ok", nil
		},
		deployTargetFn: func(string) bool { return true },
	}

	x.Maintain(context.Background()) // pass 1: broken fails+blocks; healthy deploys+untracks
	x.Maintain(context.Background()) // pass 2: broken is skipped (blocked); healthy already gone

	if deploys["o/healthy"] != 1 {
		t.Errorf("the healthy repo must deploy despite the blocked one, got %d", deploys["o/healthy"])
	}
	if deploys["o/broken"] != 1 {
		t.Errorf("the broken repo must attempt once then stop (blocked), got %d", deploys["o/broken"])
	}
	got, _ := store.List()
	if len(got) != 1 || got[0].Repo != "o/broken" || !got[0].Blocked {
		t.Fatalf("only the broken PR must remain, blocked; healthy untracked. got %+v", got)
	}
}

// TestMaintainNotToBeDeliveredNeverAttempts pins the concrete case: a repo marked not-to-be-delivered
// (DEVLAB_RUNS_NO_DEPLOY) has its merged PR untracked WITHOUT any deploy attempt — even with a deploy
// script present — so no failed attempt is ever produced.
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
		prodDeployFn:   func(context.Context, string, runs.PendingPR) (string, error) { deploys++; return "", nil },
		deployTargetFn: func(string) bool { return true }, // a script exists, but the exclusion wins
	}

	x.Maintain(context.Background())

	if deploys != 0 {
		t.Errorf("a not-to-be-delivered repo must trigger NO deploy attempt, got %d", deploys)
	}
	if r, _ := store.List(); len(r) != 0 {
		t.Errorf("a not-to-be-delivered merged PR must be untracked without an attempt, got %+v", r)
	}
}
