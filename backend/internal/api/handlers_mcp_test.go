package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/mcp"
)

const rightGroup = "hp_devlab_access"

func withRight() auth.User    { return auth.User{Username: "ada", Groups: []string{rightGroup}} }
func withoutRight() auth.User { return auth.User{Username: "bob"} }

// mcpCtx is the per-call context the endpoint builds: CSRF verdict plus forwarded credentials.
func mcpCtx(csrfOK bool) context.Context {
	return context.WithValue(context.Background(), mcpCallKey, mcpCallInfo{csrfOK: csrfOK})
}

// ── parity (REQ-043) ──────────────────────────────────────────────────────────────────────

var dataSourceOpRe = regexp.MustCompile(`(?m)^  ([a-zA-Z][A-Za-z0-9_]*)\(`)

// dataSourceOps reads the operation list from the frozen DataSource contract — the yardstick
// for "everything the UI can do".
func dataSourceOps(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "src", "data", "source.ts"))
	if err != nil {
		t.Fatalf("the DataSource contract must be readable: %v", err)
	}
	body := string(raw)
	start := strings.Index(body, "export interface DataSource {")
	if start < 0 {
		t.Fatal("DataSource interface not found in src/data/source.ts")
	}
	body = body[start:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	var ops []string
	for _, m := range dataSourceOpRe.FindAllStringSubmatch(body, -1) {
		ops = append(ops, m[1])
	}
	if len(ops) < 50 {
		t.Fatalf("suspiciously few DataSource operations parsed (%d)", len(ops))
	}
	return ops
}

// Every capability of the UI surface is reachable over MCP: one tool per DataSource operation,
// or an operation named in the exception list WITH its reason. And in the other direction: a tool
// either mirrors a real operation or says why it has none.
func TestToolTableMirrorsTheDataSource(t *testing.T) {
	ops := dataSourceOps(t)
	known := map[string]bool{}
	for _, op := range ops {
		known[op] = true
	}
	covered := map[string]string{} // op → tool
	for _, tool := range MCPToolTable() {
		if tool.DataSourceOp == "" {
			if tool.Note == "" {
				t.Errorf("tool %s mirrors no DataSource operation and gives no reason", tool.Name)
			}
			continue
		}
		if !known[tool.DataSourceOp] {
			t.Errorf("tool %s claims DataSource operation %q, which does not exist", tool.Name, tool.DataSourceOp)
		}
		if other, dup := covered[tool.DataSourceOp]; dup {
			t.Errorf("operation %s is claimed twice: %s and %s", tool.DataSourceOp, other, tool.Name)
		}
		covered[tool.DataSourceOp] = tool.Name
	}
	omitted := map[string]string{}
	for _, ex := range MCPParityList().OmittedDataSourceOps {
		if ex.Reason == "" {
			t.Errorf("omitted operation %s carries no reason", ex.Subject)
		}
		if !known[ex.Subject] {
			t.Errorf("omitted operation %s does not exist in the DataSource contract", ex.Subject)
		}
		omitted[ex.Subject] = ex.Reason
	}
	for _, op := range ops {
		if covered[op] == "" && omitted[op] == "" {
			t.Errorf("DataSource operation %s has neither a tool nor a stated reason (REQ-043 parity)", op)
		}
		if covered[op] != "" && omitted[op] != "" {
			t.Errorf("operation %s is both tooled (%s) and declared omitted", op, covered[op])
		}
	}
}

var routeRe = regexp.MustCompile(`mux\.HandleFunc\("([^"]+)",\s*s\.(guardAuthed|guardWrite|guardCSRF|guard)?\(?`)

type routeEntry struct {
	route string // "METHOD /path" as written in the route table
	guard string // "" ⇒ unguarded
}

// routeTable reads the frozen route table out of api.go — the surface every access point of the
// service is declared in.
func routeTable(t *testing.T) []routeEntry {
	t.Helper()
	raw, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []routeEntry
	for _, m := range routeRe.FindAllStringSubmatch(string(raw), -1) {
		out = append(out, routeEntry{route: m[1], guard: m[2]})
	}
	if len(out) < 50 {
		t.Fatalf("suspiciously few routes parsed (%d)", len(out))
	}
	return out
}

