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
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 120 * time.Second}

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
}

// Get proxies an authenticated GET to an aigentic sub-path (e.g. "models") forwarding the caller's
// cookie, and returns the raw JSON body.
func Get(ctx context.Context, cookieHeader, subpath string) ([]byte, int, error) {
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
		return nil, res.StatusCode, fmt.Errorf("aigentic: %s", res.Status)
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

// Result is aigentic's answer (the fields DevLab surfaces).
type Result struct {
	Output string `json:"output"`
	Engine string `json:"engine"`
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	Usage  Usage  `json:"usage"`
}

type envelope struct {
	Header map[string]string `json:"header"`
	Data   json.RawMessage   `json:"data"`
}

// Run posts a request to aigentic, forwarding the caller's Cookie header + CSRF token so aigentic
// authenticates the same user. kind is the router kind ("choose"|"claude-cli"|…). It returns the
// result, aigentic's HTTP status (for error mapping — e.g. 403 = missing right/key), and an error.
func Run(ctx context.Context, cookieHeader, csrf, kind string, req Request) (*Result, int, error) {
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
		return nil, res.StatusCode, fmt.Errorf("aigentic: %s: %s", res.Status, detail)
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
