package deploy

// THE EDGE TELLS HOSTNAMES APART — the refusals that keep it that way, and the split that makes it safe.
//
// Measured on production on 2026-08-09: every hostname got the same page, holistic's own API answered 404
// through the edge while answering 200 directly, and nobody could log in to holistic because
// /api/auth/login reached DevLab. The cause was double, and both halves are one mistake made twice — a
// property of the PACKAGE derived from its NAME:
//   * /etc/holistic/edge-address held ':8080' with no host part, so the ONE site block accepted every name;
//   * two files in the flat route directory both claimed `handle /api/*`, and `import` expands
//     alphabetically, so devlab.caddy won and holistic's API ceased to exist.
//
// The shape of the fix is measured elsewhere (a site block per hostname, against the real caddy). What is
// measured HERE is what the fix REFUSES, because a routing rule that cannot say no is a routing rule that
// will be taken over:
//   * a package declares its ROLE, a host declares its NAME, and neither can state the other's half;
//   * a root application on a host that names none for it fails BY NAME and writes nothing;
//   * an instance has exactly ONE dashboard, and a second is refused while the first still stands;
//   * the two readers of the repository's declaration — the build wrapper's and the daemon's — agree.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/model"
)

// edgeFixture stages one artifact that DECLARES an edge role, plus a fixture host's runtime configuration
// (where the edge answers, and which names it gives to which applications). It returns the arguments
// setup_prepare_route is called with, so a test asks the very switch both installers ask.
func edgeFixture(t *testing.T, repo, role, serveRoot string, hosts map[string]string) (env map[string]string, art, conf string) {
	t.Helper()
	root := t.TempDir()
	art = filepath.Join(root, "artifact")
	conf = filepath.Join(root, "conf.d")
	for _, d := range []string{art, filepath.Join(conf, "apps"), filepath.Join(conf, "services")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if role != "" {
		if err := os.WriteFile(filepath.Join(art, "edge.role"), []byte(role+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if serveRoot != "" {
		stampWebRoot(t, art, serveRoot)
	}
	return map[string]string{
		"DEVLAB_EDGE_ADDRESS_FILE": edgeAddressFixture(t),
		"DEVLAB_EDGE_HOSTS_DIR":    edgeHostsFixture(t, hosts),
	}, art, conf
}

// prepareRoute asks the ONE switch both installers ask: where does this delivery's route go, and what does
// it say? On success its whole standard output is the destination path.
func prepareRoute(t *testing.T, env map[string]string, art, repo, port, conf string) (res wrapperResult, staged string) {
	t.Helper()
	staged = filepath.Join(t.TempDir(), "staged.caddy")
	return sourceLib(t, env, "setup_prepare_route "+art+" "+repo+" "+port+" "+conf+" '' "+staged), staged
}

// A ROOT APPLICATION IS REACHED UNDER A NAME, AND THE NAME IS THE HOST'S TO GIVE. A package that declares
// itself a root application on a host that declares no hostname for it does not quietly take the instance
// over and does not quietly become a uniform service: the delivery dies, by name, having written nothing.
//
// This is the guard the whole design rests on. Without it a package could declare itself a root
// application and be served under whatever name happened to be free — which is why the two halves must
// stay apart and must never later be "simplified" into one.
func TestRootApplicationWithoutAHostnameIsRefused(t *testing.T) {
	env, art, conf := edgeFixture(t, "holistic", "dashboard", "/opt/holistic/www", nil) // the host names none
	res, staged := prepareRoute(t, env, art, "holistic", "8770", conf)
	if res.exit == 0 {
		t.Fatalf("a root application with no declared hostname must be refused:\n%s", res.out)
	}
	for _, want := range []string{"root application", "no hostname", "--edge-host holistic="} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the refusal must name the deficiency and how to end it (%q missing):\n%s", want, res.out)
		}
	}
	// NOTHING was written — not the staged text, and above all nothing on either shelf.
	if b, err := os.ReadFile(staged); err == nil && len(b) > 0 {
		t.Errorf("a refused route must stage no text, got:\n%s", string(b))
	}
	for _, shelf := range []string{"apps", "services"} {
		entries, _ := os.ReadDir(filepath.Join(conf, shelf))
		if len(entries) != 0 {
			t.Errorf("a refused route must leave the %s shelf empty, got %d file(s)", shelf, len(entries))
		}
	}

	// With a name declared, the very same delivery produces its site block on the apps shelf.
	env2, art2, conf2 := edgeFixture(t, "holistic", "dashboard", "/opt/holistic/www",
		map[string]string{"holistic": "dash.example.test"})
	res2, staged2 := prepareRoute(t, env2, art2, "holistic", "8770", conf2)
	if res2.exit != 0 {
		t.Fatalf("with a declared hostname the route must be prepared:\n%s", res2.out)
	}
	if got, want := strings.TrimSpace(res2.out), filepath.Join(conf2, "apps", "holistic.caddy"); got != want {
		t.Errorf("a root application belongs on the apps shelf: got %q, want %q", got, want)
	}
	b, err := os.ReadFile(staged2)
	if err != nil {
		t.Fatalf("the prepared route must be staged: %v", err)
	}
	for _, want := range []string{"http://dash.example.test:8080 {", "import holistic_service_routes",
		"handle /api/* {", "reverse_proxy 127.0.0.1:8770", "import app_web /opt/holistic/www"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the dashboard's site block is missing %q:\n%s", want, string(b))
		}
	}
}

// A package that declares NOTHING is a uniform service — the unprivileged default, and never the other way
// round. Silence must not be able to buy a whole hostname.
func TestSilenceMeansTheUniformService(t *testing.T) {
	env, art, conf := edgeFixture(t, "prizm", "", "", nil) // no edge.role stamp at all
	res, staged := prepareRoute(t, env, art, "prizm", "18811", conf)
	if res.exit != 0 {
		t.Fatalf("a package that declares nothing must still get its uniform route:\n%s", res.out)
	}
	if got, want := strings.TrimSpace(res.out), filepath.Join(conf, "services", "prizm.caddy"); got != want {
		t.Errorf("a uniform service belongs on the services shelf: got %q, want %q", got, want)
	}
	b, _ := os.ReadFile(staged)
	if !strings.Contains(string(b), "handle /api/services/prizm/*") {
		t.Errorf("the uniform route must claim only its own path space:\n%s", string(b))
	}
	if strings.Contains(string(b), "handle /api/* ") {
		t.Errorf("a uniform service must never claim the whole /api/* prefix:\n%s", string(b))
	}
}

// A role outside the closed list is a NAMED failure, never a fallback: a value we do not understand is not
// evidence of a service.
func TestUnknownEdgeRoleIsRefused(t *testing.T) {
	env, art, conf := edgeFixture(t, "svc", "gateway", "", nil)
	res, _ := prepareRoute(t, env, art, "svc", "8800", conf)
	if res.exit == 0 {
		t.Fatalf("an unknown edge role must be refused:\n%s", res.out)
	}
	if !strings.Contains(res.out, "gateway") || !strings.Contains(res.out, "service application dashboard") {
		t.Errorf("the refusal must name the bad value and the closed list:\n%s", res.out)
	}
}

// AN INSTANCE HAS EXACTLY ONE DASHBOARD. The dashboard is the application the landscape's uniform services
// hang under, so a second one would take all of them with it — silently, at the next delivery, and under a
// different name than the day before. The second is refused while the first still stands.
func TestASecondDashboardIsRefused(t *testing.T) {
	env, art, conf := edgeFixture(t, "holistic", "dashboard", "/opt/holistic/www",
		map[string]string{"holistic": "dash.example.test", "other": "other.example.test"})
	// The first dashboard is already installed on the apps shelf.
	mustWrite(t, filepath.Join(conf, "apps", "holistic.caddy"), bashRenderTemplate(t,
		"setup_app_route_text holistic dash.example.test 8080 8770 /opt/holistic/www 1"))

	// A SECOND package declaring the dashboard role is refused — by name, naming the incumbent.
	stampWebRoot(t, art, "/opt/other/www")
	env2 := env
	res, _ := prepareRoute(t, env2, art, "other", "9000", conf)
	if res.exit == 0 {
		t.Fatalf("a second dashboard must be refused:\n%s", res.out)
	}
	for _, want := range []string{"holistic", "already the dashboard", "exactly one dashboard"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the refusal must name the incumbent (%q missing):\n%s", want, res.out)
		}
	}
	// The incumbent is untouched — a refusal never dismantles what already works.
	if b, err := os.ReadFile(filepath.Join(conf, "apps", "holistic.caddy")); err != nil ||
		!strings.Contains(string(b), "import holistic_service_routes") {
		t.Errorf("the refusal must leave the standing dashboard exactly as it was: %v", err)
	}

	// The SAME package as a plain application (not the dashboard) is admitted: what is limited to one is
	// carrying the landscape's services, not owning a hostname.
	if err := os.WriteFile(filepath.Join(art, "edge.role"), []byte("application\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res2, staged := prepareRoute(t, env2, art, "other", "9000", conf)
	if res2.exit != 0 {
		t.Fatalf("a second ROOT APPLICATION (not a second dashboard) must be admitted:\n%s", res2.out)
	}
	b, _ := os.ReadFile(staged)
	if strings.Contains(string(b), "import holistic_service_routes") {
		t.Errorf("only the dashboard carries the landscape's services:\n%s", string(b))
	}
	// …and the DASHBOARD ITSELF may still renew itself: its own file is not a foreign incumbent.
	if err := os.WriteFile(filepath.Join(art, "edge.role"), []byte("dashboard\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stampWebRoot(t, art, "/opt/holistic/www")
	if res3, _ := prepareRoute(t, env2, art, "holistic", "8770", conf); res3.exit != 0 {
		t.Fatalf("the standing dashboard must be able to renew itself:\n%s", res3.out)
	}
}

// A root application without a face has nothing at the root of its name, so its package must say where its
// face is served from — and that declaration is re-judged against the service's own territory, exactly as
// the face installer judges it. A stamp says WHERE; it does not grant where.
func TestRootApplicationNeedsAServeRootInsideItsOwnTerritory(t *testing.T) {
	hosts := map[string]string{"app": "app.example.test"}
	// no serve root at all
	env, art, conf := edgeFixture(t, "app", "application", "", hosts)
	if res, _ := prepareRoute(t, env, art, "app", "9000", conf); res.exit == 0 ||
		!strings.Contains(res.out, "no serve root") {
		t.Errorf("a root application with no declared serve root must be refused by name:\n%s", res.out)
	}
	// a serve root in ANOTHER service's territory
	env2, art2, conf2 := edgeFixture(t, "app", "application", "/opt/holistic/www", hosts)
	if res, _ := prepareRoute(t, env2, art2, "app", "9000", conf2); res.exit == 0 ||
		!strings.Contains(res.out, "outside the territory") {
		t.Errorf("a serve root outside the service's own territory must be refused by name:\n%s", res.out)
	}
}

// A HOSTNAME IS AN INSTANCE VALUE AND NEVER TRAVELS IN A REPOSITORY. The host's declaration is read the
// same way the edge address is: first real line, comments and blanks ignored, shape checked, and a
// non-zero return WITH NOTHING PRINTED when it is absent or unusable — so the caller names the deficiency
// instead of inventing a name.
func TestEdgeHostIsReadFromRuntimeConfigurationOnly(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "holistic"), "# managed by devlab-install-recv\n\n  dash.example.test  \n")
	if got := strings.TrimSpace(bashRenderTemplate(t, "setup_edge_host holistic "+dir)); got != "dash.example.test" {
		t.Errorf("the declared hostname must be read trimmed, comments ignored, got %q", got)
	}
	for _, bad := range []struct{ name, content string }{
		{"absent", ""},
		{"empty", "\n\n# only comments\n"},
		{"bare label", "dashboard\n"},         // a machine name, not a name an environment is reached under
		{"with a scheme", "http://a.test\n"},  // a name, not a URL
		{"with a path", "a.test/dashboard\n"}, // a name, not a location
		{"with a port", "a.test:8080\n"},      // the port comes from the ONE edge address
		{"trailing dot", "a.test.\n"},
	} {
		d := t.TempDir()
		if bad.content != "" {
			mustWrite(t, filepath.Join(d, "x"), bad.content)
		}
		res := sourceLib(t, nil, "setup_edge_host x "+d)
		if res.exit == 0 {
			t.Errorf("a %s declaration must be refused, not guessed past; got %q", bad.name, res.out)
		}
		if strings.TrimSpace(res.out) != "" {
			t.Errorf("a refused declaration must print NOTHING (the caller names the deficiency), got %q", res.out)
		}
	}
}

// THE TWO READERS OF ONE DECLARATION MUST AGREE. The build wrapper reads holistic-service.json in bash
// (it runs without the daemon) and the daemon reads it in Go (it judges without the wrapper). That twin
// arrangement is deliberate — it is the same one the unit's listen port already has — and it is only safe
// while both answer identically. This pins them to each other.
func TestShellAndGoReadTheSameDeclaration(t *testing.T) {
	for _, c := range []struct {
		name, decl string
		role       EdgeRole
		serveRoot  string
	}{
		{"nothing declared", "", EdgeRoleService, ""},
		{"an empty declaration", `{}`, EdgeRoleService, ""},
		{"the dashboard", `{"edge":{"role":"dashboard"}}`, EdgeRoleDashboard, ""},
		{"an application with its own serve root", `{"edge":{"role":"application","serveRoot":"/var/lib/svc/www"}}`,
			EdgeRoleApplication, "/var/lib/svc/www"},
		{"an edge block spread over lines", "{\n  \"port\": 8790,\n  \"edge\": {\n    \"role\": \"application\",\n    \"serveRoot\": \"/opt/svc/face\"\n  }\n}", EdgeRoleApplication, "/opt/svc/face"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			// A conforming service, so Detect() reaches the edge role at all.
			if err := os.WriteFile(filepath.Join(dir, "service"), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			if c.decl != "" {
				mustWrite(t, filepath.Join(dir, DeclarationFileName), c.decl)
			}
			// Go
			det, err := Detect(dir)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if det.Edge != c.role {
				t.Errorf("Go read the edge role as %q, want %q", det.Edge, c.role)
			}
			// bash
			res := sourceLib(t, nil, "setup_decl_edge_role "+dir)
			if res.exit != 0 {
				t.Fatalf("the shell reader failed: %s", res.out)
			}
			if got := strings.TrimSpace(res.out); got != string(c.role) {
				t.Errorf("the shell read the edge role as %q, Go read %q — the two readers must agree", got, c.role)
			}
			// The serve root, where declared, is read verbatim by both; where not, the shell falls back to
			// the landscape convention and Go simply reports nothing declared.
			declaredRoot := ""
			if det.Decl != nil && det.Decl.Edge != nil {
				declaredRoot = det.Decl.Edge.ServeRoot
			}
			if declaredRoot != c.serveRoot {
				t.Errorf("Go read the serve root as %q, want %q", declaredRoot, c.serveRoot)
			}
			shellRoot := strings.TrimSpace(sourceLib(t, nil, "setup_decl_serve_root "+dir+" svc").out)
			want := c.serveRoot
			if want == "" {
				want = "/opt/svc/www"
			}
			if shellRoot != want {
				t.Errorf("the shell read the serve root as %q, want %q", shellRoot, want)
			}
		})
	}

	// An unknown role is refused by BOTH — the shell fails the build, Go reports a nonconforming repo.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "service"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, DeclarationFileName), `{"edge":{"role":"gateway"}}`)
	if res := sourceLib(t, nil, "setup_decl_edge_role "+dir); res.exit == 0 {
		t.Errorf("the shell must refuse an unknown edge role, got: %s", res.out)
	}
	det, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if det.Kind != KindNonconforming || !strings.Contains(det.Evidence, "gateway") {
		t.Errorf("Go must report an unknown edge role as a nonconforming repository, got %s: %s", det.Kind, det.Evidence)
	}
}

// DELIVERED, BUT UNREACHABLE — the gap that would otherwise be the quietest failure in the landscape: the
// program runs, its port answers, every stage of the chain is green, and the application is simply not on
// the internet, because this host names no hostname for it.
func TestDeliveredButUnreachableIsAGap(t *testing.T) {
	dets := map[string]Detection{
		"holistic": {Kind: KindService, ID: "holistic", Edge: EdgeRoleDashboard, Evidence: "./service CLI"},
		"devlab":   {Kind: KindService, ID: "devlab", Edge: EdgeRoleApplication, Evidence: "./service CLI"},
		"prizm":    {Kind: KindService, ID: "prizm", Edge: EdgeRoleService, Evidence: "./service CLI"},
	}
	allocs := []model.PortAllocation{
		{Port: 8770, Service: "holistic", Routed: true, Bound: true},
		{Port: 8781, Service: "devlab", Routed: true, Bound: true},
		{Port: 18811, Service: "prizm", Routed: true, Bound: true},
	}
	// This host names only the dashboard.
	named := map[string]bool{"holistic": true}
	gaps := FindGaps(dets, allocs, func(id string) bool { return named[id] })

	byRepo := map[string]Gap{}
	for _, g := range gaps {
		byRepo[g.Repo] = g
	}
	if _, ok := byRepo["holistic"]; ok {
		t.Errorf("a root application this host DOES name is no gap: %+v", byRepo["holistic"])
	}
	if _, ok := byRepo["prizm"]; ok {
		t.Errorf("a uniform service is reached under the dashboard's name and needs none of its own: %+v", byRepo["prizm"])
	}
	g, ok := byRepo["devlab"]
	if !ok {
		t.Fatalf("a root application with no hostname on this host must be a gap: %+v", gaps)
	}
	for _, want := range []string{"delivered, but unreachable", "root application", "--edge-host devlab="} {
		if !strings.Contains(g.Detail, want) {
			t.Errorf("the gap must say what is the case and how to end it (%q missing): %q", want, g.Detail)
		}
	}
	if g.Kind != KindService {
		t.Errorf("the gap keeps the SERVICE classification (it is a service, only unreachable): %+v", g)
	}
}

// EdgeHostDeclared answers the same question the shell reader answers, off the same directory, so the gap
// above is measured against the host rather than assumed.
func TestEdgeHostDeclaredReadsTheHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_EDGE_HOSTS_DIR", dir)
	if EdgeHostDeclared("holistic") {
		t.Error("a host that declares nothing must not be read as declaring something")
	}
	mustWrite(t, filepath.Join(dir, "holistic"), "# a comment\n\ndash.example.test\n")
	if !EdgeHostDeclared("holistic") {
		t.Error("a declared hostname must be seen")
	}
	mustWrite(t, filepath.Join(dir, "empty"), "# only comments\n\n")
	if EdgeHostDeclared("empty") {
		t.Error("a file with no declaration in it declares nothing")
	}
	if EdgeHostDeclared("") {
		t.Error("no id is no declaration")
	}
}

// THE CONTRADICTION IS THE FINDING. A host declares a hostname for an application; the package under that
// id declares itself a uniform service. Both declarations cannot be true, and the QUIET outcome is the bad
// one: the package would be routed under the dashboard's name, its face installed and served nowhere, and
// the hostname the operator declared would answer the edge's refusal. Nobody sees a failure; they see a
// 404 and look for it in the wrong place.
//
// This is the exact shape of the one ordering risk this whole change carries. The landscape dashboard's
// repository must declare edge.role=dashboard; until it does, an operator who has already declared its
// hostname would otherwise get a silently unreachable dashboard AND, because the uniform services hang
// under it, a silently unreachable landscape. Named, it stops instead — with both halves in the message.
func TestAHostnameForAPackageThatClaimsNoRoleIsRefused(t *testing.T) {
	env, art, conf := edgeFixture(t, "holistic", "service", "",
		map[string]string{"holistic": "dash.example.test"})
	res, _ := prepareRoute(t, env, art, "holistic", "8770", conf)
	if res.exit == 0 {
		t.Fatalf("a hostname for a package that claims no root-application role must be refused:\n%s", res.out)
	}
	for _, want := range []string{"contradictory", "dash.example.test", "edge.role=application|dashboard"} {
		if !strings.Contains(res.out, want) {
			t.Errorf("the refusal must show BOTH halves and how to resolve them (%q missing):\n%s", want, res.out)
		}
	}
	for _, shelf := range []string{"apps", "services"} {
		if entries, _ := os.ReadDir(filepath.Join(conf, shelf)); len(entries) != 0 {
			t.Errorf("a refused route must leave the %s shelf empty", shelf)
		}
	}

	// A uniform service the host names NOTHING for is untouched by this — the ordinary case stays ordinary.
	env2, art2, conf2 := edgeFixture(t, "prizm", "service", "", map[string]string{"holistic": "dash.example.test"})
	if res2, _ := prepareRoute(t, env2, art2, "prizm", "18811", conf2); res2.exit != 0 {
		t.Errorf("a uniform service the host names nothing for must route as usual:\n%s", res2.out)
	}
}
