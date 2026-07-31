// MCP endpoint (REQ-043): POST /api/mcp mounts the protocol server. The TOOL TABLE lives here
// (mcp_tools.go — full DataSource parity, including config_get/config_set as the named callers
// of the configuration interface), each tool covered by hp_devlab_access and checked per call.
// The server address is server-side infrastructure, never part of a request.
//
// A tool call is dispatched IN PROCESS onto the handler of its own route: the arguments become
// the request that route already knows, the resolved caller travels on its context, and the
// answer is read back from a recorder. A capability therefore exists exactly once — the tool
// table adds a second way to REACH the surface, never a second implementation of it.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/mcp"
)

// mcpCallKey carries the per-request facts a tool needs beyond the caller: whether the CSRF
// double-submit held (cookie path) and the credentials to forward to a proxied capability.
const mcpCallKey ctxKey = 1

type mcpCallInfo struct {
	csrfOK bool
	// cookie/authorization are the caller's own credential headers. A capability that talks to
	// another service on the caller's behalf (the AI proxy) must act AS the caller, exactly as it
	// does on the HTTP path.
	cookie        string
	authorization string
}

// mcpServerName is how this server is named to agents: the service id, uniform for the whole
// service, so one service means one addressable MCP server.
const mcpServerName = "devlab"

// The table is rendered once: rows and their schemas are static data.
var (
	mcpRows       = mcpToolRows()
	mcpRowSchemas = renderMCPSchemas(mcpRows)
)

var (
	errMCPNoRight  = errors.New("This capability requires the hp_devlab_access right")
	errMCPCSRF     = errors.New("This capability changes state and needs a valid CSRF token on the cookie path")
	errMCPTooLarge = errors.New("The answer is too large to return over MCP — read it in portions instead")
)

func renderMCPSchemas(rows []mcpTool) []json.RawMessage {
	out := make([]json.RawMessage, len(rows))
	for i, t := range rows {
		out[i] = mcpSchema(t)
	}
	return out
}

// mcpEndpoint serves the JSON-RPC-over-HTTP MCP protocol.
func (s *Server) mcpEndpoint(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u == nil { // the guard resolved the caller; defensive
		writeErr(w, http.StatusUnauthorized, "Not authenticated")
		return
	}
	srv := mcp.NewServer(s.mcpTools(), mcp.ServerInfo{Name: mcpServerName, Version: version})
	if err := srv.Validate(); err != nil {
		writeErr(w, http.StatusInternalServerError, "The MCP tool table is unusable: "+err.Error())
		return
	}
	ctx := context.WithValue(r.Context(), mcpCallKey, mcpCallInfo{
		csrfOK:        s.checkCSRF(r),
		cookie:        r.Header.Get("Cookie"),
		authorization: r.Header.Get("Authorization"),
	})
	srv.Handler().ServeHTTP(w, r.WithContext(mcp.WithUser(ctx, *u)))
}

// mcpTools projects the table into protocol tools bound to this server.
func (s *Server) mcpTools() []mcp.Tool {
	out := make([]mcp.Tool, 0, len(mcpRows))
	for i := range mcpRows {
		row, schema := mcpRows[i], mcpRowSchemas[i]
		out = append(out, mcp.Tool{
			Name:        row.Name,
			Description: row.Desc,
			Schema:      schema,
			Annotations: mcp.Annotations{
				ReadOnly:    row.readOnly(),
				Destructive: row.Destructive,
				Idempotent:  row.Method != http.MethodPost,
			},
			Call: func(ctx context.Context, u auth.User, args json.RawMessage) (any, error) {
				return s.mcpInvoke(ctx, row, u, args)
			},
		})
	}
	return out
}

// mcpInvoke authorizes the call, hands it to the route's own handler, and reads the answer back.
func (s *Server) mcpInvoke(ctx context.Context, t mcpTool, u auth.User, args json.RawMessage) (any, error) {
	call, _ := ctx.Value(mcpCallKey).(mcpCallInfo)
	if err := s.mcpAuthorize(t, &u, call); err != nil {
		return nil, err
	}
	req, err := mcpRequest(ctx, t, &u, call, args)
	if err != nil {
		return nil, err
	}
	rec := &mcpRecorder{limit: mcp.MaxMessageBytes}
	t.Handler(s, rec, req)
	return mcpAnswer(rec, req.URL.RequestURI())
}

// mcpAuthorize re-applies the tier of the tool's own route with the SAME predicates the guards
// use: the DevLab right for every tool, the CSRF double-submit for mutating tools on the cookie
// path (a bearer caller cannot be driven by a foreign page, so there the check does not apply),
// and a linked GitHub account where the route demands one.
func (s *Server) mcpAuthorize(t mcpTool, u *auth.User, call mcpCallInfo) error {
	if !u.CanUseDevlab() {
		return errMCPNoRight
	}
	if t.Tier == tierRead {
		return nil
	}
	if !call.csrfOK {
		return errMCPCSRF
	}
	if t.Tier == tierWrite && !s.githubLinked(u) {
		return errors.New(errNoGitHubLink)
	}
	return nil
}

