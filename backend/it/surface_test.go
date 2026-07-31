package it

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// REQ-040.3 — the reconciliation list, BOTH directions:
//
//	forward   every route of the ONE route table has at least one caller: the data seam
//	          (src/data/httpSource.ts) or the MCP tool table (backend/internal/api/handlers_mcp.go).
//	          A route without a caller is an access nobody can reach — or a button into the void.
//	backward  every path the callers name exists in the route table. A caller without a route is
//	          a button that 404s.
//
// Both sides are read out of the sources, so the list can never drift out of date.
func TestEveryRouteHasACallerAndEveryCallerARoute(t *testing.T) {
	routes := parseRouteTable(t)
	if len(routes) < 60 {
		t.Fatalf("only %d routes parsed — the route-table parser lost its grip", len(routes))
	}
	callers := callerPaths(t)
	if len(callers) < 40 {
		t.Fatalf("only %d caller paths found — the caller parser lost its grip", len(callers))
	}

	// Routes reached WITHOUT a typed caller, by design — each with the reason it is exempt.
	noTypedCaller := map[string]string{
		"/api/github/callback": "the OAuth redirect target — GitHub navigates the browser here",
		"/api/health":          "the liveness probe of the platform, not of the UI",
		"/api/mcp":             "the protocol endpoint itself: its callers are agents, and its tools are reconciled against the data seam by src/parity.test.ts",
	}

	// ── forward ───────────────────────────────────────────────────────────────────────────────
	orphanRoutes := []string{}
	for _, rt := range routes {
		if why, ok := noTypedCaller[rt.pattern]; ok {
			t.Logf("%s %s: no typed caller by design — %s", rt.method, rt.pattern, why)
			continue
		}
		if !anyCallerMatches(callers, rt.pattern) {
			orphanRoutes = append(orphanRoutes, rt.method+" "+rt.pattern)
		}
	}
	if len(orphanRoutes) > 0 {
		sort.Strings(orphanRoutes)
		t.Errorf("routes without any caller (%d) — an access nobody can reach:\n  %s",
			len(orphanRoutes), strings.Join(orphanRoutes, "\n  "))
	}

	// ── backward ──────────────────────────────────────────────────────────────────────────────
	orphanCallers := []string{}
	for _, c := range callers {
		if !anyRouteMatches(routes, c) {
			orphanCallers = append(orphanCallers, c)
		}
	}
	if len(orphanCallers) > 0 {
		sort.Strings(orphanCallers)
		t.Errorf("callers without any route (%d) — a button that 404s:\n  %s",
			len(orphanCallers), strings.Join(orphanCallers, "\n  "))
	}
}

