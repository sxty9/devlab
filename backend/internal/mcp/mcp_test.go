package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"devlab/backend/internal/auth"
)

const devlabGroup = "hp_devlab_access"

func testTools() []Tool {
	return []Tool{
		{
			Name: "b_read", Description: "Read something.",
			Schema:      ObjectSchema([]Property{{Name: "id", Kind: KindString, Required: true}}),
			Annotations: Annotations{ReadOnly: true, Idempotent: true},
			Call: func(_ context.Context, u auth.User, args json.RawMessage) (any, error) {
				return map[string]any{"user": u.Username, "args": json.RawMessage(args)}, nil
			},
		},
		{
			Name: "a_write", Description: "Change something.",
			Schema:      ObjectSchema(nil),
			Annotations: Annotations{Destructive: true},
			Call: func(context.Context, auth.User, json.RawMessage) (any, error) {
				return nil, errors.New("This capability requires the " + devlabGroup + " right")
			},
		},
	}
}

func newTestServer(tools []Tool) *Server {
	return NewServer(tools, ServerInfo{Name: "devlab", Version: "1.2.3"})
}

// call posts one JSON-RPC message with an authenticated caller attached, as the guarded mount does.
func call(t *testing.T, s *Server, msg string, tweak ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(msg))
	r.Header.Set("Content-Type", "application/json")
	for _, f := range tweak {
		f(r)
	}
	r = r.WithContext(WithUser(r.Context(), auth.User{Username: "ada", Groups: []string{devlabGroup}}))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("answer is not JSON: %v: %s", err, rec.Body.String())
	}
	if body["jsonrpc"] != "2.0" {
		t.Errorf("every answer is JSON-RPC 2.0, got %v", body["jsonrpc"])
	}
	return body
}

func rpcErrOf(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	body := decode(t, rec)
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %s", rec.Body.String())
	}
	code, _ := e["code"].(float64)
	msg, _ := e["message"].(string)
	return int(code), msg
}

func resultOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := decode(t, rec)
	if e, bad := body["error"]; bad {
		t.Fatalf("expected a result, got error %v", e)
	}
	res, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %s", rec.Body.String())
	}
	return res
}

// The handshake negotiates: a revision this server speaks is echoed, an unknown one is answered
// with the current revision — and the answer names the server uniformly (service id + version).
func TestInitializeNegotiatesAndNamesTheServer(t *testing.T) {
	s := newTestServer(testTools())

	res := resultOf(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`))
	if res["protocolVersion"] != "2024-11-05" {
		t.Errorf("a supported revision is echoed, got %v", res["protocolVersion"])
	}

	res = resultOf(t, call(t, s, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`))
	if res["protocolVersion"] != ProtocolVersion {
		t.Errorf("an unknown revision is answered with the current one, got %v", res["protocolVersion"])
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "devlab" || info["version"] != "1.2.3" {
		t.Errorf("serverInfo names the service and its version: %+v", info)
	}
	caps, _ := res["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("the tools capability is declared: %+v", caps)
	}
}

// tools/list answers the whole table, sorted, each entry with a schema and its behaviour hints.
func TestToolsListIsCompleteSortedAndSchemad(t *testing.T) {
	s := newTestServer(testTools())
	res := resultOf(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	list, _ := res["tools"].([]any)
	if len(list) != 2 {
		t.Fatalf("both tools are listed: %s", mustJSON(t, res))
	}
	first, _ := list[0].(map[string]any)
	second, _ := list[1].(map[string]any)
	if first["name"] != "a_write" || second["name"] != "b_read" {
		t.Errorf("the table is sorted by name: %v, %v", first["name"], second["name"])
	}
	if first["description"] == "" || first["inputSchema"] == nil {
		t.Errorf("every tool carries a description and an argument schema: %+v", first)
	}
	ann, _ := first["annotations"].(map[string]any)
	if ann["destructiveHint"] != true {
		t.Errorf("a state-changing tool is marked destructive before it is called: %+v", ann)
	}
	ann2, _ := second["annotations"].(map[string]any)
	if ann2["readOnlyHint"] != true {
		t.Errorf("a reading tool is marked read-only: %+v", ann2)
	}
}

// A tool answers with structured content AND the text block older clients read.
func TestToolsCallAnswersStructuredAndText(t *testing.T) {
	s := newTestServer(testTools())
	res := resultOf(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"b_read","arguments":{"id":"x1"}}}`))
	if res["isError"] == true {
		t.Fatalf("the call succeeded: %s", mustJSON(t, res))
	}
	structured, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("a JSON answer travels as structured content: %s", mustJSON(t, res))
	}
	if structured["user"] != "ada" {
		t.Errorf("the tool sees the authenticated caller: %+v", structured)
	}
	blocks, _ := res["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("exactly one content block: %s", mustJSON(t, res))
	}
	block, _ := blocks[0].(map[string]any)
	if block["type"] != "text" || !strings.Contains(block["text"].(string), `"user":"ada"`) {
		t.Errorf("the same answer is readable as text: %+v", block)
	}
}

// What a tool itself reports (a refusal, a named condition) is a RESULT with isError — the agent
// reads the reason instead of a bare protocol failure.
func TestToolRefusalTravelsAsResult(t *testing.T) {
	s := newTestServer(testTools())
	res := resultOf(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a_write","arguments":{}}}`))
	if res["isError"] != true {
		t.Fatalf("a tool's own refusal is flagged: %s", mustJSON(t, res))
	}
	blocks, _ := res["content"].([]any)
	block, _ := blocks[0].(map[string]any)
	if !strings.Contains(block["text"].(string), devlabGroup) {
		t.Errorf("the refusal names the missing right: %+v", block)
	}
}

