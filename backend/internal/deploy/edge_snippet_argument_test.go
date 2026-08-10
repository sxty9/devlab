package deploy

// THE EDGE MUST BEHAVE THE SAME ON EVERY CADDY THE LANDSCAPE RUNS — and it did not.
//
// Measured on a production host on 2026-08-10: every hostname answered 503 "this application is
// installed on this host, but its interface is not: nothing is served from {args[0]}", with the
// placeholder printed VERBATIM in the message. That literal placeholder was the whole proof — it had
// never been replaced. The host ran Caddy 2.6.2, and the bracket spelling `{args[0]}` for snippet
// arguments exists only from Caddy 2.7 on.
//
// What makes this a class of fault rather than a typo: an older Caddy does not REFUSE a placeholder it
// does not know. It passes it through verbatim into the generated configuration, and everything
// downstream still validates, still loads and still reports success. So the file matcher went looking for
// a directory literally named `{args[0]}`, never matched, and every interface in the landscape fell into
// the snippet's 503 branch — on every host whose Caddy predates 2.7, with nothing anywhere reporting an
// error. A second face of the same fault is worse than the 503: a root that cannot be resolved to an
// absolute path is resolved against the caddy process's WORKING DIRECTORY, so a host whose working
// directory held an index.html served that unrelated page under a Holistic hostname.
//
// THE FIX IS STRUCTURAL, not a better spelling: the shared `app_web` snippet takes NO argument at all.
// The application's own site block states `root * <path>`, the file matcher takes that site root as its
// default, and the 503 message names it back through `{http.vars.root}` — a plain directive and a runtime
// placeholder that every Caddy since 2.0 reads identically. `{args.0}`, the spelling Caddy 2.6.2 DOES
// understand, was measured and works on both versions, and was still rejected: Caddy 2.11.4 logs
// "Placeholder {args.0} deprecated, use {args[0]} instead" once per occurrence on every load, so it would
// leave the running software instructing the next maintainer to restore precisely the spelling that
// breaks the older host.
//
// These tests measure that in the two ways that matter:
//   a) STRUCTURAL, and running on every machine: nothing the templates GENERATE carries a snippet
//      argument in any spelling, and the site block states its serve root itself.
//   b) BEHAVIOURAL, against every caddy the machine offers: face present → 200 with deep links, face
//      genuinely absent → 503 naming the path, unclaimed hostname → 404 — identical on each version.

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// snippetArgumentSpellings is EVERY spelling of a Caddy snippet argument. Both are refused in generated
// configuration, and the pair is named in one place so neither can be forgotten: `{args[0]}` is the fault
// that was measured, `{args.0}` is the deprecated repair that would reintroduce it the moment a future
// Caddy drops it. What is admissible is no snippet argument at all.
var snippetArgumentSpellings = []string{"{args[", "{args."}

// renderedEdgeParts is everything the templates EMIT onto a host: the edge shell and a root application's
// site block. The test measures what reaches a host's Caddyfile, never the library's source — the source
// names the forbidden spellings on purpose, in the comment that exists to stop them from coming back.
func renderedEdgeParts(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"the edge shell (setup_edge_caddyfile_text)": bashRenderTemplate(t,
			"setup_edge_caddyfile_text /etc/caddy/conf.d 10.10.0.1:8080"),
		"a root application's site block (setup_app_route_text)": bashRenderTemplate(t,
			"setup_app_route_text holistic dash.example.test 8080 8781 /opt/holistic/www 1"),
	}
}

// TEST a1: no generated edge configuration carries a snippet argument, in either spelling. This is the
// check that binds on every machine, with or without a caddy, and on every caddy version — the
// behavioural test below can only measure the versions the machine happens to carry.
func TestGeneratedEdgeCarriesNoSnippetArgument(t *testing.T) {
	for what, text := range renderedEdgeParts(t) {
		for _, spelling := range snippetArgumentSpellings {
			if strings.Contains(text, spelling) {
				t.Errorf("%s must carry no snippet argument, found %q — an older caddy passes an unknown "+
					"placeholder through verbatim instead of refusing it, so the fault is silent and every "+
					"interface on that host answers 503:\n%s", what, spelling, text)
			}
		}
	}
}

// TEST a2: what stands INSTEAD — the site block states its own serve root as a plain directive, and
// imports the shared snippet bare. Both halves are asserted together because either alone is satisfiable
// by a broken configuration: a `root` line with the argument still on the import, or a bare import with
// no root anywhere (which is precisely the "resolved against the working directory" fault).
func TestSiteBlockStatesItsServeRootItself(t *testing.T) {
	block := bashRenderTemplate(t, "setup_app_route_text holistic dash.example.test 8080 8781 /opt/holistic/www 1")
	if !strings.Contains(block, "root * /opt/holistic/www") {
		t.Errorf("the site block must state its serve root as a plain directive every caddy reads alike:\n%s", block)
	}
	if !strings.Contains(block, "import app_web\n") {
		t.Errorf("the face snippet must be imported without an argument:\n%s", block)
	}
	// The serve root must be ABSOLUTE where it is stated — a relative root is resolved against the caddy
	// process's working directory, the second face of the fault this file guards.
	for _, line := range strings.Split(block, "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[0] == "root" && f[1] == "*" && !strings.HasPrefix(f[2], "/") {
			t.Errorf("the serve root must be absolute, got %q — a relative root is answered out of caddy's "+
				"working directory", f[2])
		}
	}
	// And the snippet reads that root back rather than being told it again.
	shell := bashRenderTemplate(t, "setup_edge_caddyfile_text /etc/caddy/conf.d 10.10.0.1:8080")
	if !strings.Contains(shell, "{http.vars.root}") {
		t.Errorf("the missing-interface answer must name the site's own root:\n%s", shell)
	}
}

