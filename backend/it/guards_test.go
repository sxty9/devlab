package it

import (
	"net/http"
	"os"
	"os/user"
	"regexp"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/sched"
)

// B-03 / REQ-040.3: the guard matrix over the WHOLE route table. Every route is checked in the
// states a request can be in — no session, a valid session WITHOUT the DevLab right, and (for
// mutating routes) a session without the CSRF double submit. The table is read out of api.go, so
// a route added later is covered automatically.
func TestGuardMatrixCoversEveryRoute(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: time.Hour})
	stranger := e.unprivilegedAccount()
	routes := parseRouteTable(t)
	if len(routes) < 60 {
		t.Fatalf("only %d routes parsed out of the route table — the parser lost its grip", len(routes))
	}

	// Public by design: health is the liveness probe, refresh IS the re-authentication, and the
	// OAuth entry/callback are entered by the browser mid-flow.
	public := map[string]bool{
		"GET /api/health":           true,
		"POST /api/auth/refresh":    true,
		"GET /api/github/authorize": true,
		"GET /api/github/callback":  true,
	}
	// The session tier (a valid Holistic session, no DevLab right needed): the SPA's identity
	// probe, which is what tells "signed in but no access" from "not signed in".
	sessionTier := map[string]bool{"GET /api/user": true}

	for _, rt := range routes {
		key := rt.method + " " + rt.pattern
		path := rt.concrete()

		t.Run(key, func(t *testing.T) {
			// 1. no session at all ⇒ 401, never a leak.
			if !public[key] {
				req, err := http.NewRequest(rt.method, e.ts.URL+path, strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				if code, body := e.do(req); code != http.StatusUnauthorized {
					t.Errorf("without a session: %d %s — want 401", code, trim(body))
				}
			}

			// 2. a valid session WITHOUT the DevLab right ⇒ 403 that NAMES the right.
			if !public[key] && !sessionTier[key] && stranger != "" {
				req := e.requestAs(stranger, rt.method, path, map[string]any{})
				code, body := e.do(req)
				if code != http.StatusForbidden {
					t.Errorf("with a session but without the right: %d %s — want 403", code, trim(body))
				} else if !strings.Contains(string(body), "hp_devlab_access") {
					t.Errorf("the refusal does not name the missing right: %s", trim(body))
				}
			}

			// 3. a route that DECLARES the CSRF double submit must enforce it. (Whether a
			// mutating route declares the right guard at all is a static question — tools/
			// abnahme.sh audits the table for a mutating route bound to the non-CSRF guard.)
			if rt.guard == "guardWrite" || rt.guard == "guardCSRF" {
				req := e.request(rt.method, path, map[string]any{})
				req.Header.Del("X-CSRF-Token")
				code, body := e.do(req)
				if code != http.StatusForbidden || !strings.Contains(string(body), "CSRF") {
					t.Errorf("without the CSRF header: %d %s — want 403 naming CSRF", code, trim(body))
				}
			}

			// 4. every route in the table is guarded at all — an unguarded entry is a hole.
			if !public[key] && rt.guard == "none" {
				t.Errorf("the route carries no guard (handler %s)", rt.handler)
			}
		})
	}
}

// unprivilegedAccount returns a real local account that holds NEITHER the admin group nor the
// DevLab right — the identity the 403 dimension needs. "" when the machine offers none (the
// dimension is then skipped rather than faked).
func (e *env) unprivilegedAccount() string {
	e.t.Helper()
	for _, name := range []string{"nobody", "daemon", "bin", "sys", "games"} {
		u, err := user.Lookup(name)
		if err != nil {
			continue
		}
		gids, err := u.GroupIds()
		if err != nil {
			continue
		}
		privileged := false
		for _, gid := range gids {
			g, err := user.LookupGroupId(gid)
			if err != nil {
				continue
			}
			if g.Name == e.adminGr || g.Name == "hp_devlab_access" {
				privileged = true
			}
		}
		if !privileged {
			return name
		}
	}
	e.t.Log("no unprivileged local account available — the 403 dimension of the matrix is not exercised here")
	return ""
}

// B-03 / REQ-040.3: the write tier additionally requires a linked GitHub account and says so; the
// read tier and the run-domain writes must NOT require it (server-side pushes use the runner's own
// account, so demanding the caller's link would be a false barrier).
func TestWriteTierRequiresALinkedAccountAndOthersDoNot(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: time.Hour})

	code, body := e.post("/api/repos/alpha/commit", map[string]any{"message": "x"})
	if code != http.StatusForbidden || !strings.Contains(string(body), "GitHub") {
		t.Errorf("a repo write without a linked account: %d %s — want 403 naming the link", code, trim(body))
	}

	code, body = e.post("/api/mercury/runs/notices/clear", map[string]any{})
	if code == http.StatusForbidden && strings.Contains(string(body), "GitHub") {
		t.Errorf("a run-domain write demanded a GitHub link it does not need: %s", trim(body))
	}

	if code, body := e.do(e.request(http.MethodGet, "/api/mercury/runs/notices", nil)); code != http.StatusOK {
		t.Errorf("reading the notice pool without a linked account: %d %s — want 200", code, trim(body))
	}
}