// mcpRequest turns validated arguments into the request the route expects. Path segments are
// escaped, so no argument can widen a call to another route, and the target is always the tool's
// own literal path — the address is never taken from the caller.
func mcpRequest(ctx context.Context, t mcpTool, u *auth.User, call mcpCallInfo, args json.RawMessage) (*http.Request, error) {
	given := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(string(args)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(args, &given); err != nil {
			return nil, errors.New("arguments must be a JSON object")
		}
	}
	path := t.Path
	query := url.Values{}
	body := map[string]json.RawMessage{}
	pathValues := map[string]string{}
	for _, p := range t.Params {
		raw, ok := given[p.Name]
		if !ok {
			continue
		}
		// An explicit null belongs IN a document (it clears a field), but in an address it means
		// nothing — there it is simply absent.
		if isNullArg(raw) && p.In != inBody {
			continue
		}
		switch p.In {
		case inPath:
			v, err := mcpScalar(p, raw)
			if err != nil {
				return nil, err
			}
			pathValues[p.wire()] = v
			path = strings.ReplaceAll(path, "{"+p.wire()+"}", url.PathEscape(v))
		case inQuery:
			v, err := mcpScalar(p, raw)
			if err != nil {
				return nil, err
			}
			query.Set(p.wire(), v)
		case inBody:
			body[p.wire()] = raw
		}
	}
	if strings.ContainsAny(path, "{}") {
		return nil, fmt.Errorf("tool %s is missing a path argument", t.Name)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	withBody := t.Method != http.MethodGet
	if withBody {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("tool %s: arguments could not be encoded: %w", t.Name, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.WithValue(ctx, userCtxKey, u), t.Method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("tool %s: %w", t.Name, err)
	}
	if withBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if call.cookie != "" {
		req.Header.Set("Cookie", call.cookie)
	}
	if call.authorization != "" {
		req.Header.Set("Authorization", call.authorization)
	}
	for name, value := range pathValues {
		req.SetPathValue(name, value)
	}
	return req, nil
}

// mcpScalar renders one argument for a path or query position. Schema validation already fixed
// the type; this only decides how it is written.
func mcpScalar(p mcpParam, raw json.RawMessage) (string, error) {
	switch p.Kind {
	case mcp.KindString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("argument %q must be a string", p.Name)
		}
		return s, nil
	case mcp.KindInteger:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return "", fmt.Errorf("argument %q must be a whole number", p.Name)
		}
		return strconv.FormatInt(n, 10), nil
	case mcp.KindNumber:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return "", fmt.Errorf("argument %q must be a number", p.Name)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	case mcp.KindBoolean:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return "", fmt.Errorf("argument %q must be true or false", p.Name)
		}
		return strconv.FormatBool(b), nil
	default:
		return "", fmt.Errorf("argument %q cannot travel in the address", p.Name)
	}
}

// mcpAnswer reads the recorded response back as the tool's answer. A refusal keeps its own
// wording (the detail the surface would show), so an agent reads the same honest reason a person
// would.
func mcpAnswer(rec *mcpRecorder, uri string) (any, error) {
	if rec.truncated {
		return nil, errMCPTooLarge
	}
	status := rec.Status()
	if status >= 400 {
		return nil, errors.New(mcpDetail(rec, status))
	}
	payload := rec.body.Bytes()
	if len(bytes.TrimSpace(payload)) == 0 {
		return &mcp.Content{Text: "OK"}, nil
	}
	if mcpIsJSON(rec.Header().Get("Content-Type")) {
		return &mcp.Content{JSON: json.RawMessage(payload)}, nil
	}
	return &mcp.Content{Bytes: payload, MIME: rec.Header().Get("Content-Type"), URI: uri}, nil
}

// mcpDetail lifts the {"detail": …} wording out of an error answer, or falls back to the status.
func mcpDetail(rec *mcpRecorder, status int) string {
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.body.Bytes(), &body); err == nil && body.Detail != "" {
		return body.Detail
	}
	if text := strings.TrimSpace(rec.body.String()); text != "" {
		if len(text) > 240 {
			text = text[:240] + "…"
		}
		return text
	}
	return "The request was refused (" + strconv.Itoa(status) + ")"
}

func mcpIsJSON(contentType string) bool {
	base := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	return base == "application/json" || strings.HasSuffix(base, "+json")
}

// mcpRecorder collects one handler's answer in memory, bounded by what a message may carry.
type mcpRecorder struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	limit     int
	truncated bool
}

func (m *mcpRecorder) Header() http.Header {
	if m.header == nil {
		m.header = http.Header{}
	}
	return m.header
}

func (m *mcpRecorder) WriteHeader(code int) {
	if m.status == 0 {
		m.status = code
	}
}

func (m *mcpRecorder) Write(p []byte) (int, error) {
	if m.status == 0 {
		m.status = http.StatusOK
	}
	if m.limit > 0 && m.body.Len()+len(p) > m.limit {
		m.truncated = true
		return 0, errMCPTooLarge
	}
	return m.body.Write(p)
}

// Status is the recorded status, defaulting to 200 for a handler that only wrote a body.
func (m *mcpRecorder) Status() int {
	if m.status == 0 {
		return http.StatusOK
	}
	return m.status
}

// isNullArg reports the JSON null literal.
func isNullArg(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }
