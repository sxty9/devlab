// Package mcp is the MCP protocol server (REQ-043): JSON-RPC over HTTP, WITHOUT any domain
// knowledge — the tool table (with its per-tool rights coverage) lives in the api layer. The
// server's address is server-side infrastructure, never part of a request: nothing here reads a
// host, base URL or target from the caller, so no manipulated call can steer an agent onto a
// foreign host.
//
// Transport: one JSON-RPC message per POST, answered as application/json. There is no MCP
// session id — the caller's Holistic session (or bearer) IS the session, so the service keeps
// exactly one authentication path. Batches are refused (they left the protocol).
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"devlab/backend/internal/auth"
)

// ProtocolVersion is the revision this server speaks; older revisions stay negotiable so an
// older agent still gets a usable answer instead of a protocol error.
const ProtocolVersion = "2025-06-18"

// MaxMessageBytes caps one JSON-RPC message. It must stay above the largest tool body the api
// layer accepts (media uploads travel as base64) — api asserts that in its own test.
const MaxMessageBytes = 48 << 20

// negotiable lists the revisions this server answers with, newest first.
var negotiable = []string{ProtocolVersion, "2025-03-26", "2024-11-05"}

// JSON-RPC + protocol error codes. -32000 is the implementation-defined range: it carries the
// refusals that are not the caller's syntax (no authenticated caller, unusable table entry).
const (
	codeParse       = -32700
	codeInvalidReq  = -32600
	codeNoSuchMeth  = -32601
	codeInvalidPar  = -32602
	codeServerError = -32000
)

// Annotations are the behaviour hints a client shows before it calls: read-only tools are safe
// to explore, destructive ones change or remove state (REQ-040.6 — the effect is visible before
// the act), idempotent ones may be retried.
type Annotations struct {
	ReadOnly    bool `json:"readOnlyHint,omitempty"`
	Destructive bool `json:"destructiveHint,omitempty"`
	Idempotent  bool `json:"idempotentHint,omitempty"`
}

// Tool is one exposed capability: name, description, JSON schema and its call. Every call is
// covered by the rights system — the api layer checks hp_devlab_access per tool before Call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Annotations Annotations
	Call        func(ctx context.Context, u auth.User, args json.RawMessage) (any, error)
}

// Content is one tool's answer: a structured JSON value, plain text, or bytes with a media
// type. A tool that answers bytes names where they live inside the service (URI) — never an
// absolute address.
type Content struct {
	JSON  json.RawMessage
	Text  string
	Bytes []byte
	MIME  string
	URI   string
}

// ServerInfo names the server. The name is uniform for the whole service (its service id), so
// agents address one server per service through the central infrastructure.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Server speaks the protocol over the given tool table.
type Server struct {
	info  ServerInfo
	tools []Tool
	index map[string]*Tool
}

// NewServer builds the protocol server for a tool table. Names are unique by construction; a
// duplicate keeps the first entry and is reported (the api-side test forbids it outright).
func NewServer(tools []Tool, info ServerInfo) *Server {
	s := &Server{info: info, index: make(map[string]*Tool, len(tools))}
	s.tools = make([]Tool, 0, len(tools))
	for _, t := range tools {
		if _, dup := s.index[t.Name]; dup {
			log.Printf("mcp: duplicate tool %q ignored", t.Name)
			continue
		}
		s.tools = append(s.tools, t)
		s.index[t.Name] = nil // placeholder; the index is built after the sort settles addresses
	}
	sort.Slice(s.tools, func(i, j int) bool { return s.tools[i].Name < s.tools[j].Name })
	for i := range s.tools {
		s.index[s.tools[i].Name] = &s.tools[i]
	}
	return s
}

// userCtxKey namespaces the caller the mount resolved.
type userCtxKey struct{}

// WithUser hands the authenticated caller to the protocol server. The mount (the guarded HTTP
// route) resolves the user; the protocol layer never authenticates by itself.
func WithUser(ctx context.Context, u auth.User) context.Context {
	return context.WithValue(ctx, userCtxKey{}, u)
}

// userFrom returns the caller the mount attached, if any.
func userFrom(ctx context.Context) (auth.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(auth.User)
	return u, ok
}