// Bytes travel by media type: an image as an image block, anything else as an embedded resource
// named by its place INSIDE the service — never by an absolute address.
func TestByteAnswersCarryMediaTypeAndInServiceURI(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	pdf := []byte("%PDF-1.7")
	s := newTestServer([]Tool{
		{Name: "img", Description: "Image.", Schema: ObjectSchema(nil), Call: func(context.Context, auth.User, json.RawMessage) (any, error) {
			return &Content{Bytes: png, MIME: "image/png", URI: "/api/repos/r/raw?path=vision/a.png"}, nil
		}},
		{Name: "doc", Description: "Document.", Schema: ObjectSchema(nil), Call: func(context.Context, auth.User, json.RawMessage) (any, error) {
			return &Content{Bytes: pdf, MIME: "application/pdf", URI: "/api/mercury/runs/r1/attachments/a1/raw"}, nil
		}},
		{Name: "txt", Description: "Text.", Schema: ObjectSchema(nil), Call: func(context.Context, auth.User, json.RawMessage) (any, error) {
			return &Content{Bytes: []byte("plain"), MIME: "text/plain; charset=utf-8"}, nil
		}},
	})

	block := firstBlock(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"img"}}`))
	if block["type"] != "image" || block["mimeType"] != "image/png" {
		t.Errorf("an image answers as an image block: %+v", block)
	}
	if block["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Errorf("the image bytes travel base64-encoded: %+v", block)
	}

	block = firstBlock(t, call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"doc"}}`))
	if block["type"] != "resource" {
		t.Fatalf("other bytes answer as an embedded resource: %+v", block)
	}
	resource, _ := block["resource"].(map[string]any)
	uri, _ := resource["uri"].(string)
	if !strings.HasPrefix(uri, "/api/") || strings.Contains(uri, "://") {
		t.Errorf("the resource is named by its place inside the service, not by a host: %q", uri)
	}
	if resource["blob"] != base64.StdEncoding.EncodeToString(pdf) {
		t.Errorf("the document bytes travel base64-encoded: %+v", resource)
	}

	block = firstBlock(t, call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"txt"}}`))
	if block["type"] != "text" || block["text"] != "plain" {
		t.Errorf("text bytes answer as readable text: %+v", block)
	}
}

// Protocol refusals: unknown tool, arguments that miss the schema, unknown method.
func TestProtocolRefusals(t *testing.T) {
	s := newTestServer(testTools())

	if code, msg := rpcErrOf(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`)); code != codeInvalidPar || !strings.Contains(msg, "nope") {
		t.Errorf("an unknown tool is named: %d %s", code, msg)
	}
	if code, msg := rpcErrOf(t, call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"b_read","arguments":{}}}`)); code != codeInvalidPar || !strings.Contains(msg, `"id"`) {
		t.Errorf("a missing required argument is named: %d %s", code, msg)
	}
	if code, _ := rpcErrOf(t, call(t, s, `{"jsonrpc":"2.0","id":3,"method":"resources/list"}`)); code != codeNoSuchMeth {
		t.Errorf("an undeclared method is refused: %d", code)
	}
	if code, _ := rpcErrOf(t, call(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"b_read","arguments":{"id":7}}}`)); code != codeInvalidPar {
		t.Errorf("a wrongly typed argument is refused: %d", code)
	}
	if code, _ := rpcErrOf(t, call(t, s, `{"id":5,"method":"ping"}`)); code != codeInvalidReq {
		t.Errorf("a message that is not JSON-RPC 2.0 is refused: %d", code)
	}
	if code, _ := rpcErrOf(t, call(t, s, `{"jsonrpc":"2.0","id":6,`)); code != codeParse {
		t.Errorf("malformed JSON is a parse error: %d", code)
	}
	if code, _ := rpcErrOf(t, call(t, s, `[{"jsonrpc":"2.0","id":7,"method":"ping"}]`)); code != codeInvalidReq {
		t.Errorf("a batch is refused: %d", code)
	}
}