// REQ-040.3, sharpened: the reconciliation above is at PATH level, so a write route whose path is
// only READ by the seam would slip through. Every mutating route must have a MUTATING caller —
// otherwise the write exists but nothing can perform it.
func TestEveryMutatingRouteHasAMutatingCaller(t *testing.T) {
	requireMCPToolTable(t)
	writers := mutatingCallerPaths(t)
	missing := []string{}
	for _, rt := range parseRouteTable(t) {
		if rt.method == "GET" || rt.pattern == "/api/auth/refresh" {
			continue
		}
		found := false
		for _, w := range writers {
			if segmentsMatch(rt.pattern, w) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, rt.method+" "+rt.pattern)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("mutating routes nothing can perform (%d):\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

// mutatingCallerPaths collects the paths a caller MUTATES: in the data seam a mutating call names
// the CSRF-bearing helper on the same line; every path in the MCP tool table counts, because a
// tool calls its handler directly.
func mutatingCallerPaths(t *testing.T) []string {
	t.Helper()
	out := []string{}
	seam, err := os.ReadFile(filepath.Join("..", "..", "src", "data", "httpSource.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(seam), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if !strings.Contains(line, "post(") && !strings.Contains(line, "withCsrf(") {
			continue
		}
		for _, m := range callerPathRe.FindAllString(line, -1) {
			if p := normalizeCallerPath(m); p != "" {
				out = append(out, p)
			}
		}
	}
	if tools, err := os.ReadFile(filepath.Join("..", "internal", "api", "mcp_tools.go")); err == nil {
		for _, m := range callerPathRe.FindAllString(string(tools), -1) {
			if p := normalizeCallerPath(m); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// requireMCPToolTable refuses to evaluate a criterion that rests on the MCP tool table while that
// table is still a stub: an unproven line must never read as a passed one.
func requireMCPToolTable(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "internal", "api", "mcp_tools.go"))
	if err != nil {
		t.Skipf("the MCP tool table does not exist yet — this criterion is UNPROVEN, not passed: %v", err)
	}
	if !strings.Contains(string(b), "func mcpToolRows()") {
		t.Skip("the MCP tool table is still a stub — this criterion is UNPROVEN, not passed")
	}
}

// callerPaths collects every /api path the callers name: the data seam (httpSource.ts, the ONE
// seam between UI and data) and the MCP tool table (the second caller of the same surface).
func callerPaths(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, rel := range []string{
		filepath.Join("..", "..", "src", "data", "httpSource.ts"),
		filepath.Join("..", "internal", "api", "mcp_tools.go"),
	} {
		b, err := os.ReadFile(rel)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue // a mention in prose is not a caller
			}
			for _, m := range callerPathRe.FindAllString(line, -1) {
				if p := normalizeCallerPath(m); p != "" {
					seen[p] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var (
	// Any /api/... path, wherever it appears in an expression: a template literal may start with
	// an interpolation (the WebSocket URL does), so the opening quote is not a reliable anchor.
	callerPathRe = regexp.MustCompile(`/api/[A-Za-z0-9_/{}$().?=&:${}-]*`)
	// A complete template placeholder, and one whose closing brace lies beyond our match (a
	// nested template literal, e.g. `…${q ? `?${q}` : ''}`).
	templateSegRe   = regexp.MustCompile(`\$\{[^{}]*\}`)
	danglingSegRe   = regexp.MustCompile(`\$\{.*$`)
	namedParamRe    = regexp.MustCompile(`^\{[A-Za-z]+\}$`)
	repeatedParamRe = regexp.MustCompile(`(\{\})+`)
	// A literal segment with an interpolation glued onto its end is a query-string appendix: path
	// ids are always whole segments here.
	gluedParamRe = regexp.MustCompile(`^([^{}]+)\{\}$`)
)

// normalizeCallerPath turns a caller's path into the route-table shape: interpolations become one
// placeholder segment, a query string is dropped.
func normalizeCallerPath(p string) string {
	p = templateSegRe.ReplaceAllString(p, "{}")
	p = danglingSegRe.ReplaceAllString(p, "{}")
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	segs := strings.Split(strings.TrimRight(p, "/"), "/")
	for i, s := range segs {
		// The Go tool table writes its path parameters the way the route table does ({id}); the
		// data seam interpolates them. Both are the same thing: one placeholder segment.
		s = namedParamRe.ReplaceAllString(s, "{}")
		s = repeatedParamRe.ReplaceAllString(s, "{}")
		if m := gluedParamRe.FindStringSubmatch(s); m != nil {
			s = m[1]
		}
		segs[i] = s
	}
	out := strings.Join(segs, "/")
	if out == "/api" || out == "/api/" {
		return ""
	}
	return out
}

// segmentsMatch compares a route pattern to a caller path segment by segment, where a route
// placeholder ({id}) matches a caller placeholder ({}) or a literal segment.
func segmentsMatch(pattern, caller string) bool {
	ps := strings.Split(strings.Trim(pattern, "/"), "/")
	cs := strings.Split(strings.Trim(caller, "/"), "/")
	if len(ps) != len(cs) {
		return false
	}
	for i := range ps {
		pv, cv := ps[i], cs[i]
		isParam := strings.HasPrefix(pv, "{") && strings.HasSuffix(pv, "}")
		switch {
		case isParam:
			// A route parameter must be filled by a placeholder — a literal there would mean the
			// caller hard-codes an id.
			if cv != "{}" {
				return false
			}
		case cv == "{}":
			// The caller interpolates where the route has a fixed segment: only acceptable if the
			// literal is the interpolation's only possible value, which we cannot know — reject,
			// so such a caller is reported rather than silently matched.
			return false
		default:
			if pv != cv {
				return false
			}
		}
	}
	return true
}

func anyCallerMatches(callers []string, pattern string) bool {
	for _, c := range callers {
		if segmentsMatch(pattern, c) {
			return true
		}
	}
	return false
}

func anyRouteMatches(routes []route, caller string) bool {
	for _, rt := range routes {
		if segmentsMatch(rt.pattern, caller) {
			return true
		}
	}
	return false
}