// caddyBinaries is every caddy this machine offers to measure against: the one on PATH, plus any named in
// DEVLAB_TEST_CADDIES (colon-separated paths). The seam exists because the fault guarded here is a
// VERSION fault — a configuration that works on the newest caddy and breaks on an older one — so a
// machine that carries an older caddy can measure it, and one that carries a single caddy still measures
// that one. Each binary is reported with its version, so the test states the coverage it actually had
// instead of implying more.
func caddyBinaries(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	candidates := []string{"caddy"}
	if extra := os.Getenv("DEVLAB_TEST_CADDIES"); extra != "" {
		candidates = append(candidates, strings.Split(extra, ":")...)
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		bin, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		out, err := exec.Command(bin, "version").Output()
		if err != nil {
			continue
		}
		version := strings.Fields(strings.TrimSpace(string(out)))[0]
		found[version] = bin // one measurement per VERSION; the same caddy named twice is one caddy
	}
	return found
}

// TEST b: the DECISIVE measurement — the real templates, through the real caddy, on every version this
// machine can offer, telling the three states apart identically on each. This is the test that would have
// caught the outage: on Caddy 2.6.2 the old template answered 503 for a face that was demonstrably there.
func TestTheEdgeBehavesTheSameOnEveryCaddyVersion(t *testing.T) {
	versions := caddyBinaries(t)
	if len(versions) == 0 {
		t.Skip("no caddy on this machine — the edge cannot be measured here")
	}
	for version, bin := range versions {
		version, bin := version, bin
		t.Run(version, func(t *testing.T) {
			t.Logf("measuring the generated edge against %s (%s)", version, bin)
			host := t.TempDir() // this fixture host's /opt
			conf := filepath.Join(host, "conf.d")
			if err := os.MkdirAll(filepath.Join(conf, "apps"), 0o755); err != nil {
				t.Fatal(err)
			}
			serveRoot := filepath.Join(host, SETUP_ROOT_APP_ID, "www")
			port := freePort(t)
			deliverDashboardBlock(t, conf, serveRoot, port, freePort(t))
			get := serveEdgeWith(t, bin, renderEdge(t, host, conf, fmt.Sprintf("127.0.0.1:%d", port)), port)

			// (b) the face is REALLY absent: the honest 503, naming the path it looked in. The snippet's
			// intent must survive the fix — this is the half a careless repair would trade away.
			code, body := get(instanceRootHost, "/")
			if code != http.StatusServiceUnavailable {
				t.Fatalf("%s: without the face its hostname must answer 503, got %d: %s", version, code, body)
			}
			if !strings.Contains(body, serveRoot) {
				t.Errorf("%s: the answer must name the directory it found nothing in (%s):\n%s", version, serveRoot, body)
			}
			for _, spelling := range snippetArgumentSpellings {
				if strings.Contains(body, spelling) {
					t.Errorf("%s: the answer still carries the unreplaced placeholder %q — this is the "+
						"production symptom itself:\n%s", version, spelling, body)
				}
			}

			// (a) the face arrives — installed by the shared installer, from a package that declares where
			// it belongs, so what is measured is the whole path and not a hand-placed file.
			art := facePackage(t, serveRoot, true)
			if res := sourceLib(t, map[string]string{"DEVLAB_SERVICE_ROOT": host},
				"setup_install_web "+art+" "+SETUP_ROOT_APP_ID); res.exit != 0 {
				t.Fatalf("installing the face must succeed:\n%s", res.out)
			}
			code, body = get(instanceRootHost, "/")
			if code != http.StatusOK || !strings.Contains(body, "dashboard") {
				t.Fatalf("%s: with the face installed its hostname must serve it, got %d: %s", version, code, body)
			}
			// A deep link is the single-page application's start page, not a 404.
			if code, body := get(instanceRootHost, "/mercury/todo"); code != http.StatusOK || !strings.Contains(body, "dashboard") {
				t.Errorf("%s: a deep link must reach the start page, got %d: %s", version, code, body)
			}
			// And a name nobody claimed stays its own, differently-worded state.
			if code, body := get("nobody.example.test", "/"); code != http.StatusNotFound ||
				!strings.Contains(body, "no root application answering to the name") {
				t.Errorf("%s: an unclaimed hostname must be refused in its own words, got %d: %s", version, code, body)
			}
		})
	}
}
