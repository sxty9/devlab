package deploy

// THE ROOT OF THE INSTANCE — what a host answers with when the instance itself is called.
//
// Measured on 2026-08-09: the production host's edge answered every unmatched request out of ONE
// service's state directory (`handle { root * /var/lib/devlab/www }`), so calling the instance produced
// DevLab's login screen instead of the landscape dashboard — and the dashboard's own absence stayed
// invisible behind that substitute. Two faults held each other up: the delivered dashboard was never
// installed, and the edge covered the gap with somebody else's files.
//
// These tests measure the fix where it actually happens — against the real caddy, on a real listener,
// through the very template the receiver writes:
//   a) a host that HAS the root application answers the instance root with its dashboard,
//   b) no host answers the instance root with another service's interface,
//   c) a host WITHOUT the root application says exactly that,
//   d) a search over the receiver finds no hardcoded directory of a single service.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// renderEdge builds the host edge from the SHARED template, with the service tree under `host` — the
// same call devlab-install-recv --provision makes, so what is measured here is what a provisioned host
// gets, not a Go copy of it.
func renderEdge(t *testing.T, host, conf, addr string) string {
	t.Helper()
	return bashRenderTemplate(t, "SETUP_SERVICE_ROOT="+host+"; setup_edge_caddyfile_text "+conf+" "+addr)
}

// freePort asks the kernel for a port nothing holds, so the live edge below binds without colliding.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// serveEdge runs the REAL caddy on the rendered edge and returns a getter for one request. The admin
// endpoint is switched off through a harness file that only IMPORTS the rendered edge, so the product
// under test is unmodified.
func serveEdge(t *testing.T, edge string, port int) func(path string) (int, string) {
	t.Helper()
	if _, err := exec.LookPath("caddy"); err != nil {
		t.Skip("caddy not installed — the instance root cannot be measured on this machine")
	}
	dir := t.TempDir()
	edgeFile := filepath.Join(dir, "edge.caddy")
	if err := os.WriteFile(edgeFile, []byte(edge), 0o644); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(harness, []byte("{\n\tadmin off\n}\nimport "+edgeFile+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "caddy", "run", "--config", harness, "--adapter", "caddyfile")
	var log strings.Builder
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("caddy did not start: %v", err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b := make([]byte, 4096)
		n, _ := resp.Body.Read(b)
		return resp.StatusCode, string(b[:n])
	}
	// The listener is up when it answers at all — whatever it answers.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := get("/"); code != 0 {
			return get
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the edge never came up:\n%s", log.String())
	return nil
}

// TESTS a + c, measured at the host: the instance root answers with the ROOT APPLICATION's dashboard
// once that application is installed, and says the root application is missing while it is not — never
// a 404 and never a substitute. The face is put in place by the very function the installer and the
// production receiver use (setup_install_web), from a package that declares where it belongs, so what
// is measured is the whole path: package → install → what the browser gets.
func TestInstanceRootAnswersWithTheRootApplication(t *testing.T) {
	host := t.TempDir() // this fixture host's /opt
	conf := filepath.Join(host, "conf.d")
	if err := os.MkdirAll(conf, 0o755); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	edge := renderEdge(t, host, conf, fmt.Sprintf("127.0.0.1:%d", port))
	get := serveEdge(t, edge, port)

	// (c) BEFORE the root application is delivered: the answer states the fact.
	code, body := get("/")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("without the root application the instance root must answer 503, got %d: %s", code, body)
	}
	for _, want := range []string{"no root application", "holistic", "No other service's interface"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer must say what is the case (missing %q):\n%s", want, body)
		}
	}
	// A deep link is not a different case: the instance has no root application, whatever is asked of it.
	if code, _ := get("/mercury/todo"); code != http.StatusServiceUnavailable {
		t.Errorf("a deep link must get the same honest answer, got %d", code)
	}

	// (a) The root application's package arrives and is installed by the shared installer.
	root := filepath.Join(host, "holistic", "www")
	art := facePackage(t, root, true)
	res := sourceLib(t, map[string]string{"DEVLAB_SERVICE_ROOT": host}, "setup_install_web "+art+" holistic")
	if res.exit != 0 {
		t.Fatalf("installing the root application's face must succeed:\n%s", res.out)
	}

	code, body = get("/")
	if code != http.StatusOK || !strings.Contains(body, "dashboard") {
		t.Fatalf("with the root application installed the instance root must serve its dashboard, got %d: %s", code, body)
	}
	// The dashboard is a single-page application: a deep link is its start page, not a 404.
	if code, body := get("/mercury/todo"); code != http.StatusOK || !strings.Contains(body, "dashboard") {
		t.Errorf("a deep link must reach the dashboard's start page, got %d: %s", code, body)
	}
}

// TEST b: no host serves another service's interface at the instance root. The edge is built from ONE
// template, so this is decided there: the last block names the root application's serve root and nothing
// else — no state directory of a service, no path handed in by whoever builds the edge.
func TestInstanceRootNamesTheRootApplicationOnly(t *testing.T) {
	edge := bashRenderTemplate(t, "setup_edge_caddyfile_text /etc/caddy/conf.d 10.10.0.1:8080")
	if !strings.Contains(edge, "root * /opt/holistic/www") {
		t.Errorf("the instance root must serve the root application's serve root:\n%s", edge)
	}
	for _, forbidden := range []string{"/var/lib/devlab", "/var/lib/holistic", "/usr/share/caddy"} {
		if strings.Contains(edge, forbidden) {
			t.Errorf("the edge must not serve %s at the instance root:\n%s", forbidden, edge)
		}
	}
	// The template takes no serve root: a caller cannot point the instance root at a service of its
	// choosing, because there is no argument through which to do it.
	res := sourceLib(t, nil, "setup_edge_caddyfile_text /etc/caddy/conf.d /var/lib/devlab/www 10.10.0.1:8080")
	if res.exit == 0 {
		t.Errorf("the edge template must not accept a serve root as an argument any more:\n%s", res.out)
	}
}

// TEST d: a search over the RECEIVER — the script that installs on a production host — finds no
// hardcoded directory of a single service. Where a face goes is read from the package; what the
// instance root answers with is read from the shared decision. Neither is written into this script.
func TestReceiverCarriesNoSingleServiceDirectory(t *testing.T) {
	for _, script := range []string{"devlab-deploy-recv", "devlab-install-recv"} {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", script))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, forbidden := range []string{
			"DEVLAB_STATIC_DIR", // the env that used to name one service's serve root
			"WWW_DIR",           // and the variable it fed
			"/var/lib/devlab/www",
			"/opt/holistic/www", // not even the RIGHT service is written in by hand
		} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s must not carry %q — a serve root is read from the package or from the shared decision, never written into the receiver", script, forbidden)
			}
		}
	}
	// What they carry INSTEAD, each in its own role: the install-only receiver installs every delivered
	// face through the shared installer, and the host provisioning takes the instance root from the
	// shared decision rather than deriving one.
	recv, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "devlab-deploy-recv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recv), "setup_install_web") {
		t.Error("devlab-deploy-recv must install a delivered face through the shared installer")
	}
	prov, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "devlab-install-recv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prov), "setup_root_app_www") {
		t.Error("devlab-install-recv must take the instance root from the shared decision (setup_root_app_www)")
	}
}
