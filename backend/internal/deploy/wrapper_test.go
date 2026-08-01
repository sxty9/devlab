package deploy

// Dry-run tests of the root wrapper templates in deploy/ (B5 ownership). The --check branch runs
// the FULL validation cascade without root and without effects, so the security-critical logic
// (name grammar, realpath confinement, env strictness, handover-only systemd-run) is provable
// here — no sudo, no host mutation (REQ-028, B-43/B-44, K-2).

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// repoRoot walks up from this source file to the repository root (the deploy/ templates live there).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file)))) // backend/internal/deploy → repo root
}

type wrapperResult struct {
	exit int
	out  string
}

// runWrapper invokes a deploy/ script directly (not via sudo) with a controlled environment.
func runWrapper(t *testing.T, script string, env map[string]string, args ...string) wrapperResult {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), script)}, args...)...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	res := wrapperResult{out: string(out)}
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s did not run: %v\n%s", script, err, out)
		}
		res.exit = ee.ExitCode()
	}
	return res
}

// testOwner is the organisation the fixtures below pretend to belong to. The wrapper takes the
// managed owner from root-owned configuration; DEVLAB_GH_OWNER is the direct-invocation test seam
// (sudo's env_reset strips it in production).
const testOwner = "example-org"

// installEnv prepares a fake workspace root with a valid artifact and returns the env + paths. The
// checkout carries an origin remote under testOwner, because the wrapper only installs repositories
// of the configured organisation.
func installEnv(t *testing.T) (env map[string]string, workRoot, artifact string) {
	t.Helper()
	state := t.TempDir()
	workRoot = filepath.Join(state, "workspaces")
	artifact = filepath.Join(workRoot, "runner", "svc-a", ".mercury-artifact")
	if err := os.MkdirAll(artifact, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifact, "svc-ad"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrigin(t, filepath.Dir(artifact), testOwner, "svc-a")
	return map[string]string{"DEVLAB_STATE_DIR": state, "DEVLAB_GH_OWNER": testOwner}, workRoot, artifact
}

// writeOrigin gives a checkout the git config of a clone of <owner>/<repo> — the fact the wrapper
// reads (as text, never by running git) to decide whose repository it is being handed.
func writeOrigin(t *testing.T, checkout, owner, repo string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = https://github.com/" +
		owner + "/" + repo + ".git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	if err := os.WriteFile(filepath.Join(checkout, ".git", "config"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ─── devlab-install --check: the validation cascade without root ─────────────

// A traversal artifact path (…/../../tmp/evil) canonicalizes OUTSIDE the workspace root and
// dies before any effect — the test proves the realpath confinement, not a string prefix.
func TestInstallCheckRejectsPathTraversal(t *testing.T) {
	env, workRoot, _ := installEnv(t)
	// A real directory OUTSIDE the workspace root, reached through '..' segments FROM it: the
	// string starts under the root, the canonical path does not.
	evil := filepath.Join(filepath.Dir(filepath.Dir(workRoot)), "evil")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	traversal := workRoot + "/../../evil"

	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", traversal, "dev", "--check")
	if res.exit != 2 {
		t.Fatalf("traversal must die with exit 2, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "escapes the workspace root") {
		t.Errorf("the refusal must name the confinement: %s", res.out)
	}
	if strings.Contains(res.out, "PLAN:") {
		t.Errorf("validation failures must precede every planned effect: %s", res.out)
	}
}

func TestInstallCheckRejectsNonResolvingArtifact(t *testing.T) {
	env, workRoot, _ := installEnv(t)
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", filepath.Join(workRoot, "runner", "absent"), "dev", "--check")
	if res.exit != 2 {
		t.Fatalf("a non-resolving artifact dir must die with exit 2, got %d\n%s", res.exit, res.out)
	}
}

func TestInstallCheckValidatesRepoGrammar(t *testing.T) {
	env, _, artifact := installEnv(t)
	for _, repo := range []string{"../evil", "UPPER", "a", "x_y", "-lead", "with space"} {
		res := runWrapper(t, "deploy/devlab-install", env, repo, artifact, "dev", "--check")
		if res.exit != 2 {
			t.Errorf("repo %q must die with exit 2, got %d\n%s", repo, res.exit, res.out)
		}
	}
}

func TestInstallCheckValidatesEnv(t *testing.T) {
	env, _, artifact := installEnv(t)

	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "staging", "--check")
	if res.exit != 2 {
		t.Fatalf("env 'staging' must die with exit 2, got %d\n%s", res.exit, res.out)
	}

	// prod exists in the grammar but is NOT armed in this phase — it dies as config-state.
	res = runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "prod", "--check")
	if res.exit != 5 {
		t.Fatalf("env 'prod' must die with exit 5 (not armed), got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "not armed") {
		t.Errorf("prod refusal must say why: %s", res.out)
	}
}

// A valid foreign dev install plans install-only steps — and NEVER a systemd-run (the handover
// is the self-repo's path; K-2's code search must stay empty here).
func TestInstallCheckForeignPlansWithoutSystemdRun(t *testing.T) {
	env, _, artifact := installEnv(t)
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 0 {
		t.Fatalf("valid check must exit 0, got %d\n%s", res.exit, res.out)
	}
	if strings.Contains(res.out, "systemd-run") {
		t.Errorf("a foreign install must never plan systemd-run: %s", res.out)
	}
	for _, want := range []string{"PLAN:", "install", "systemctl restart svc-a"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("plan should mention %q: %s", want, res.out)
		}
	}
	// First-time setup (no unit on this test host) plans the inline templates with the port.
	if !strings.Contains(res.out, "8772") {
		t.Errorf("first-time plan should carry the validated port: %s", res.out)
	}
}

// First-time setup without a validated port is refused — never a guessed template default.
func TestInstallCheckFirstTimeNeedsPort(t *testing.T) {
	env, _, artifact := installEnv(t)
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check")
	if res.exit != 5 {
		t.Fatalf("first-time setup without --port must die with exit 5, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "--port") {
		t.Errorf("refusal should name the missing port: %s", res.out)
	}
}

func TestInstallCheckHandoverIsSelfOnly(t *testing.T) {
	env, _, artifact := installEnv(t)
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--handover")
	if res.exit != 2 {
		t.Fatalf("--handover on a foreign repo must die with exit 2, got %d\n%s", res.exit, res.out)
	}
}

// The self-repo handover plans install + systemd-run(devlab-restart-when-free) in ONE call (B-2)
// — and the check run itself changes nothing.
func TestInstallCheckSelfHandoverPlansTransientUnit(t *testing.T) {
	env, workRoot, _ := installEnv(t)
	self := filepath.Join(workRoot, "runner", "devlab", ".mercury-artifact")
	if err := os.MkdirAll(filepath.Join(self, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(self, "devlabd"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := runWrapper(t, "deploy/devlab-install", env, "devlab", self, "dev", "--check", "--handover")
	if res.exit != 0 {
		t.Fatalf("self handover check must exit 0, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "systemd-run") || !strings.Contains(res.out, "devlab-restart-when-free") {
		t.Errorf("self handover must plan the transient restart unit: %s", res.out)
	}
	if !strings.Contains(res.out, "--collect") {
		t.Errorf("the transient unit must be collected: %s", res.out)
	}

	// Without --handover: install only, no restart of any kind.
	res = runWrapper(t, "deploy/devlab-install", env, "devlab", self, "dev", "--check")
	if res.exit != 0 || strings.Contains(res.out, "systemd-run") || strings.Contains(res.out, "systemctl restart") {
		t.Errorf("self install without --handover must plan no restart (exit %d): %s", res.exit, res.out)
	}
}

// A foreign artifact WITHOUT a ui/ reports the ui half as absent — never silently omitted.
func TestInstallCheckNoUIReportsNone(t *testing.T) {
	env, _, _ := installEnv(t)
	artifact := filepath.Join(env["DEVLAB_STATE_DIR"], "workspaces", "runner", "svc-a", ".mercury-artifact")
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 0 {
		t.Fatalf("valid check must exit 0, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "MERCURY-UI: none") {
		t.Errorf("a service without a ui must say so on the half-report line: %s", res.out)
	}
}

// A ui-bearing service with NO holistic checkout configured is a NAMED deficiency (exit 5) — the
// program installs, but the delivery says the face could NOT be built in, never a silent skip.
func TestInstallCheckUIUnconfiguredIsNamed(t *testing.T) {
	env, workRoot, _ := installEnv(t)
	artifact := filepath.Join(workRoot, "runner", "svc-a", ".mercury-artifact")
	if err := os.MkdirAll(filepath.Join(artifact, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No DEVLAB_HOLISTIC_REPO, and the config file default does not exist under a temp state dir.
	env["DEVLAB_HOLISTIC_REPO_FILE"] = filepath.Join(env["DEVLAB_STATE_DIR"], "no-such-holistic-repo")
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 5 {
		t.Fatalf("an unconfigured dashboard for a ui service must die with exit 5, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "MERCURY-UI: unconfigured") || !strings.Contains(res.out, "could NOT be built in") {
		t.Errorf("the refusal must name the ui half and why: %s", res.out)
	}
}

// A ui-bearing service WITH a configured dashboard plans the wire-in, the OWNER-run rebuild (root
// never builds) AND the delivery to the serve root the browser reads — and it makes explicit that
// `built` is gated on ARRIVAL there, not on the build. This is the checkpoint pulled from "was it
// built?" onto "did it arrive?" (the confusion that let a rebuilt-but-undelivered dashboard read green).
func TestInstallCheckUIConfiguredPlansWireInBuildAndServe(t *testing.T) {
	env, workRoot, _ := installEnv(t)
	artifact := filepath.Join(workRoot, "runner", "svc-a", ".mercury-artifact")
	if err := os.MkdirAll(filepath.Join(artifact, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	holistic := filepath.Join(t.TempDir(), "holistic")
	if err := os.MkdirAll(filepath.Join(holistic, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(holistic, "frontend", "external"), 0o755); err != nil {
		t.Fatal(err)
	}
	www := filepath.Join(t.TempDir(), "www") // the serve root the browser reads — named in the plan
	env["DEVLAB_HOLISTIC_REPO"] = holistic
	env["DEVLAB_HOLISTIC_WWW"] = www
	res := runWrapper(t, "deploy/devlab-install", env, "svc-a", artifact, "dev", "--check", "--port", "8772")
	if res.exit != 0 {
		t.Fatalf("a configured ui check must exit 0, got %d\n%s", res.exit, res.out)
	}
	for _, want := range []string{
		"external/svc-a",           // (1) the wire-in
		"AS THE CHECKOUT OWNER",    // (2) the rebuild runs unprivileged — root never builds
		"DELIVER the built bundle", // (3) the delivery step that was missing
		www,                        // …to the serve root the browser fetches from
		"ARRIVED",                  // built is gated on arrival, not on the build
		"MERCURY-UI: planned", "foreign-blocked",
	} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the ui plan should mention %q: %s", want, res.out)
		}
	}
	// The old confusion, guarded against: the plan must NOT stop at the build as the last word on the
	// ui half — the serve root has to be the measure.
	if !strings.Contains(res.out, "serve root") {
		t.Errorf("the plan must make the serve root the measure of the ui half: %s", res.out)
	}
}

// The class of break that shipped green because DevLab itself carries no ui/: a service ui carries a
// tsconfig that extends the dashboard base by a path valid only where the repo sits BESIDE holistic
// (../../holistic/frontend/tsconfig.base.json). Copied into <frontend>/external/<repo> that path
// escapes into nothing and vite dies. This check stands a ui-bearing service against the REAL
// dashboard layout and proves the relocation neutralises the editor tsconfig instead of copying it —
// exactly what a green delivery of the copy-not-symlink change failed to exercise.
func TestInstallCheckUINeutralisesEditorTsconfig(t *testing.T) {
	// presentr was only where it was NOTICED — the identical line lives in every ui-bearing service, so
	// the proof spans more than one. A shared dashboard checkout with the base where a service tsconfig
	// canNOT reach it from external/<repo> is the same asymmetry that broke the real delivery.
	holistic := filepath.Join(t.TempDir(), "holistic")
	if err := os.MkdirAll(filepath.Join(holistic, "frontend", "external"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(holistic, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(holistic, "frontend", "tsconfig.base.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The exact hazard, verbatim from a real service ui: an extends that climbs out of the tree.
	badTs := `{"extends":"../../holistic/frontend/tsconfig.base.json","include":["src"]}`

	for _, repo := range []string{"presentr", "contax"} {
		t.Run(repo, func(t *testing.T) {
			state := t.TempDir()
			workRoot := filepath.Join(state, "workspaces")
			artifact := filepath.Join(workRoot, "runner", repo, ".mercury-artifact")
			uiSrc := filepath.Join(artifact, "ui", "src")
			if err := os.MkdirAll(uiSrc, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(artifact, repo+"d"), []byte("bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(artifact, "ui", "tsconfig.json"), []byte(badTs), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(uiSrc, "index.tsx"), []byte("export const x = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			writeOrigin(t, filepath.Dir(artifact), testOwner, repo)
			www := filepath.Join(state, "www") // a distinct serve root per service — the browser's source
			env := map[string]string{
				"DEVLAB_STATE_DIR": state, "DEVLAB_GH_OWNER": testOwner, "DEVLAB_HOLISTIC_REPO": holistic,
				"DEVLAB_HOLISTIC_WWW": www,
			}
			res := runWrapper(t, "deploy/devlab-install", env, repo, artifact, "dev", "--check", "--port", "8772")
			if res.exit != 0 {
				t.Fatalf("the neutralised ui of %q must plan cleanly, got exit %d\n%s", repo, res.exit, res.out)
			}
			if !strings.Contains(res.out, "neutralised") || !strings.Contains(res.out, "MERCURY-UI: planned") {
				t.Errorf("%q: the plan must report the editor tsconfig neutralised and plan the build: %s", repo, res.out)
			}
			if strings.Contains(res.out, "MERCURY-UI: would-fail") {
				t.Errorf("%q: the editor tsconfig was NOT neutralised or the serve step did not deliver: %s", repo, res.out)
			}
			// Point 4: a ui change reaches the browser for MORE THAN ONE service. Each of presentr and
			// contax must plan the delivery of its built bundle to ITS OWN serve root — proven here, and
			// (b) the serve step itself is simulated on a throwaway pair inside --check, so a green plan
			// means the copy that ships actually moves the bundle, not merely that it was built.
			if !strings.Contains(res.out, "DELIVER the built bundle") || !strings.Contains(res.out, www) {
				t.Errorf("%q: the plan must deliver the built bundle to the serve root %s: %s", repo, www, res.out)
			}
			if !strings.Contains(res.out, "ARRIVED at "+www) {
				t.Errorf("%q: built must be gated on arrival at the serve root, not on the build: %s", repo, res.out)
			}
		})
	}
}

// The self artifact must be complete (binary + web) — honest config-state refusal otherwise.
func TestInstallCheckSelfArtifactComplete(t *testing.T) {
	env, workRoot, _ := installEnv(t)
	self := filepath.Join(workRoot, "runner", "devlab", ".mercury-artifact")
	if err := os.MkdirAll(self, 0o755); err != nil { // no binary, no web
		t.Fatal(err)
	}
	res := runWrapper(t, "deploy/devlab-install", env, "devlab", self, "dev", "--check")
	if res.exit != 5 {
		t.Fatalf("an incomplete self artifact must die with exit 5, got %d\n%s", res.exit, res.out)
	}
}

// ─── devlab-restart-when-free --check: the free probe without root ───────────

// startReadySocket serves the given status code on a unix socket in the statepath layout.
func startReadySocket(t *testing.T, stateDir string, status int) {
	t.Helper()
	sock := filepath.Join(stateDir, "restart-ready.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot bind a unix socket here: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close(); l.Close() })
	time.Sleep(20 * time.Millisecond) // let the listener come up before the probe
}

func TestRestartWhenFreeProbe(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skipf("curl not available: %v", err)
	}

	// Dead daemon (no socket) ⇒ free: a dead daemon blocks nothing.
	state := t.TempDir()
	res := runWrapper(t, "deploy/devlab-restart-when-free", map[string]string{"DEVLAB_STATE_DIR": state}, "--check")
	if res.exit != 0 || strings.TrimSpace(res.out) != "dead" {
		t.Errorf("no socket ⇒ dead (free), got exit %d %q", res.exit, res.out)
	}

	// 423 ⇒ busy.
	busyState := t.TempDir()
	startReadySocket(t, busyState, http.StatusLocked)
	res = runWrapper(t, "deploy/devlab-restart-when-free", map[string]string{"DEVLAB_STATE_DIR": busyState}, "--check")
	if res.exit != 0 || strings.TrimSpace(res.out) != "busy" {
		t.Errorf("423 ⇒ busy, got exit %d %q", res.exit, res.out)
	}

	// 204 ⇒ free.
	freeState := t.TempDir()
	startReadySocket(t, freeState, http.StatusNoContent)
	res = runWrapper(t, "deploy/devlab-restart-when-free", map[string]string{"DEVLAB_STATE_DIR": freeState}, "--check")
	if res.exit != 0 || strings.TrimSpace(res.out) != "free" {
		t.Errorf("204 ⇒ free, got exit %d %q", res.exit, res.out)
	}
}

func TestRestartWhenFreeValidatesKnobs(t *testing.T) {
	res := runWrapper(t, "deploy/devlab-restart-when-free",
		map[string]string{"DEVLAB_STATE_DIR": t.TempDir(), "DEVLAB_RESTART_POLL": "bogus"}, "--check")
	if res.exit != 2 {
		t.Errorf("garbled poll knob must die with exit 2, got %d\n%s", res.exit, res.out)
	}
}

// ─── the service CLI skeleton stays template-conform (smoke, no root) ────────

func TestServiceCLISmoke(t *testing.T) {
	res := runWrapper(t, "service", nil, "help")
	if res.exit != 0 || !strings.Contains(res.out, "Exit codes: 0 ok") {
		t.Fatalf("service help must exit 0 with the template exit-code contract, got %d\n%s", res.exit, res.out)
	}
	for _, verb := range []string{"setup", "build", "start", "stop", "restart", "status", "update", "uninstall", "version", "help"} {
		if !strings.Contains(res.out, verb) {
			t.Errorf("help must document the template verb %q", verb)
		}
	}

	res = runWrapper(t, "service", nil, "version")
	if res.exit != 0 || !strings.Contains(res.out, "devlab") {
		t.Errorf("service version must name the service, got %d %q", res.exit, res.out)
	}

	res = runWrapper(t, "service", nil, "no-such-verb")
	if res.exit != 2 {
		t.Errorf("unknown verb must exit 2 (template convention), got %d\n%s", res.exit, res.out)
	}
}