// Handler returns the JSON-RPC-over-HTTP handler.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpErr(w, http.StatusMethodNotAllowed, "MCP messages are POSTed")
		return
	}
	// A JSON content type is required. That is also what keeps a form on a foreign page from
	// reaching a tool: a cross-site JSON POST is not a simple request, so the browser preflights
	// it and the preflight goes unanswered. With the same-origin check below the transport
	// carries no ambient cross-site authority.
	if ct := r.Header.Get("Content-Type"); !isJSON(ct) {
		httpErr(w, http.StatusUnsupportedMediaType, "MCP messages are application/json")
		return
	}
	if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
		httpErr(w, http.StatusForbidden, "Cross-origin MCP calls are refused")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxMessageBytes+1))
	if err != nil {
		httpErr(w, http.StatusBadRequest, "Could not read the message")
		return
	}
	if int64(len(body)) > MaxMessageBytes {
		httpErr(w, http.StatusRequestEntityTooLarge, "MCP message too large")
		return
	}
	if strings.HasPrefix(strings.TrimLeft(string(body), " \t\r\n"), "[") {
		writeRPC(w, errorResponse(nullID, codeInvalidReq, "Batched requests are not supported"))
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, errorResponse(nullID, codeParse, "Malformed JSON"))
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, errorResponse(idOf(req), codeInvalidReq, "Not a JSON-RPC 2.0 request"))
		return
	}
	// A notification carries no id and gets no body — acknowledged, never answered.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	u, ok := userFrom(r.Context())
	if !ok {
		writeRPC(w, errorResponse(idOf(req), codeServerError, "No authenticated caller"))
		return
	}
	result, rerr := s.dispatch(r.Context(), u, req)
	if rerr != nil {
		writeRPC(w, errorResponse(idOf(req), rerr.Code, rerr.Message))
		return
	}
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: idOf(req), Result: result})
}

// dispatch answers one request method. A refusal that belongs to the protocol (unknown method,
// unknown tool, arguments that miss the schema) is a JSON-RPC error; everything a tool itself
// reports lands in the tool result, where the agent reads it.
func (s *Server) dispatch(ctx context.Context, u auth.User, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.initialize(req.Params), nil
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return s.list(), nil
	case "tools/call":
		return s.call(ctx, u, req.Params)
	default:
		return nil, &rpcError{Code: codeNoSuchMeth, Message: "Unknown method: " + req.Method}
	}
}

// initialize answers the handshake: the negotiated revision, the capabilities and who we are.
func (s *Server) initialize(params json.RawMessage) initializeResult {
	var in struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &in)
	version := ProtocolVersion
	for _, v := range negotiable {
		if v == in.ProtocolVersion {
			version = v
			break
		}
	}
	return initializeResult{
		ProtocolVersion: version,
		Capabilities:    capabilities{Tools: toolsCapability{ListChanged: false}},
		ServerInfo:      s.info,
	}
}

// list answers the whole table (sorted, unpaginated — a service surface, not a feed).
func (s *Server) list() toolsListResult {
	out := toolsListResult{Tools: make([]toolDescriptor, 0, len(s.tools))}
	for i := range s.tools {
		t := &s.tools[i]
		schema := t.Schema
		if len(schema) == 0 {
			schema = ObjectSchema(nil)
		}
		out.Tools = append(out.Tools, toolDescriptor{
			Name: t.Name, Description: t.Description, InputSchema: schema, Annotations: t.Annotations,
		})
	}
	return out
}

// call validates the arguments against the tool's own schema and runs it.
func (s *Server) call(ctx context.Context, u auth.User, params json.RawMessage) (any, *rpcError) {
	var in struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, &rpcError{Code: codeInvalidPar, Message: "Malformed call parameters"}
	}
	t, ok := s.index[in.Name]
	if !ok || t == nil {
		return nil, &rpcError{Code: codeInvalidPar, Message: "Unknown tool: " + in.Name}
	}
	if err := ValidateArgs(t.Schema, in.Arguments); err != nil {
		return nil, &rpcError{Code: codeInvalidPar, Message: err.Error()}
	}
	if t.Call == nil {
		return nil, &rpcError{Code: codeServerError, Message: "Tool " + t.Name + " has no implementation"}
	}
	out, err := t.Call(ctx, u, in.Arguments)
	if err != nil {
		return toolResult{Content: []contentBlock{{Type: "text", Text: err.Error()}}, IsError: true}, nil
	}
	return blocksFor(out), nil
}