// Every route of the frozen table has a named caller on the MCP side or a stated reason why it
// has none (REQ-040.3), and no tool points at a route that does not exist.
func TestEveryRouteHasAToolOrAStatedReason(t *testing.T) {
	routes := routeTable(t)
	declared := map[string]bool{}
	for _, r := range routes {
		declared[r.route] = true
	}
	tooled := map[string]string{}
	for _, tool := range MCPToolTable() {
		if !declared[tool.Route] {
			t.Errorf("tool %s rides route %q, which is not in the route table", tool.Name, tool.Route)
		}
		if other, dup := tooled[tool.Route]; dup {
			t.Errorf("route %s is claimed twice: %s and %s", tool.Route, other, tool.Name)
		}
		tooled[tool.Route] = tool.Name
	}
	untooled := map[string]string{}
	for _, ex := range MCPParityList().UntooledRoutes {
		if ex.Reason == "" {
			t.Errorf("untooled route %s carries no reason", ex.Subject)
		}
		if !declared[ex.Subject] {
			t.Errorf("untooled route %q is not in the route table", ex.Subject)
		}
		untooled[ex.Subject] = ex.Reason
	}
	for _, r := range routes {
		if tooled[r.route] == "" && untooled[r.route] == "" {
			t.Errorf("route %s has no MCP caller and no stated reason", r.route)
		}
	}
}

// A tool is never laxer than its own route: it always demands the DevLab right, and it inherits
// the CSRF and GitHub-link demands of the guard the route carries.
func TestToolTierMatchesTheRouteGuard(t *testing.T) {
	guards := map[string]string{}
	for _, r := range routeTable(t) {
		guards[r.route] = r.guard
	}
	want := map[string]mcpTier{"guard": tierRead, "guardCSRF": tierCSRF, "guardWrite": tierWrite}
	for _, row := range mcpToolRows() {
		route := row.Method + " " + row.Path
		guard, ok := guards[route]
		if !ok {
			continue // reported by the route test
		}
		if guard == "guardAuthed" {
			// The MCP surface is a DevLab surface: even a session-only route demands the right here.
			if row.Tier != tierRead {
				t.Errorf("%s: a session-only route needs at most the read tier, got %s", row.Name, row.Tier)
			}
			continue
		}
		if row.Tier != want[guard] {
			t.Errorf("%s: route %s is %s, so the tool must be tier %s, got %s",
				row.Name, route, guard, want[guard], row.Tier)
		}
	}
}

// ── rights coverage (REQ-043: every capability covered, checked per tool) ──────────────────

// Without the DevLab right EVERY tool refuses, and it refuses before it reaches its capability.
func TestEveryToolRefusesACallerWithoutTheRight(t *testing.T) {
	s := &Server{}
	tools := s.mcpTools()
	if len(tools) != len(mcpToolRows()) {
		t.Fatalf("the served table is the whole table: %d of %d", len(tools), len(mcpToolRows()))
	}
	for _, tool := range tools {
		out, err := tool.Call(mcpCtx(true), withoutRight(), json.RawMessage(`{}`))
		if err == nil {
			t.Errorf("%s: answered %v without the right", tool.Name, out)
			continue
		}
		if err != errMCPNoRight {
			t.Errorf("%s: refused with %q, want the missing-right refusal", tool.Name, err)
		}
	}
}

// The refusal names what is missing (a person and an agent read the same sentence).
func TestRightRefusalNamesTheRight(t *testing.T) {
	if !strings.Contains(errMCPNoRight.Error(), rightGroup) {
		t.Errorf("the refusal names the right: %v", errMCPNoRight)
	}
}

