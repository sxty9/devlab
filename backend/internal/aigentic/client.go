// Package aigentic is a thin loopback client to the holistic `aigentic` AI service. DevLab does
// NOT run its own LLM — it proxies to aigentic's single /run endpoint, forwarding the caller's
// Holistic session cookie so aigentic resolves the same identity, bills the user's own Anthropic
// key/Claude subscription, and enforces its own hp_aigentic_* rights. Repo context is supplied as
// `inline` bytes (aigentic's sandboxed daemon cannot read DevLab's workspaces). No streaming.
package aigentic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/model"
)

// CallTimeout is the deadline a call gets when its CALLER set none. Every call stays bounded — a
// request handler that hangs on the model would hold a browser connection open for as long as the
// model thinks — but the bound now belongs to the WORK, not to the transport: a caller that carries
// its own deadline (a detached job, which holds no connection open) keeps it. The old fixed
// client-level 120 s cut such a job off mid-answer and turned a slow-but-healthy plan into a
// transport error.
const CallTimeout = 120 * time.Second

// httpClient deliberately carries NO client-level timeout: every call below derives its deadline
// from the context (see withDeadline), so a bound exists for each one without capping the callers
// that legitimately run longer.
var httpClient = &http.Client{}

// withDeadline bounds a call that brought no deadline of its own.
func withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, CallTimeout)
}

// StatusError is a non-2xx answer from aigentic: the status plus the service's own detail. It is a
// TYPE, not a formatted string, so a caller can NAME the failure (Reason) instead of showing the
// user a transport line. Its message is the wording the client has always produced.
type StatusError struct {
	Status int    // the HTTP status code (403 = right/key missing, 429 = usage window, …)
	Text   string // the status line as received, e.g. "403 Forbidden"
	Detail string // aigentic's own detail, when it sent one
}

func (e *StatusError) Error() string {
	if e.Detail == "" {
		return "aigentic: " + e.Text
	}
	return "aigentic: " + e.Text + ": " + e.Detail
}

// Reason names WHY a call failed, in words the user can act on — never a bare "failed", never a raw
// transport line. It answers "" for a failure it does not recognise (a caller that knows its own
// failure modes — an unusable answer, say — names those itself).
func Reason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "The AI request was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "The AI service did not answer within " + CallTimeout.String()
	}
	var se *StatusError
	if errors.As(err, &se) {
		switch {
		case se.Status == http.StatusForbidden:
			return "AI access is missing: grant hp_aigentic_run and link a Claude key or subscription in aigentic"
		case se.Status == http.StatusUnauthorized:
			return "The AI service did not accept this session — sign in again"
		case se.Status == http.StatusTooManyRequests:
			return "The AI usage limit is reached — the model answers again once the window resets"
		case se.Status == http.StatusServiceUnavailable:
			return "The AI engine is unavailable — link an Anthropic key or Claude subscription in aigentic"
		case se.Status >= 500:
			return "The AI service reported an error (" + strconv.Itoa(se.Status) + ")"
		}
		return "The AI service refused the request (" + se.Text + ")"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return "The AI service is not reachable"
	}
	return ""
}

// baseURL is aigentic's HTTP prefix on the loopback (behind Holistic's Caddy in prod, but DevLab
// reaches the daemon directly). Override with DEVLAB_AIGENTIC_URL.
func baseURL() string {
	if u := os.Getenv("DEVLAB_AIGENTIC_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://127.0.0.1:8780/api/services/aigentic"
}

// InlineFile is a context file supplied by value (text or base64) with its media type.
type InlineFile struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	MediaType string `json:"mediaType,omitempty"`
}

// ClaudeOpts carries claude-engine tuning (reasoning effort).
type ClaudeOpts struct {
	Effort string `json:"effort,omitempty"`
}

// Request is aigentic's per-run data payload (the fields DevLab uses).
type Request struct {
	Prompt       string       `json:"prompt"`
	Inline       []InlineFile `json:"inline,omitempty"`
	OutputFormat string       `json:"outputFormat,omitempty"`
	Model        string       `json:"model,omitempty"`
	Claude       *ClaudeOpts  `json:"claude,omitempty"`
	// Interactive lets the model reply with a structured multiple-choice question (à la Claude Code),
	// surfaced on Result.Ask and rendered as clickable options in the chat bubble.
	Interactive bool `json:"interactive,omitempty"`
}

// Get proxies an authenticated GET to an aigentic sub-path (e.g. "models") forwarding the caller's
// cookie, and returns the raw JSON body.
func Get(ctx context.Context, cookieHeader, subpath string) ([]byte, int, error) {
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL()+"/"+strings.TrimPrefix(subpath, "/"), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, res.StatusCode, &StatusError{Status: res.StatusCode, Text: res.Status}
	}
	return raw, res.StatusCode, nil
}

// Usage is the token accounting aigentic returns.
type Usage struct {
	InputTokens  int  `json:"inputTokens"`
	OutputTokens int  `json:"outputTokens"`
	TotalTokens  int  `json:"totalTokens"`
	Truncated    bool `json:"truncated"`
}

// Result is aigentic's answer (the fields DevLab surfaces). Ask is aigentic's structured-question
// shape; DevLab owns that DTO in the model package (surfaced verbatim to the SPA), so the client
// decodes straight into it — one definition, no local mirror to keep in sync.
type Result struct {
	Output string       `json:"output"`
	Engine string       `json:"engine"`
	Model  string       `json:"model"`
	Effort string       `json:"effort,omitempty"`
	Usage  Usage        `json:"usage"`
	Ask    *model.AiAsk `json:"ask,omitempty"`
}

type envelope struct {
	Header map[string]string `json:"header"`
	Data   json.RawMessage   `json:"data"`
}

// Run posts a request to aigentic, forwarding the caller's Cookie header + CSRF token so aigentic
// authenticates the same user. kind is the router kind ("choose"|"claude-cli"|…). It returns the
// result, aigentic's HTTP status (for error mapping — e.g. 403 = missing right/key), and an error.
func Run(ctx context.Context, cookieHeader, csrf, kind string, req Request) (*Result, int, error) {
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	data, err := json.Marshal(req)
	if err != nil {
		return nil, 0, err
	}
	body, err := json.Marshal(envelope{Header: map[string]string{"kind": kind}, Data: data})
	if err != nil {
		return nil, 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL()+"/run", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if cookieHeader != "" {
		httpReq.Header.Set("Cookie", cookieHeader)
	}
	if csrf != "" {
		httpReq.Header.Set("X-CSRF-Token", csrf)
	}
	res, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail := strings.TrimSpace(string(raw))
		if d := parseDetail(raw); d != "" {
			detail = d
		}
		return nil, res.StatusCode, &StatusError{Status: res.StatusCode, Text: res.Status, Detail: detail}
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, res.StatusCode, fmt.Errorf("aigentic: decode envelope: %w", err)
	}
	var result Result
	if err := json.Unmarshal(env.Data, &result); err != nil {
		return nil, res.StatusCode, fmt.Errorf("aigentic: decode result: %w", err)
	}
	return &result, res.StatusCode, nil
}

func parseDetail(b []byte) string {
	var e struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(b, &e) == nil {
		return e.Detail
	}
	return ""
}