// blocksFor shapes a tool's answer into content blocks: JSON as text plus structured content,
// text as text, bytes as an image or an embedded resource named by its in-service URI.
func blocksFor(out any) toolResult {
	switch v := out.(type) {
	case nil:
		return toolResult{Content: []contentBlock{{Type: "text", Text: "OK"}}}
	case *Content:
		return contentBlocks(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return toolResult{
				Content: []contentBlock{{Type: "text", Text: "Answer could not be encoded: " + err.Error()}},
				IsError: true,
			}
		}
		return contentBlocks(&Content{JSON: raw})
	}
}

func contentBlocks(c *Content) toolResult {
	switch {
	case len(c.JSON) > 0:
		res := toolResult{Content: []contentBlock{{Type: "text", Text: string(c.JSON)}}}
		// structuredContent is an object by contract; a JSON array travels as text only.
		if isJSONObject(c.JSON) {
			res.StructuredContent = c.JSON
		}
		return res
	case len(c.Bytes) > 0:
		if isTextMIME(c.MIME) {
			return toolResult{Content: []contentBlock{{Type: "text", Text: string(c.Bytes)}}}
		}
		data := base64.StdEncoding.EncodeToString(c.Bytes)
		if strings.HasPrefix(c.MIME, "image/") {
			return toolResult{Content: []contentBlock{{Type: "image", Data: data, MIMEType: c.MIME}}}
		}
		return toolResult{Content: []contentBlock{{
			Type:     "resource",
			Resource: &resourceBlock{URI: c.URI, MIMEType: c.MIME, Blob: data},
		}}}
	default:
		return toolResult{Content: []contentBlock{{Type: "text", Text: c.Text}}}
	}
}

// Validate reports whether the table is usable: non-empty, every tool named, described,
// schema'd and callable. The mount checks it once so a broken table is named, never served.
func (s *Server) Validate() error {
	if len(s.tools) == 0 {
		return ErrNoTools
	}
	for i := range s.tools {
		t := &s.tools[i]
		switch {
		case t.Name == "":
			return errors.New("mcp: tool without a name")
		case t.Description == "":
			return errors.New("mcp: tool " + t.Name + " without a description")
		case len(t.Schema) == 0:
			return errors.New("mcp: tool " + t.Name + " without an argument schema")
		case t.Call == nil:
			return errors.New("mcp: tool " + t.Name + " without an implementation")
		}
	}
	return nil
}

// ErrNoTools reports an empty table — a table with no tools would silently claim parity.
var ErrNoTools = errors.New("mcp: empty tool table")

// Tools returns the served table in served order — the parity audit reads it from here.
func (s *Server) Tools() []Tool { return s.tools }

// ── wire shapes ───────────────────────────────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
}

type capabilities struct {
	Tools toolsCapability `json:"tools"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type toolsListResult struct {
	Tools []toolDescriptor `json:"tools"`
}

type toolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations Annotations     `json:"annotations,omitempty"`
}

type toolResult struct {
	Content           []contentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

type contentBlock struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	Data     string         `json:"data,omitempty"`
	MIMEType string         `json:"mimeType,omitempty"`
	Resource *resourceBlock `json:"resource,omitempty"`
}

type resourceBlock struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ── helpers ───────────────────────────────────────────────────────────────────────────────

var nullID = json.RawMessage("null")

func idOf(req rpcRequest) json.RawMessage {
	if len(req.ID) == 0 {
		return nullID
	}
	return req.ID
}

func errorResponse(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func writeRPC(w http.ResponseWriter, res rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res)
}

// httpErr answers a transport-level refusal in the holistic error shape.
func httpErr(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"detail": detail})
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func isTextMIME(m string) bool {
	mt, _, err := mime.ParseMediaType(m)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func isJSONObject(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimLeft(string(raw), " \t\r\n"), "{")
}

// sameOrigin compares an Origin header against the host the request arrived on. Only the host
// is compared: the server's own address stays server-side, so there is nothing to configure and
// nothing a caller could point elsewhere.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}