// State-changing tools need the CSRF double-submit on the cookie path; reading tools do not.
func TestMutatingToolsNeedCSRFOnTheCookiePath(t *testing.T) {
	s := &Server{}
	u := withRight()
	for _, row := range mcpToolRows() {
		err := s.mcpAuthorize(row, &u, mcpCallInfo{csrfOK: false})
		if row.Tier == tierRead {
			if err != nil {
				t.Errorf("%s: a reading tool needs no CSRF token, got %v", row.Name, err)
			}
			continue
		}
		if err != errMCPCSRF {
			t.Errorf("%s: without a CSRF token the tool must refuse, got %v", row.Name, err)
		}
	}
}

// Tools that touch a user's own repository need a linked GitHub account, exactly as their route
// does; the constitution/run tools do not (those push with the service's own identity).
func TestWriteToolsNeedALinkedAccount(t *testing.T) {
	s := &Server{v: &auth.Verifier{}} // no dev-bypass, no link store ⇒ nobody is linked
	u := withRight()
	for _, row := range mcpToolRows() {
		err := s.mcpAuthorize(row, &u, mcpCallInfo{csrfOK: true})
		switch row.Tier {
		case tierWrite:
			if err == nil || !strings.Contains(err.Error(), errNoGitHubLink) {
				t.Errorf("%s: without a GitHub link the tool must refuse, got %v", row.Name, err)
			}
		default:
			if err != nil {
				t.Errorf("%s: needs no GitHub link, got %v", row.Name, err)
			}
		}
	}
}

// ── table hygiene ─────────────────────────────────────────────────────────────────────────

var toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)+$`)

// One vocabulary, one shape: names are unique snake_case, every tool explains itself, and the
// behaviour hints follow the method (a GET is read-only, a destructive tool is never a GET).
func TestToolTableHygiene(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range mcpToolRows() {
		if seen[row.Name] {
			t.Errorf("duplicate tool name %s", row.Name)
		}
		seen[row.Name] = true
		if !toolNameRe.MatchString(row.Name) {
			t.Errorf("%s: tool names are <subject>_<verb> in snake_case", row.Name)
		}
		if strings.TrimSpace(row.Desc) == "" {
			t.Errorf("%s: a tool without a description is not self-explaining", row.Name)
		}
		if row.Handler == nil {
			t.Errorf("%s: no handler", row.Name)
		}
		if row.Destructive && row.readOnly() {
			t.Errorf("%s: a reading tool cannot be destructive", row.Name)
		}
		if !strings.HasPrefix(row.Path, "/api/") {
			t.Errorf("%s: a tool rides an internal route, got %q", row.Name, row.Path)
		}
		placeholders := regexp.MustCompile(`\{(\w+)\}`).FindAllStringSubmatch(row.Path, -1)
		for _, ph := range placeholders {
			found := false
			for _, p := range row.Params {
				if p.In == inPath && p.wire() == ph[1] {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: path placeholder {%s} has no argument", row.Name, ph[1])
			}
		}
		names := map[string]bool{}
		for _, p := range row.Params {
			if names[p.Name] {
				t.Errorf("%s: duplicate argument %s", row.Name, p.Name)
			}
			names[p.Name] = true
			if strings.TrimSpace(p.Desc) == "" {
				t.Errorf("%s: argument %s explains nothing", row.Name, p.Name)
			}
			if p.In == inPath && !p.Required {
				t.Errorf("%s: path argument %s must be required", row.Name, p.Name)
			}
		}
	}
}

// The whole table validates as a protocol table (named, described, schema'd, callable).
func TestServedTableValidates(t *testing.T) {
	s := &Server{}
	srv := mcp.NewServer(s.mcpTools(), mcp.ServerInfo{Name: mcpServerName, Version: version})
	if err := srv.Validate(); err != nil {
		t.Fatalf("the served table must be usable: %v", err)
	}
	if len(srv.Tools()) != len(mcpToolRows()) {
		t.Errorf("no tool is dropped on the way to the protocol: %d of %d", len(srv.Tools()), len(mcpToolRows()))
	}
}

// One message must be able to carry the largest body a tool accepts, or an upload would be
// refused by the transport instead of by the rule that means it.
func TestMessageCapCoversTheLargestBody(t *testing.T) {
	if mcp.MaxMessageBytes < maxAttachmentBodyBytes {
		t.Errorf("the MCP message cap (%d) must cover the largest tool body (%d)",
			mcp.MaxMessageBytes, maxAttachmentBodyBytes)
	}
}

// ── the address stays server-side ─────────────────────────────────────────────────────────

// No argument can steer a call: an argument that looks like a path or a URL is escaped into ONE
// segment of the tool's own route, and the target never gains a host.
func TestArgumentsCannotSteerTheAddress(t *testing.T) {
	rows := map[string]mcpTool{}
	for _, row := range mcpToolRows() {
		rows[row.Name] = row
	}
	u := withRight()
	cases := []struct {
		tool string
		args string
		want string // the path the request must keep
	}{
		{"run_get", `{"id":"../../../etc/passwd"}`, "/api/mercury/runs/..%2F..%2F..%2Fetc%2Fpasswd"},
		{"run_get", `{"id":"http://elsewhere.example/x"}`, "/api/mercury/runs/http:%2F%2Felsewhere.example%2Fx"},
		{"repo_get", `{"id":"a/b"}`, "/api/repos/a%2Fb"},
	}
	for _, c := range cases {
		req, err := mcpRequest(context.Background(), rows[c.tool], &u, mcpCallInfo{}, json.RawMessage(c.args))
		if err != nil {
			t.Fatalf("%s(%s): %v", c.tool, c.args, err)
		}
		if req.URL.EscapedPath() != c.want {
			t.Errorf("%s(%s): path %q, want %q", c.tool, c.args, req.URL.EscapedPath(), c.want)
		}
		if req.URL.Host != "" || req.URL.Scheme != "" || req.Host != "" {
			t.Errorf("%s: the target must carry no host: scheme=%q host=%q", c.tool, req.URL.Scheme, req.URL.Host)
		}
	}
	// The handler still receives the raw value — escaping is transport, not meaning.
	req, err := mcpRequest(context.Background(), rows["run_get"], &u, mcpCallInfo{}, json.RawMessage(`{"id":"a/b"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := req.PathValue("id"); got != "a/b" {
		t.Errorf("path value = %q, want the raw argument", got)
	}
}

