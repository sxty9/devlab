package deploy

// THE ROOT OF THE INSTANCE — what a host answers with when the instance itself is called.
//
// Measured on 2026-08-09: the production host's edge answered every unmatched request out of ONE
// service's state directory (`handle { root * /var/lib/devlab/www }`), so calling the instance produced
// DevLab's login screen instead of the landscape dashboard — and the dashboard's own absence stayed
// invisible behind that substitute. Two faults held each other up: the delivered dashboard was never
// installed, and the edge covered the gap with somebody else's files.
//
// A SECOND fault of the same family was measured on the same host on 2026-08-09 and is fixed together
// with it: that single site block accepted EVERY hostname, so three different names all got the same
// page, and two applications that each own `/api/*` collided inside it. The root of the instance is
// therefore no longer a property of the SHELL — it is the root application's own site block, on the
// hostname this host declares for it. The shell holds no application at all.
//
// These tests measure the fix where it actually happens — against the real caddy, on a real listener,
// through the very templates the receiver writes:
//   a) a host that HAS the root application answers ITS HOSTNAME with the dashboard,
//   b) no host answers with another service's interface,
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

// renderEdge builds the host edge SHELL from the SHARED template, with the service tree under `host` —
// the same call devlab-install-recv --provision makes, so what is measured here is what a provisioned
// host gets, not a Go copy of it. The shell alone carries no application.
func renderEdge(t *testing.T, host, conf, addr string) string {
	t.Helper()
	return bashRenderTemplate(t, "SETUP_SERVICE_ROOT="+host+"; setup_edge_caddyfile_text "+conf+" "+addr)
}

// instanceRootHost is the hostname a fixture host declares for its root application. A real host reads
// this from its own runtime configuration (/etc/holistic/edge/hosts/<id>, written by --edge-host); no
// name is ever baked into a repository, this test included — it is a fixture value, like the port.
const instanceRootHost = "dashboard.example.test"

// deliverDashboardBlock writes the DASHBOARD application's site block onto the apps shelf, exactly as the
// installer's route step does when the landscape dashboard is delivered: on the hostname the host
// declares for it, carrying the landscape's uniform services, serving its own face at the root of that
// name. `port` is the edge's own port; `api` is the dashboard daemon's loopback port.
func deliverDashboardBlock(t *testing.T, conf, serveRoot string, port, api int) {
	t.Helper()
	mustWrite(t, filepath.Join(conf, "apps", SETUP_ROOT_APP_ID+".caddy"), bashRenderTemplate(t,
		fmt.Sprintf("setup_app_route_text %s %s %d %d %s 1", SETUP_ROOT_APP_ID, instanceRootHost, port, api, serveRoot)))
}

// SETUP_ROOT_APP_ID mirrors the library's SETUP_ROOT_APP — which service IS the landscape's dashboard.
// It is a landscape fact, not an instance value, which is why it may stand in the repository at all.
const SETUP_ROOT_APP_ID = "holistic"

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

