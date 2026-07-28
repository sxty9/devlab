package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// These tests exercise the uniform per-repo deploy script (deploy/deploy.d.goservice) end to end,
// stubbing systemctl/ss/install/sleep so the health gate can be driven deterministically. They pin the
// axiom "Kein stummes Ausbleiben": the script reports "installed and started" only once the service is
// actually running and holding its port, and treats an install after which it does not run as a failed
// setup (exit 12, which the runner already maps to a failed deploy).

func goservicePath(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	// backend/internal/api -> repo root -> deploy/deploy.d.goservice
	p := filepath.Join(filepath.Dir(self), "..", "..", "..", "deploy", "deploy.d.goservice")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("deploy script not found at %s: %v", p, err)
	}
	return p
}

func writeExecFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runGoservice runs the deploy script for a synthetic service "svcx" in dev mode against stubbed tools.
// active drives systemctl is-active; listen is the space-separated set of ports ss reports as LISTEN;
// confs maps service id -> routed port for the fake caddy conf dir. Returns combined output + exit code.
func runGoservice(t *testing.T, active, listen string, confs map[string]int) (string, int) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	sb := t.TempDir()
	bin := filepath.Join(sb, "bin")
	caddy := filepath.Join(sb, "caddy")
	art := filepath.Join(sb, "art")
	for _, d := range []string{bin, caddy, art} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecFile(t, filepath.Join(art, "svcxd"), "#!/bin/sh\n") // the prebuilt binary the script installs

	writeExecFile(t, filepath.Join(bin, "install"), `#!/usr/bin/env bash
args=(); mkdirs=0
while [[ $# -gt 0 ]]; do case "$1" in -d) mkdirs=1;; -o|-g|-m) shift;; -*) ;; *) args+=("$1");; esac; shift; done
if [[ $mkdirs -eq 1 ]]; then for d in "${args[@]}"; do mkdir -p "$d" 2>/dev/null || true; done; exit 0; fi
n=${#args[@]}; for ((i=0;i<n-1;i++)); do cp "${args[$i]}" "${args[$((n-1))]}" 2>/dev/null || true; done; exit 0
`)
	writeExecFile(t, filepath.Join(bin, "sleep"), "#!/usr/bin/env bash\nexit 0\n")
	writeExecFile(t, filepath.Join(bin, "systemctl"), `#!/usr/bin/env bash
case "$1" in
  restart) exit 0 ;;
  is-active) [[ "${FAKE_ACTIVE:-inactive}" == "active" ]] && exit 0 || exit 3 ;;
esac
exit 0
`)
	writeExecFile(t, filepath.Join(bin, "ss"), `#!/usr/bin/env bash
want=""; for a in "$@"; do [[ "$a" == *:* ]] && want="${a##*:}"; done
for p in ${FAKE_LISTEN:-}; do [[ "$p" == "$want" ]] && { echo "LISTEN 0 0 127.0.0.1:$p 0.0.0.0:*"; exit 0; }; done
exit 0
`)

	for id, port := range confs {
		conf := "handle {\n  reverse_proxy 127.0.0.1:" + strconv.Itoa(port) + "\n}\n"
		if err := os.WriteFile(filepath.Join(caddy, id+".caddy"), []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Copy the script in under the name "svcx" so REPO (basename "$0") is svcx.
	src, err := os.ReadFile(goservicePath(t))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(sb, "svcx")
	writeExecFile(t, script, string(src))

	cmd := exec.Command(script, art, "dev")
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DEVLAB_CADDY_CONF="+caddy,
		"DEVLAB_PORT_BAND_LO=8770",
		"DEVLAB_PORT_BAND_HI=8785",
		"DEVLAB_DEPLOY_HEALTH_WAIT=1",
		"FAKE_ACTIVE="+active,
		"FAKE_LISTEN="+listen,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run: %v\n%s", err, out)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

// A setup after which the service does not run is a failed setup — never a silent success.
func TestGoserviceFailsWhenServiceNotRunning(t *testing.T) {
	out, code := runGoservice(t, "inactive", "", map[string]int{"svcx": 8790})
	if code != 12 {
		t.Fatalf("exit = %d, want 12\n%s", code, out)
	}
	if !strings.Contains(out, "FAILED") || !strings.Contains(out, "failed setup") {
		t.Errorf("want a failed-setup message:\n%s", out)
	}
	if strings.Contains(out, "installed and started") {
		t.Errorf("must not claim it started when it did not:\n%s", out)
	}
}

// The positive control: active AND holding its port -> the started message.
func TestGoserviceSucceedsWhenRunningAndHoldingPort(t *testing.T) {
	out, code := runGoservice(t, "active", "8790", map[string]int{"svcx": 8790})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "installed and started") {
		t.Errorf("want the started message:\n%s", out)
	}
}

// Active but not actually listening on its port (bound elsewhere / did not come up) is still a failure.
func TestGoserviceFailsWhenActiveButPortNotHeld(t *testing.T) {
	out, code := runGoservice(t, "active", "", map[string]int{"svcx": 8790})
	if code != 12 {
		t.Fatalf("exit = %d, want 12\n%s", code, out)
	}
}

// The prizm case at deploy time: the service is dead because its port is already held — name the holder
// and propose a free port instead of failing silently.
func TestGoserviceNamesConflictAndProposesFree(t *testing.T) {
	confs := map[string]int{"svcx": 8780, "aigentic": 8780}
	for p := 8770; p <= 8783; p++ {
		if p != 8780 {
			confs["occ"+strconv.Itoa(p)] = p // occupy the rest so the free port is deterministically 8784
		}
	}
	out, code := runGoservice(t, "inactive", "8780", confs)
	if code != 12 {
		t.Fatalf("exit = %d, want 12\n%s", code, out)
	}
	if !strings.Contains(out, "held by aigentic") {
		t.Errorf("must name the holder aigentic:\n%s", out)
	}
	if !strings.Contains(out, "free is 8784") {
		t.Errorf("must propose the free port 8784:\n%s", out)
	}
}
