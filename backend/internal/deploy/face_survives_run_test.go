package deploy

// THE DELIVERED FACE MUST SURVIVE THE RUN THAT DELIVERS IT — and the success mark must belong to the
// END of that run.
//
// What was measured on the production host on 2026-08-09, after a complete delivery of the landscape
// dashboard: the receiver reported the face installed at its serve root, the service's directory held
// nothing but the payload's own pybase and venv, and the instance root answered 503 "no root
// application". Both statements were true at different moments of the SAME run: the face was installed
// first, and the program half — a python-app payload copied with `rsync -a --delete` over the whole
// service directory — removed everything the payload did not carry, the freshly installed face
// included. The mark was issued at the copy, so it never saw the deletion that followed it.
//
// These tests measure the two halves of that fault ON A HOST, through the REAL receiver, and read the
// result from the host rather than from the report:
//
//	a) DECISIVE — after a complete delivery, the face lies at the declared serve root and the instance
//	   root SERVES it (a real caddy edge, a real HTTP request).
//	b) a second delivery over the same host leaves the face standing.
//	c) a face that really is gone at the end of the run still fails the delivery, by name.
//	d) a go-daemon carrying a face behaves identically — the rule is not per build kind.
//
// The receiver runs unprivileged against a fixture host through its DIRECT-INVOCATION seam
// (DEVLAB_RECV_TEST, which sudo's env_reset and sshd's forced command both strip): the ownership flags
// of the system files it installs are dropped and the operating-system account is not created. Every
// other step is the real one — the same copies, the same gates, the same order.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureHost is one fixture production host with one service's delivery staged for it.
type fixtureHost struct {
	repo      string
	root      string // the host's service root (its /opt)
	art       string // the staged artifact the receiver installs from
	serveRoot string // where the PACKAGE declares its face belongs
	env       map[string]string
}