// Arguments land where the route expects them: path, query (under the wire name the route uses)
// and JSON body. A mutating call always carries a JSON object body, even when empty.
func TestArgumentsTravelWhereTheRouteExpectsThem(t *testing.T) {
	rows := map[string]mcpTool{}
	for _, row := range mcpToolRows() {
		rows[row.Name] = row
	}
	u := withRight()

	req, err := mcpRequest(context.Background(), rows["calendar_get"], &u, mcpCallInfo{},
		json.RawMessage(`{"days":14,"kind":"todo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.Query().Get("days"); got != "14" {
		t.Errorf("days = %q", got)
	}
	if got := req.URL.Query().Get("type"); got != "todo" {
		t.Errorf("the calendar's kind travels as the route's own query name: %q", got)
	}

	req, err = mcpRequest(context.Background(), rows["file_write"], &u, mcpCallInfo{},
		json.RawMessage(`{"id":"repo","path":"a.txt","content":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["path"] != "a.txt" || body["content"] != "hello" {
		t.Errorf("body = %+v", body)
	}
	if _, leaked := body["id"]; leaked {
		t.Errorf("a path argument does not leak into the body: %+v", body)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("a body is announced as JSON: %q", req.Header.Get("Content-Type"))
	}

	req, err = mcpRequest(context.Background(), rows["notice_clear"], &u, mcpCallInfo{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var empty map[string]any
	if err := json.NewDecoder(req.Body).Decode(&empty); err != nil {
		t.Fatalf("an argument-less mutation still carries an object body: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("body = %+v, want {}", empty)
	}

	// An explicit null belongs in a document (it clears a field); in an address it is simply absent.
	req, err = mcpRequest(context.Background(), rows["run_update"], &u, mcpCallInfo{},
		json.RawMessage(`{"id":"r1","title":"t","dueAt":null}`))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.NewDecoder(req.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if got, ok := doc["dueAt"]; !ok || strings.TrimSpace(string(got)) != "null" {
		t.Errorf("an explicit null reaches the document: %v", doc)
	}
	req, err = mcpRequest(context.Background(), rows["calendar_get"], &u, mcpCallInfo{},
		json.RawMessage(`{"days":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.RawQuery != "" {
		t.Errorf("a null query argument is absent, not empty: %q", req.URL.RawQuery)
	}
}

// The caller's own credentials travel with a proxied capability, so it acts AS the caller.
func TestCallerCredentialsAreForwarded(t *testing.T) {
	rows := map[string]mcpTool{}
	for _, row := range mcpToolRows() {
		rows[row.Name] = row
	}
	u := withRight()
	call := mcpCallInfo{cookie: "h_access=abc; h_csrf=xyz", authorization: "Bearer t"}
	req, err := mcpRequest(context.Background(), rows["assistant_ask"], &u, call,
		json.RawMessage(`{"id":"repo","prompt":"why?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Cookie") != call.cookie || req.Header.Get("Authorization") != call.authorization {
		t.Errorf("credentials are forwarded: %+v", req.Header)
	}
	if got := userFrom(req); got == nil || got.Username != u.Username {
		t.Errorf("the resolved caller travels on the request: %+v", got)
	}
}

// ── answers ───────────────────────────────────────────────────────────────────────────────

func TestAnswerMapping(t *testing.T) {
	// A JSON answer stays structured.
	rec := &mcpRecorder{limit: 1 << 20}
	rec.Header().Set("Content-Type", "application/json")
	rec.WriteHeader(http.StatusOK)
	_, _ = rec.Write([]byte(`{"ok":true}`))
	out, err := mcpAnswer(rec, "/api/x")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := out.(*mcp.Content); c == nil || string(c.JSON) != `{"ok":true}` {
		t.Errorf("JSON answer = %+v", out)
	}

	// An empty answer is an honest confirmation, not an empty document.
	rec = &mcpRecorder{limit: 1 << 20}
	rec.WriteHeader(http.StatusNoContent)
	out, err = mcpAnswer(rec, "/api/x")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := out.(*mcp.Content); c == nil || c.Text == "" {
		t.Errorf("empty answer = %+v", out)
	}

	// Bytes keep their media type and their place inside the service.
	rec = &mcpRecorder{limit: 1 << 20}
	rec.Header().Set("Content-Type", "image/png")
	_, _ = rec.Write([]byte{0x89, 'P'})
	out, err = mcpAnswer(rec, "/api/repos/r/raw?path=vision/a.png")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := out.(*mcp.Content)
	if c == nil || c.MIME != "image/png" || c.URI != "/api/repos/r/raw?path=vision/a.png" {
		t.Errorf("byte answer = %+v", out)
	}

	// A refusal keeps the wording the surface would show.
	rec = &mcpRecorder{limit: 1 << 20}
	rec.Header().Set("Content-Type", "application/json")
	rec.WriteHeader(http.StatusForbidden)
	_, _ = rec.Write([]byte(`{"detail":"Link your GitHub account first"}`))
	if _, err = mcpAnswer(rec, "/api/x"); err == nil || err.Error() != "Link your GitHub account first" {
		t.Errorf("refusal error = %v", err)
	}

	// An answer beyond the message cap is named, never truncated silently.
	rec = &mcpRecorder{limit: 8}
	rec.Header().Set("Content-Type", "application/json")
	if _, werr := rec.Write([]byte(`{"a":"much too long"}`)); werr == nil {
		t.Error("writing past the cap must fail")
	}
	if _, err = mcpAnswer(rec, "/api/x"); err != errMCPTooLarge {
		t.Errorf("oversized answer = %v", err)
	}
}

// ── the endpoint end to end ───────────────────────────────────────────────────────────────

// post drives the real endpoint the way the guarded route does: an authenticated caller, one
// JSON-RPC message.
func post(t *testing.T, s *Server, u auth.User, msg string, tweak ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(msg))
	r.Header.Set("Content-Type", "application/json")
	for _, f := range tweak {
		f(r)
	}
	r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &u))
	rec := httptest.NewRecorder()
	s.mcpEndpoint(rec, r)
	return rec
}

// withCSRF presents the double-submit pair the cookie path requires.
func withCSRF(r *http.Request) {
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "tok"})
	r.Header.Set("X-CSRF-Token", "tok")
}

func rpcResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("answer is not JSON: %v: %s", err, rec.Body.String())
	}
	if e, bad := body["error"]; bad {
		t.Fatalf("unexpected protocol error: %v", e)
	}
	res, _ := body["result"].(map[string]any)
	return res
}

// The mounted endpoint speaks the handshake, serves the whole table, and runs a capability end
// to end through its own route handler.
func TestEndpointServesTheTableAndCalls(t *testing.T) {
	// Port ledger fixtures: the tool answers from the observed host state, like the route does.
	routes := t.TempDir()
	if err := os.WriteFile(filepath.Join(routes, "aigentic.caddy"),
		[]byte("handle /api/services/aigentic/* {\n\treverse_proxy 127.0.0.1:8780\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(t.TempDir(), "tcp")
	if err := os.WriteFile(proc, []byte("  sl  local_address rem_address   st\n"+
		"   0: 0100007F:224C 00000000:0000 0A 0 0 0 0 0 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_CADDY_CONF", routes)
	t.Setenv("DEVLAB_PROC_NET_TCP", proc)
	t.Setenv("DEVLAB_PROC_NET_TCP6", proc)

	s := &Server{v: &auth.Verifier{}}

	res := rpcResult(t, post(t, s, withRight(), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+mcp.ProtocolVersion+`"}}`))
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != mcpServerName || info["version"] != version {
		t.Errorf("the server names itself uniformly: %+v", info)
	}

	res = rpcResult(t, post(t, s, withRight(), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	list, _ := res["tools"].([]any)
	if len(list) != len(mcpToolRows()) {
		t.Errorf("tools/list serves the whole table: %d of %d", len(list), len(mcpToolRows()))
	}

	res = rpcResult(t, post(t, s, withRight(), `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"port_list"}}`))
	if res["isError"] == true {
		t.Fatalf("port_list answered an error: %s", rec2str(res))
	}
	blocks, _ := res["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("an answer carries content: %s", rec2str(res))
	}
	first, _ := blocks[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "8780") {
		t.Errorf("the tool answers what the route answers: %s", text)
	}
}

// A capability that cannot answer says why, in the wording the surface uses — as a tool result,
// so the agent reads the reason.
func TestEndpointReportsAnHonestRefusalAsResult(t *testing.T) {
	t.Setenv("DEVLAB_CADDY_CONF", filepath.Join(t.TempDir(), "absent"))
	s := &Server{v: &auth.Verifier{}}
	res := rpcResult(t, post(t, s, withRight(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"port_list"}}`))
	if res["isError"] != true {
		t.Fatalf("an unavailable capability is flagged: %s", rec2str(res))
	}
	blocks, _ := res["content"].([]any)
	first, _ := blocks[0].(map[string]any)
	if text, _ := first["text"].(string); strings.TrimSpace(text) == "" {
		t.Errorf("the refusal carries its reason: %+v", first)
	}
}

// A caller without the right reaches no capability, even though the endpoint answered.
func TestEndpointRefusesARightlessCaller(t *testing.T) {
	s := &Server{v: &auth.Verifier{}}
	res := rpcResult(t, post(t, s, withoutRight(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"port_list"}}`))
	if res["isError"] != true {
		t.Fatalf("a rightless call is refused: %s", rec2str(res))
	}
	blocks, _ := res["content"].([]any)
	first, _ := blocks[0].(map[string]any)
	if text, _ := first["text"].(string); !strings.Contains(text, rightGroup) {
		t.Errorf("the refusal names the right: %+v", first)
	}
}

// ── the machine-readable parity artifact ──────────────────────────────────────────────────

// The generated parity list is the artifact the frontend parity test reads. It is regenerated
// deliberately (UPDATE_FIXTURES=1) — drift is a contract change, never a surprise.
func TestParityArtifactInStep(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contract", "mcp-tools.json")
	got, err := json.MarshalIndent(MCPParityList(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("UPDATE_FIXTURES") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the parity artifact is missing (run with UPDATE_FIXTURES=1 to create): %v", err)
	}
	if string(want) != string(got) {
		t.Errorf("the MCP tool table drifted from contract/mcp-tools.json — regenerate deliberately with UPDATE_FIXTURES=1")
	}
}

// Every artifact row is complete enough for the two-sided parity test to be built on it.
func TestParityArtifactIsSelfContained(t *testing.T) {
	list := MCPParityList()
	if list.ProtocolVersion == "" {
		t.Error("the artifact names the protocol revision")
	}
	if len(list.Tools) == 0 {
		t.Fatal("no tools in the artifact")
	}
	for _, tool := range list.Tools {
		if tool.Name == "" || tool.Description == "" || tool.Route == "" || tool.Tier == "" {
			t.Errorf("incomplete row: %+v", tool)
		}
		if len(tool.InputSchema) == 0 {
			t.Errorf("%s: no argument schema in the artifact", tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("%s: schema is not JSON: %v", tool.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s: arguments are an object: %v", tool.Name, schema["type"])
		}
		if tool.ReadOnly != (tool.Method == http.MethodGet) {
			t.Errorf("%s: readOnly must follow the method", tool.Name)
		}
	}
	for i := 1; i < len(list.Tools); i++ {
		if list.Tools[i-1].Name >= list.Tools[i].Name {
			t.Errorf("the artifact is sorted by name: %s before %s", list.Tools[i-1].Name, list.Tools[i].Name)
		}
	}
}

func rec2str(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// A state-changing tool runs through the endpoint when the caller presents the double-submit the
// cookie path demands — and refuses, with the reason, when it does not.
func TestEndpointMutatingToolFollowsTheCookiePath(t *testing.T) {
	s := &Server{v: &auth.Verifier{}}
	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"notice_clear"}}`

	res := rpcResult(t, post(t, s, withRight(), msg, withCSRF))
	if res["isError"] == true {
		t.Fatalf("with a valid CSRF token the capability runs: %s", rec2str(res))
	}

	res = rpcResult(t, post(t, s, withRight(), msg))
	if res["isError"] != true {
		t.Fatalf("without a CSRF token the capability is refused: %s", rec2str(res))
	}
	blocks, _ := res["content"].([]any)
	first, _ := blocks[0].(map[string]any)
	if text, _ := first["text"].(string); !strings.Contains(text, "CSRF") {
		t.Errorf("the refusal names what is missing: %+v", first)
	}
}

// Arguments are validated against the tool's own schema BEFORE the capability runs: a missing or
// misspelled argument is a protocol error that names itself, not a half-executed call.
func TestEndpointValidatesArgumentsBeforeCalling(t *testing.T) {
	s := &Server{v: &auth.Verifier{}}
	for _, c := range []struct{ msg, want string }{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_get","arguments":{}}}`, `"id" is required`},
		{`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_get","arguments":{"idd":"r1"}}}`, `unknown argument "idd"`},
		{`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"calendar_get","arguments":{"kind":"nonsense"}}}`, `"kind" must be one of`},
		{`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"calendar_get","arguments":{"days":"seven"}}}`, `"days" must be of type integer`},
	} {
		rec := post(t, s, withRight(), c.msg, withCSRF)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		e, isErr := body["error"].(map[string]any)
		if !isErr {
			t.Errorf("%s: want a protocol error, got %s", c.msg, rec.Body.String())
			continue
		}
		if msg, _ := e["message"].(string); !strings.Contains(msg, c.want) {
			t.Errorf("%s: message %q, want %q", c.msg, msg, c.want)
		}
	}
}