// serveEdge runs the REAL caddy on the rendered edge and returns a getter that sets the HOST HEADER — the
// only thing that decides which application answers, and therefore the only honest way to ask. It is THE
// one edge runner these tests share.
//
// The product bytes run UNMODIFIED: the admin endpoint is moved to a private unix socket through the
// CADDY_ADMIN environment rather than through an `admin off` wrapper around the config. That is not a
// convenience — the shell now carries a global options block of its own (default_bind), and a wrapper
// that prepends a second one is refused by caddy outright ("server block without any key is global
// configuration, and if used, it must be first"). A per-instance admin socket also keeps two concurrently
// running test edges from both claiming 127.0.0.1:2019, which is how a `caddy reload` in an environment
// with several caddys reaches the wrong one.
func serveEdge(t *testing.T, edge string, port int) func(host, path string) (int, string) {
	t.Helper()
	if _, err := exec.LookPath("caddy"); err != nil {
		t.Skip("caddy not installed — the edge cannot be measured on this machine")
	}
	dir := t.TempDir()
	edgeFile := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(edgeFile, []byte(edge), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "caddy", "run", "--config", edgeFile, "--adapter", "caddyfile")
	cmd.Env = append(os.Environ(), "CADDY_ADMIN=unix/"+filepath.Join(dir, "admin.sock"))
	var log strings.Builder
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("caddy did not start: %v", err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })

	get := func(host, path string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		if err != nil {
			return 0, err.Error()
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
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
		if code, _ := get("", "/"); code != 0 {
			return get
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the edge never came up:\n%s", log.String())
	return nil
}

// TESTS a + c, measured at the host: the root application's HOSTNAME answers with its dashboard once that
// application is installed, and says the interface is missing while it is not — never a 404 over a
// deficiency nobody named, and never a substitute. The face is put in place by the very function the
// installer and the production receiver use (setup_install_web), from a package that declares where it
// belongs, so what is measured is the whole path: package → install → what the browser gets.
//
// THE THREE STATES ARE TOLD APART, and each is answered in its own words:
//
//	the application is delivered and its face is here → the face, deep links included;
//	the application is delivered and its face is NOT  → 503 naming the missing interface;
//	no application answers to this name at all        → 404 naming that.
func TestInstanceRootAnswersWithTheRootApplication(t *testing.T) {
	host := t.TempDir() // this fixture host's /opt
	conf := filepath.Join(host, "conf.d")
	if err := os.MkdirAll(filepath.Join(conf, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	deliverDashboardBlock(t, conf, filepath.Join(host, SETUP_ROOT_APP_ID, "www"), port, freePort(t))
	edge := renderEdge(t, host, conf, fmt.Sprintf("127.0.0.1:%d", port))
	get := serveEdge(t, edge, port)

	// (c) BEFORE the root application's face is delivered: the answer states the fact, under its own name.
	code, body := get(instanceRootHost, "/")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("without the root application's face its hostname must answer 503, got %d: %s", code, body)
	}
	for _, want := range []string{"its interface is not", "No other application's interface"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer must say what is the case (missing %q):\n%s", want, body)
		}
	}
	// A deep link is not a different case: the face is absent, whatever is asked of it.
	if code, _ := get(instanceRootHost, "/mercury/todo"); code != http.StatusServiceUnavailable {
		t.Errorf("a deep link must get the same honest answer, got %d", code)
	}
	// And a name NOBODY claims is a different state again, answered in different words.
	code, body = get("nobody.example.test", "/")
	if code != http.StatusNotFound || !strings.Contains(body, "no root application answering to the name") {
		t.Errorf("an unclaimed hostname must be refused in its own words, got %d: %s", code, body)
	}

	// (a) The root application's package arrives and is installed by the shared installer.
	root := filepath.Join(host, SETUP_ROOT_APP_ID, "www")
	art := facePackage(t, root, true)
	res := sourceLib(t, map[string]string{"DEVLAB_SERVICE_ROOT": host}, "setup_install_web "+art+" "+SETUP_ROOT_APP_ID)
	if res.exit != 0 {
		t.Fatalf("installing the root application's face must succeed:\n%s", res.out)
	}

	code, body = get(instanceRootHost, "/")
	if code != http.StatusOK || !strings.Contains(body, "dashboard") {
		t.Fatalf("with the root application installed its hostname must serve the dashboard, got %d: %s", code, body)
	}
	// The dashboard is a single-page application: a deep link is its start page, not a 404.
	if code, body := get(instanceRootHost, "/mercury/todo"); code != http.StatusOK || !strings.Contains(body, "dashboard") {
		t.Errorf("a deep link must reach the dashboard's start page, got %d: %s", code, body)
	}
}

// TEST b: no host serves another service's interface in the SHELL. The shell now holds no application at
// all — not even the right one: an application's serve root travels in its OWN site block, written when
// that application is delivered. So the shell must name no service directory whatsoever, and it must
// still take no serve root as an argument, because a caller must not be able to point a name at a
// service of its choosing.
func TestInstanceRootNamesTheRootApplicationOnly(t *testing.T) {
	edge := bashRenderTemplate(t, "setup_edge_caddyfile_text /etc/caddy/conf.d 10.10.0.1:8080")
	for _, forbidden := range []string{"/var/lib/devlab", "/var/lib/holistic", "/opt/holistic/www", "/usr/share/caddy"} {
		if strings.Contains(edge, forbidden) {
			t.Errorf("the edge shell must serve no service's directory (%s):\n%s", forbidden, edge)
		}
	}
	// It also names no HOSTNAME: which name reaches which application is the host's runtime configuration,
	// never a value in a repository (Keine Instanz-Spezifika). Only the declared socket appears.
	if strings.Contains(edge, "http://dash") || strings.Contains(edge, ".example.") {
		t.Errorf("the edge shell must carry no hostname of any instance:\n%s", edge)
	}
	// The template takes no serve root: a caller cannot point a name at a service of its choosing,
	// because there is no argument through which to do it.
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
