package aigentic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The graveyard client talks to aigentic's owned-store endpoints (grave/*), which back Mercury's
// axiom tree with scheme. As with Run, the caller's Cookie + CSRF are forwarded so aigentic
// resolves the same Holistic identity and enforces its own rights — DevLab holds no store of its
// own. content crosses the wire base64-encoded; callers work with plain bytes.

// GraveRecord is a single stored record: its path and decoded content.
type GraveRecord struct {
	Path    string
	Content []byte
}

// GraveList returns the record paths under a prefix (sorted). A backend that cannot enumerate
// yields aigentic's status (503) as an error.
func GraveList(ctx context.Context, cookieHeader, prefix string) ([]string, int, error) {
	raw, status, err := graveGET(ctx, cookieHeader, "grave/list?prefix="+url.QueryEscape(prefix))
	if err != nil {
		return nil, status, err
	}
	var body struct {
		Refs []string `json:"refs"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, status, fmt.Errorf("aigentic: decode list: %w", err)
	}
	return body.Refs, status, nil
}

// GraveGet reads one record. found is false (with a nil error) when the path holds nothing, so a
// caller can probe existence.
func GraveGet(ctx context.Context, cookieHeader, path string) (data []byte, found bool, status int, err error) {
	raw, status, err := graveGET(ctx, cookieHeader, "grave/get?path="+url.QueryEscape(path))
	if status == http.StatusNotFound {
		return nil, false, status, nil
	}
	if err != nil {
		return nil, false, status, err
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, status, fmt.Errorf("aigentic: decode get: %w", err)
	}
	dec, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		return nil, false, status, fmt.Errorf("aigentic: decode content: %w", err)
	}
	return dec, true, status, nil
}

// GravePut stores a record with a mandatory description. Without overwrite, a put onto an existing
// path is rejected (aigentic returns 409) so a mis-addressed write cannot destroy another record.
func GravePut(ctx context.Context, cookieHeader, csrf, path, description string, content []byte, overwrite bool) (ref string, status int, err error) {
	body, _ := json.Marshal(map[string]any{
		"path":        path,
		"description": description,
		"content":     base64.StdEncoding.EncodeToString(content),
		"overwrite":   overwrite,
	})
	raw, status, err := gravePOST(ctx, cookieHeader, csrf, http.MethodPost, "grave/put", body)
	if err != nil {
		return "", status, err
	}
	var out struct {
		Ref string `json:"ref"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Ref, status, nil
}

// GraveMove re-keys a record. The destination must be free (409 otherwise).
func GraveMove(ctx context.Context, cookieHeader, csrf, from, to string) (int, error) {
	body, _ := json.Marshal(map[string]string{"from": from, "to": to})
	_, status, err := gravePOST(ctx, cookieHeader, csrf, http.MethodPost, "grave/move", body)
	return status, err
}

// GraveDelete removes a record (recursively for a folder). Idempotent.
func GraveDelete(ctx context.Context, cookieHeader, csrf, path string) (int, error) {
	_, status, err := gravePOST(ctx, cookieHeader, csrf, http.MethodDelete, "grave?path="+url.QueryEscape(path), nil)
	return status, err
}

func graveGET(ctx context.Context, cookieHeader, subpath string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL()+"/"+subpath, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	return graveDo(req)
}

func gravePOST(ctx context.Context, cookieHeader, csrf, method, subpath string, body []byte) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL()+"/"+subpath, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	return graveDo(req)
}

func graveDo(req *http.Request) ([]byte, int, error) {
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail := strings.TrimSpace(string(raw))
		if d := parseDetail(raw); d != "" {
			detail = d
		}
		return raw, res.StatusCode, fmt.Errorf("aigentic: %s: %s", res.Status, detail)
	}
	return raw, res.StatusCode, nil
}