// ping answers, and a notification (no id) is acknowledged without a body.
func TestPingAndNotification(t *testing.T) {
	s := newTestServer(testTools())
	if res := resultOf(t, call(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)); len(res) != 0 {
		t.Errorf("ping answers an empty result: %+v", res)
	}
	rec := call(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != "" {
		t.Errorf("a notification is acknowledged without an answer: %d %q", rec.Code, rec.Body.String())
	}
}

// The transport refuses everything that could carry ambient cross-site authority, and everything
// that is not one JSON message.
func TestTransportGuards(t *testing.T) {
	s := newTestServer(testTools())

	r := httptest.NewRequest(http.MethodGet, "/api/mcp", nil)
	r = r.WithContext(WithUser(r.Context(), auth.User{Username: "ada"}))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Errorf("only POST carries a message: %d %q", rec.Code, rec.Header().Get("Allow"))
	}

	rec = call(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	})
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a form post cannot reach a tool: %d", rec.Code)
	}

	rec = call(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, func(r *http.Request) {
		r.Header.Set("Origin", "https://elsewhere.example")
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("a cross-origin call is refused: %d", rec.Code)
	}

	rec = call(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, func(r *http.Request) {
		r.Header.Set("Origin", "http://"+r.Host)
	})
	if rec.Code != http.StatusOK {
		t.Errorf("a same-origin call is served: %d", rec.Code)
	}
}

// Without a caller attached by the mount nothing runs — the protocol layer never authenticates
// by itself, so a tool can never execute unauthenticated.
func TestNoCallerNoCall(t *testing.T) {
	s := newTestServer(testTools())
	r := httptest.NewRequest(http.MethodPost, "/api/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"b_read","arguments":{"id":"x"}}}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if code, msg := rpcErrOf(t, rec); code != codeServerError || !strings.Contains(msg, "authenticated") {
		t.Errorf("an unauthenticated message is refused: %d %s", code, msg)
	}
}

// An oversized message is refused before it is parsed.
func TestOversizedMessageRefused(t *testing.T) {
	s := newTestServer(testTools())
	big := strings.Repeat("x", 64)
	r := httptest.NewRequest(http.MethodPost, "/api/mcp", &endlessReader{chunk: big})
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(WithUser(r.Context(), auth.User{Username: "ada"}))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a message beyond the cap is refused: %d", rec.Code)
	}
}

// A table that cannot serve is named, not silently served.
func TestValidateNamesABrokenTable(t *testing.T) {
	if err := newTestServer(nil).Validate(); !errors.Is(err, ErrNoTools) {
		t.Errorf("an empty table is refused: %v", err)
	}
	cases := []struct {
		name string
		tool Tool
		want string
	}{
		{"nameless", Tool{Description: "d", Schema: ObjectSchema(nil), Call: okCall}, "without a name"},
		{"undescribed", Tool{Name: "t", Schema: ObjectSchema(nil), Call: okCall}, "without a description"},
		{"schemaless", Tool{Name: "t", Description: "d", Call: okCall}, "without an argument schema"},
		{"unimplemented", Tool{Name: "t", Description: "d", Schema: ObjectSchema(nil)}, "without an implementation"},
	}
	for _, c := range cases {
		err := newTestServer([]Tool{c.tool}).Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

// A duplicate name cannot shadow a capability: the first entry stays and the table keeps one
// entry per name.
func TestDuplicateNameKeepsTheFirst(t *testing.T) {
	s := newTestServer([]Tool{
		{Name: "dup", Description: "first", Schema: ObjectSchema(nil), Call: okCall},
		{Name: "dup", Description: "second", Schema: ObjectSchema(nil), Call: okCall},
	})
	if len(s.Tools()) != 1 || s.Tools()[0].Description != "first" {
		t.Fatalf("one entry per name, the first one: %+v", s.Tools())
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────────────────

func okCall(context.Context, auth.User, json.RawMessage) (any, error) { return nil, nil }

func firstBlock(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	res := resultOf(t, rec)
	blocks, _ := res["content"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("no content block: %s", mustJSON(t, res))
	}
	block, _ := blocks[0].(map[string]any)
	return block
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// endlessReader feeds more bytes than the cap allows without allocating them all.
type endlessReader struct{ chunk string }

func (e *endlessReader) Read(p []byte) (int, error) {
	n := copy(p, e.chunk)
	for n < len(p) {
		n += copy(p[n:], e.chunk)
	}
	return n, nil
}

// The protocol layer knows no domain: it imports the standard library and the identity package,
// nothing of the service's subject matter. Anything else would put a capability in the transport.
func TestProtocolLayerKnowsNoDomain(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(path, "devlab/") {
				continue // standard library
			}
			if path != "devlab/backend/internal/auth" {
				t.Errorf("%s imports %s — the protocol layer carries no domain package", e.Name(), path)
			}
		}
	}
}