// A1-5: the public health endpoint carries no operational internals, and the security headers are
// on every answer.
func TestPublicSurfaceLeaksNothing(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: time.Hour})
	req, err := http.NewRequest(http.MethodGet, e.ts.URL+"/api/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the security headers are missing")
	}
	var health map[string]any
	e.getJSON("/api/health", &health)
	for _, forbidden := range []string{"executions", "slots", "capacity", "restart", "state", "uptime"} {
		if _, leaked := health[forbidden]; leaked {
			t.Errorf("the public health endpoint exposes %q", forbidden)
		}
	}
	if len(health) > 2 {
		t.Errorf("the health payload carries %d fields — it is meant to be minimal: %+v", len(health), health)
	}
}

// ── the route table parser (shared with surface_test.go) ─────────────────────────────────────

// route is one entry of the api.go route table.
type route struct {
	method  string
	pattern string
	guard   string // guardAuthed | guard | guardWrite | guardCSRF | none
	handler string
}

// concrete substitutes a value for every {placeholder} so the route can actually be called.
func (r route) concrete() string { return placeholderRe.ReplaceAllString(r.pattern, "probe") }

var (
	placeholderRe = regexp.MustCompile(`\{[a-zA-Z]+\}`)
	routeRe       = regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)",\s*(?:s\.(guardAuthed|guardWrite|guardCSRF|guard)\()?s\.([A-Za-z0-9_]+)\)?\)`)
)

// parseRouteTable reads THE route table out of api.go — the one place it exists.
func parseRouteTable(t *testing.T) []route {
	t.Helper()
	src, err := os.ReadFile("../internal/api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	out := []route{}
	for _, m := range routeRe.FindAllStringSubmatch(string(src), -1) {
		if m[2] == "/" {
			continue // the SPA fallback is not an API route
		}
		guard := m[3]
		if guard == "" {
			guard = "none"
		}
		out = append(out, route{method: m[1], pattern: m[2], guard: guard, handler: m[4]})
	}
	return out
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// D 34 / B-03: the CSRF double submit is waived ONLY on the bearer path — and the waiver cannot be
// borrowed. Both decisions (does CSRF apply, and whose identity is this) read the SAME predicate, so
// a page that sends a fake Authorization header alongside the victim's cookies waives the check and
// loses the session in the same move: the request is refused, never executed.
func TestCSRFWaiverBelongsToTheBearerPathAlone(t *testing.T) {
	e := newEnv(t, sched.Config{Tick: time.Hour})
	e.addTodo("run_csrf", "a mutating target", "alpha")
	const path = "/api/mercury/runs/run_csrf/cancel"

	// (a) the cookie path without the double submit is refused, naming CSRF.
	req := e.request(http.MethodPost, path, map[string]any{})
	req.Header.Del("X-CSRF-Token")
	if code, body := e.do(req); code != http.StatusForbidden || !strings.Contains(string(body), "CSRF") {
		t.Errorf("cookie path without the header: %d %s — want 403 naming CSRF", code, trim(body))
	}

	// (b) a token-SHAPED but invalid bearer header waives the CSRF check — and then fails identity:
	// 401, never a performed mutation.
	req = e.request(http.MethodPost, path, map[string]any{})
	req.Header.Del("X-CSRF-Token")
	req.Header.Set("Authorization", "Bearer not.a.real.token")
	if code, body := e.do(req); code != http.StatusUnauthorized {
		t.Errorf("a forged bearer beside the cookies: %d %s — want 401 (the waiver cannot be borrowed)", code, trim(body))
	}

	// (c) a VALID bearer needs no double submit at all — the header cannot be sent cross-site.
	req = e.request(http.MethodPost, path, map[string]any{})
	req.Header.Del("X-CSRF-Token")
	req.Header.Set("Authorization", "Bearer "+e.token(e.user, 15*time.Minute))
	code, body := e.do(req)
	if code == http.StatusForbidden && strings.Contains(string(body), "CSRF") {
		t.Errorf("a valid bearer was asked for a CSRF token: %d %s", code, trim(body))
	}
	if code == http.StatusUnauthorized {
		t.Errorf("a valid bearer was not accepted as a session: %d %s", code, trim(body))
	}
}