// stageDelivery builds a fixture host and stages a complete delivery of <repo> for it: the prebuilt
// program of the declared build kind, the built web bundle plus the `web.root` declaration that says
// where it belongs, and the setup/<unit>.service the first-time setup installs. This is the shape
// artifact-build produces — nothing here is invented for the test but the bytes.
func stageDelivery(t *testing.T, repo, kind string) *fixtureHost {
	t.Helper()
	host := t.TempDir()
	staging := t.TempDir()
	unitDir := t.TempDir()
	etc := t.TempDir()
	art := filepath.Join(staging, repo)
	serveRoot := filepath.Join(host, repo, "www")

	setup := filepath.Join(art, "setup")
	web := filepath.Join(art, "web", "assets")
	for _, d := range []string{setup, web} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The face, written with the build account's group-only mode — the shape the install must deliver
	// readable by its public role whatever the builder's umask was.
	if err := os.WriteFile(filepath.Join(art, "web", "index.html"), []byte("<html>dashboard</html>"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "app.js"), []byte("console.log(1)"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "web.root"), []byte(serveRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The program, per declared build kind — prebuilt, never built by an installer.
	switch kind {
	case "go-daemon":
		if err := os.WriteFile(filepath.Join(art, repo+"d"), []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	case "python-app":
		payload := filepath.Join(art, "payload")
		if err := os.MkdirAll(filepath.Join(payload, "venv", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(payload, "venv", "bin", "uvicorn"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		stageBundledPython(t, payload, true)
	default:
		t.Fatalf("unknown build kind %q", kind)
	}
	stampBuildKind(t, art, kind)
	// The delivered unit is the source of truth for how the service starts. It names no port, so the
	// running gate proves the unit alone (the port half is measured by its own tests).
	unit := "[Unit]\nDescription=" + repo + "\n\n[Service]\nUser=" + repo +
		"\nEnvironment=HOLISTIC_SECRET_FILE=" + filepath.Join(etc, "jwt-secret") +
		"\nExecStart=/opt/" + repo + "/venv/bin/uvicorn\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(filepath.Join(setup, repo+".service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}

	return &fixtureHost{
		repo: repo, root: host, art: art, serveRoot: serveRoot,
		env: map[string]string{
			"DEVLAB_RECV_TEST":        "1",
			"DEVLAB_STAGING":          staging,
			"DEVLAB_UNIT_DIR":         unitDir,
			"DEVLAB_SERVICE_ROOT":     host,
			"DEVLAB_HOLISTIC_DIR":     etc,
			"DEVLAB_HOLISTIC_PERMS":   filepath.Join(etc, "permissions.d"),
			"DEVLAB_CADDY_CONF":       filepath.Join(etc, "conf.d"),
			"DEVLAB_GETENT":           fakeGetent(t, nil),
			"DEVLAB_DPKG":             noPackages(t),
			"DEVLAB_SYSTEMCTL":        fakeSystemctl(t, ""),
			"DEVLAB_GATE_COMEUP_SECS": "5",
			"DEVLAB_GATE_DWELL_SECS":  "1",
		},
	}
}

// deliver runs the REAL receiver — no --check, no plan — exactly as the forced command does.
func (f *fixtureHost) deliver(t *testing.T) wrapperResult {
	t.Helper()
	return runWrapper(t, "deploy/devlab-deploy-recv", f.env, f.repo)
}

// servedByTheInstanceRoot answers what a browser asking this fixture host for the instance root gets,
// through the REAL caddy on the edge the host provisioning writes (setup_edge_caddyfile_text). It is
// the measurement the report cannot make for itself: what the user's browser receives.
func (f *fixtureHost) servedByTheInstanceRoot(t *testing.T) (int, string) {
	t.Helper()
	conf := filepath.Join(f.root, "conf.d")
	if err := os.MkdirAll(filepath.Join(conf, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	// The dashboard is reached under the hostname this host declares for it, so the browser's question
	// carries that name — which is exactly what cloudflared puts in front of this edge in production.
	deliverDashboardBlock(t, conf, f.serveRoot, port, freePort(t))
	get := serveEdge(t, renderEdge(t, f.root, conf, fmt.Sprintf("127.0.0.1:%d", port)), port)
	return get(instanceRootHost, "/")
}

// TEST a — DECISIVE: after a COMPLETE delivery of the landscape dashboard (a python-app, the build kind
// the fault was found on) the face is at the serve root its package declares, the program is installed
// beside it, and the instance root SERVES the face. Measured at the host: the files are read off the
// host and the answer is fetched over HTTP — not read off the delivery's own report.
func TestDeliveredFaceSurvivesTheProgramInstall(t *testing.T) {
	f := stageDelivery(t, "holistic", "python-app")

	res := f.deliver(t)
	if res.exit != 0 {
		t.Fatalf("a complete delivery must succeed, got %d\n%s", res.exit, res.out)
	}
	// The program half really ran — otherwise "the face survived" would prove nothing.
	if _, err := os.Stat(filepath.Join(f.root, "holistic", "venv", "bin", "uvicorn")); err != nil {
		t.Fatalf("the prebuilt payload must be installed into the service directory: %v\n%s", err, res.out)
	}
	// …and the face is still there when the run is over.
	for _, rel := range []string{"index.html", filepath.Join("assets", "app.js")} {
		if _, err := os.Stat(filepath.Join(f.serveRoot, rel)); err != nil {
			t.Fatalf("the delivered face must be at the declared serve root after the run: %v\n%s", err, res.out)
		}
	}
	if !strings.Contains(res.out, "MERCURY-WEB: installed | "+f.serveRoot) {
		t.Errorf("the outcome must be stated on the one machine-readable line:\n%s", res.out)
	}

	// THE MEASUREMENT THAT MATTERS: the instance root answers with the dashboard.
	code, body := f.servedByTheInstanceRoot(t)
	if code != http.StatusOK || !strings.Contains(body, "dashboard") {
		t.Fatalf("the instance root must serve the delivered face, got %d: %s", code, body)
	}
}

// TEST b: a SECOND delivery over the same host — the update path, the one a renewal takes — leaves the
// face standing and serving. A boundary that holds only on the first run is not a boundary.
func TestSecondDeliveryLeavesTheFaceStanding(t *testing.T) {
	f := stageDelivery(t, "holistic", "python-app")
	if res := f.deliver(t); res.exit != 0 {
		t.Fatalf("the first delivery must succeed, got %d\n%s", res.exit, res.out)
	}
	res := f.deliver(t)
	if res.exit != 0 {
		t.Fatalf("a second delivery must succeed, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "Aktualisierung of 'holistic'") {
		t.Errorf("the second run must take the update path (the unit is now on the host):\n%s", res.out)
	}
	if _, err := os.Stat(filepath.Join(f.serveRoot, "index.html")); err != nil {
		t.Fatalf("the face must still be at the serve root after the second delivery: %v\n%s", err, res.out)
	}
	if !strings.Contains(res.out, "MERCURY-WEB: installed | "+f.serveRoot) {
		t.Errorf("the second run must state the face's end state too:\n%s", res.out)
	}
	code, body := f.servedByTheInstanceRoot(t)
	if code != http.StatusOK || !strings.Contains(body, "dashboard") {
		t.Fatalf("the instance root must still serve the face after a second delivery, got %d: %s", code, body)
	}
}

// TEST c: a face that is REALLY gone at the end of the run fails the delivery, by name — the honesty
// half of the fix. Something else in the run removes the serve root after it was installed (here: the
// service start, standing in for whatever step deletes it — that is exactly how the fault presented,
// as a later step of the same run taking the face with it). The success mark is measured at the END,
// so the run that would have reported "installed" now reports failure and fails the delivery.
func TestAFaceThatIsGoneAtTheEndFailsTheDelivery(t *testing.T) {
	f := stageDelivery(t, "holistic", "python-app")
	// A systemctl that wipes the serve root when the service is started.
	wiper := filepath.Join(t.TempDir(), "systemctl")
	script := "#!/bin/sh\ncase \"$1\" in\n  start|restart) rm -rf '" + f.serveRoot + "' ;;\n  show) printf '\\n' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(wiper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env["DEVLAB_SYSTEMCTL"] = wiper

	res := f.deliver(t)
	if res.exit != 11 {
		t.Fatalf("a face that is gone at the end must fail the delivery with exit 11, got %d\n%s", res.exit, res.out)
	}
	if !strings.Contains(res.out, "MERCURY-WEB: failed") {
		t.Errorf("the failure must be stated on the machine-readable line:\n%s", res.out)
	}
	if strings.Contains(res.out, "MERCURY-WEB: installed") {
		t.Errorf("a run that lost the face must never also claim it installed one:\n%s", res.out)
	}
	for _, want := range []string{"now that the run has finished", "did not survive"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the reason must name the end state (missing %q):\n%s", want, res.out)
		}
	}
}

// TEST d: a GO service that carries a face behaves identically — same install, same end-state verdict,
// same survival across a second delivery. The rule is one rule; there is no special case per build kind.
func TestGoServiceWithAFaceBehavesIdentically(t *testing.T) {
	f := stageDelivery(t, "prizm", "go-daemon")

	res := f.deliver(t)
	if res.exit != 0 {
		t.Fatalf("a go-daemon delivery carrying a face must succeed, got %d\n%s", res.exit, res.out)
	}
	if _, err := os.Stat(filepath.Join(f.root, "prizm", "bin", "prizmd")); err != nil {
		t.Fatalf("the prebuilt binary must be installed into the program's own directory: %v\n%s", err, res.out)
	}
	if _, err := os.Stat(filepath.Join(f.serveRoot, "index.html")); err != nil {
		t.Fatalf("the delivered face must be at the declared serve root after the run: %v\n%s", err, res.out)
	}
	if !strings.Contains(res.out, "MERCURY-WEB: installed | "+f.serveRoot) {
		t.Errorf("a go service must state the same end state on the same line:\n%s", res.out)
	}
	if res2 := f.deliver(t); res2.exit != 0 || !strings.Contains(res2.out, "MERCURY-WEB: installed | "+f.serveRoot) {
		t.Errorf("a second go-daemon delivery must leave the face standing too (exit %d):\n%s", res2.exit, res2.out)
	}
}

// THE BOUNDARY ITSELF, at the one place it lives: the program copy removes what the PROGRAM left behind
// and nothing else. Both halves are proved together, because either alone would be satisfiable by a
// wrong fix: dropping --delete entirely would keep the face and stop cleaning up after the program.
func TestProgramCopyCleansItsOwnObjectOnly(t *testing.T) {
	host := t.TempDir()
	art := t.TempDir()
	dest := filepath.Join(host, "holistic")
	payload := filepath.Join(art, "payload")
	if err := os.MkdirAll(filepath.Join(payload, "venv", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "venv", "bin", "uvicorn"), []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A payload that happens to carry files where the OTHER half lives must not be able to write there
	// either — overwriting the face right after it was installed is the same fault from the other side.
	if err := os.MkdirAll(filepath.Join(payload, "www"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "www", "index.html"), []byte("<html>from the payload</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(art, "web.root"), []byte(filepath.Join(dest, "www")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The host as a previous delivery left it: the face of this service, plus a program file the new
	// payload no longer carries.
	if err := os.MkdirAll(filepath.Join(dest, "www", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "www", "index.html"), []byte("<html>face</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "www", "assets", "app.js"), []byte("face"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dest, "oldlib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "oldlib", "gone.py"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := sourceLib(t, map[string]string{"DEVLAB_SERVICE_ROOT": host},
		"setup_install_program "+art+" holistic python-app")
	if res.exit != 0 {
		t.Fatalf("the program install must succeed, got %d\n%s", res.exit, res.out)
	}
	// The other half's territory is untouched, contents included — nothing removed from it…
	for _, rel := range []string{filepath.Join("www", "index.html"), filepath.Join("www", "assets", "app.js")} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("the program copy must not remove what another half of the service owns (%s): %v", rel, err)
		}
	}
	// …and nothing written into it either.
	if b, err := os.ReadFile(filepath.Join(dest, "www", "index.html")); err != nil || !strings.Contains(string(b), "face") {
		t.Errorf("the program copy must not write into another half's territory, got %q (err %v)", string(b), err)
	}
	// …and the delete still does its work inside the program's own object.
	if _, err := os.Stat(filepath.Join(dest, "oldlib")); err == nil {
		t.Error("the program copy must still remove what a PREVIOUS payload left behind — the delete is bounded, not switched off")
	}
	if _, err := os.Stat(filepath.Join(dest, "venv", "bin", "uvicorn")); err != nil {
		t.Errorf("the new payload must arrive: %v", err)
	}
}

// The kept-out path is DERIVED from the package's own declaration, never from a fixed directory name: a
// service that declares its face somewhere else inside its own tree is protected there, and a face that
// lies outside the program's destination needs no keeping out at all.
func TestProgramKeepoutsComeFromTheDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		declare string
		want    string
	}{
		{"the serve convention", "/opt/holistic/www", "www"},
		{"a face the package puts elsewhere in its own tree", "/opt/holistic/public/site", "public/site"},
		{"a face outside the program's destination", "/var/lib/holistic/www", ""},
		{"no face at all", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			art := t.TempDir()
			if tc.declare != "" {
				if err := os.WriteFile(filepath.Join(art, "web.root"), []byte(tc.declare+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res := sourceLib(t, nil, "setup_program_keepouts "+art+" holistic /opt/holistic")
			if got := strings.TrimSpace(res.out); got != tc.want {
				t.Errorf("the keepout must be derived from the declaration %q: want %q, got %q", tc.declare, tc.want, got)
			}
		})
	}
	// A declaration the face half would REFUSE grants no keepout either — otherwise a package could name
	// a directory it may not own and have the program copy spare it.
	art := t.TempDir()
	if err := os.WriteFile(filepath.Join(art, "web.root"), []byte("/opt/holistic/../prizm/www\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := sourceLib(t, nil, "setup_program_keepouts "+art+" holistic /opt/holistic")
	if strings.TrimSpace(res.out) != "" {
		t.Errorf("an inadmissible declaration must grant no keepout, got %q", res.out)
	}
}

// ONE program install for both hosts: neither wrapper copies anything into a service directory of its
// own accord any more, so the workbench and a production host cannot drift on what a program install
// may remove. (Each wrapper keeps the copies of its OWN objects — the dashboard wire-in and serve on
// the workbench; those are not program installs and do not touch a service's directory.)
func TestBothInstallersTakeTheOneProgramInstall(t *testing.T) {
	for _, script := range []string{"devlab-install", "devlab-deploy-recv"} {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", script))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, "setup_install_program") {
			t.Errorf("%s must install the program through the shared installer", script)
		}
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "/opt/") {
				continue
			}
			if strings.Contains(trimmed, "rsync") || strings.HasPrefix(trimmed, "install ") ||
				strings.HasPrefix(trimmed, "run install ") || strings.Contains(trimmed, "rm -rf") {
				t.Errorf("%s must not write into a service directory itself — the program install and its boundary live in one place: %s", script, trimmed)
			}
		}
	}
}
