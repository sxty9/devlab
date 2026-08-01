// Package github is a tiny, dependency-free GitHub REST client (net/http only). DevLab uses it
// to resolve, per user, which repos that user may see and what they may do there — GitHub is the
// single source of truth for repo authorization (DevLab stores no repo ACLs of its own).
//
// Tokens are secrets: they are read from the per-user link store, passed in as arguments, used
// only in the Authorization header, and NEVER logged.
package github

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
	"sync"
	"time"
)

const (
	apiBase    = "https://api.github.com"
	apiVersion = "2022-11-28"
	userAgent  = "devlab"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Viewer is the authenticated GitHub identity behind a token.
type Viewer struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
}

// Repo is one repository visible to a user, with that user's effective permission.
type Repo struct {
	FullName    string   // owner/repo
	Name        string   // repo
	Owner       string   // owner login
	Description string   `json:"description"`
	Language    string   `json:"language"`
	Private     bool     `json:"private"`
	Topics      []string `json:"topics"`
	Permission  string   // pull | push | admin (effective for the viewer)
}

// apiRepo is the wire shape from the GitHub API; we project it onto Repo.
type apiRepo struct {
	FullName    string   `json:"full_name"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Language    string   `json:"language"`
	Private     bool     `json:"private"`
	Topics      []string `json:"topics"`
	Owner       struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions struct {
		Admin    bool `json:"admin"`
		Maintain bool `json:"maintain"`
		Push     bool `json:"push"`
		Triage   bool `json:"triage"`
		Pull     bool `json:"pull"`
	} `json:"permissions"`
}

// permission collapses GitHub's permission booleans into DevLab's three-level model.
func (r apiRepo) permission() string {
	switch {
	case r.Permissions.Admin:
		return "admin"
	case r.Permissions.Maintain, r.Permissions.Push:
		return "push"
	default:
		return "pull"
	}
}

func (r apiRepo) toRepo() Repo {
	return Repo{
		FullName:    r.FullName,
		Name:        r.Name,
		Owner:       r.Owner.Login,
		Description: r.Description,
		Language:    r.Language,
		Private:     r.Private,
		Topics:      r.Topics,
		Permission:  r.permission(),
	}
}

// do issues an authenticated GET and decodes JSON into out. The token is used only here.
func do(ctx context.Context, token, url string, out any) (*http.Response, error) {
	res, _, err := doCond(ctx, token, url, "", out)
	return res, err
}

// doCond is do with an optional conditional-request ETag. A non-empty etag is sent as
// If-None-Match; GitHub then answers 304 Not Modified for an unchanged resource — a response that
// does NOT count against the hourly request budget. On a 304 notModified is true and out is left
// untouched (the caller supplies the last known value). Every response's rate-limit headers are
// recorded (recordRateLimit), so a caller can stop BEFORE the budget is spent instead of
// discovering an empty budget by a rejected request.
func doCond(ctx context.Context, token, url, etag string, out any) (res *http.Response, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err = httpClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	recordRateLimit(res.Header)
	if res.StatusCode == http.StatusNotModified {
		return res, true, nil
	}
	if res.StatusCode == http.StatusUnauthorized {
		return res, false, ErrUnauthorized
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return res, false, fmt.Errorf("github: %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return res, false, fmt.Errorf("github: decode: %w", err)
		}
	}
	return res, false, nil
}

// ── Rate-limit budget (the shared, finite request window) ────────────────────────────────
//
// GitHub returns the remaining budget of the hourly window on EVERY response (X-RateLimit-*). The
// client keeps the last observed reading so a caller — above all the PR maintenance — can stop
// reading before the window is empty, rather than learning it is empty from a rejected request.
// This is process-wide because the budget is per TOKEN, not per call: the runner holds one token.
type RateSnapshot struct {
	Known     bool      // false until a real response was observed (a cold start reads normally)
	Remaining int       // requests left in the current window
	Limit     int       // the window's size (5000 for an authenticated user)
	Reset     time.Time // when the window refills — the moment a standstill ends by itself
}

var (
	rateMu   sync.Mutex
	rateSnap RateSnapshot
)

// recordRateLimit folds one response's rate-limit headers into the shared snapshot. A response
// without the headers (a non-API host, a transport error) leaves the last reading untouched.
func recordRateLimit(h http.Header) {
	rem := h.Get("X-RateLimit-Remaining")
	if rem == "" {
		return
	}
	n, err := strconv.Atoi(rem)
	if err != nil {
		return
	}
	snap := RateSnapshot{Known: true, Remaining: n}
	if l, err := strconv.Atoi(h.Get("X-RateLimit-Limit")); err == nil {
		snap.Limit = l
	}
	if r, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		snap.Reset = time.Unix(r, 0).UTC()
	}
	rateMu.Lock()
	rateSnap = snap
	rateMu.Unlock()
}

// RateLimit returns the last observed request budget of the token's hourly window.
func RateLimit() RateSnapshot {
	rateMu.Lock()
	defer rateMu.Unlock()
	return rateSnap
}

// ── Conditional-request cache (unchanged reads cost nothing) ─────────────────────────────
//
// The maintenance re-reads the same pull requests again and again to notice a merge. With an ETag
// the unchanged ones answer 304 and do not count against the budget, so the cache holds the last
// ETag and the value it belonged to, keyed by request URL.
type condCache struct {
	mu sync.Mutex
	m  map[string]condEntry
}

type condEntry struct {
	etag  string
	state PullState
}

func (c *condCache) get(url string) (etag string, state PullState, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[url]
	return e.etag, e.state, ok
}

func (c *condCache) put(url, etag string, state PullState) {
	if etag == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[url] = condEntry{etag: etag, state: state}
}

var pullConditional = &condCache{m: map[string]condEntry{}}

// ErrUnauthorized means the token was rejected (revoked/expired) — the caller should prompt a relink.
var ErrUnauthorized = errors.New("github: token unauthorized")

// GetViewer returns the identity behind a token (GET /user).
func GetViewer(ctx context.Context, token string) (Viewer, error) {
	var v Viewer
	if _, err := do(ctx, token, apiBase+"/user", &v); err != nil {
		return Viewer{}, err
	}
	return v, nil
}

// ListRepos returns every repo the user owns or collaborates on (paginated, capped). The viewer's
// effective per-repo permission is included inline.
func ListRepos(ctx context.Context, token string) ([]Repo, error) {
	const perPage = 100
	const maxPages = 20 // hard cap: ≤2000 repos
	var repos []Repo
	for page := 1; page <= maxPages; page++ {
		url := apiBase + "/user/repos?affiliation=owner,collaborator,organization_member&per_page=" +
			strconv.Itoa(perPage) + "&page=" + strconv.Itoa(page)
		var batch []apiRepo
		if _, err := do(ctx, token, url, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			repos = append(repos, r.toRepo())
		}
		if len(batch) < perPage {
			break
		}
	}
	return repos, nil
}

// RepoPermission re-resolves the viewer's authoritative permission on a single repo (GET
// /repos/{owner}/{repo}). Used at write time so a stale list cannot grant push. fullName is
// "owner/repo". Returns ErrUnauthorized if the token can't even see the repo.
func RepoPermission(ctx context.Context, token, fullName string) (string, error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("github: bad repo %q", fullName)
	}
	var r apiRepo
	if _, err := do(ctx, token, apiBase+"/repos/"+owner+"/"+name, &r); err != nil {
		return "", err
	}
	return r.permission(), nil
}

// doPost issues a POST with a JSON body and decodes the response — the mutating twin of do().
func doPost(ctx context.Context, token, url string, body, out any) (*http.Response, error) {
	return doMethod(ctx, http.MethodPost, token, url, body, out)
}

// doMethod is doPost generalized over the HTTP method (POST/PUT/…) — one request path, no siblings.
func doMethod(ctx context.Context, method, token, url string, body, out any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	recordRateLimit(res.Header)
	if res.StatusCode == http.StatusUnauthorized {
		return res, ErrUnauthorized
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return res, fmt.Errorf("github: %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return res, fmt.Errorf("github: decode: %w", err)
		}
	}
	return res, nil
}

// PullRequest is the subset of a PR that DevLab surfaces.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"` // only populated by GetPullRequest (single-PR GET), not the list endpoint
	Title   string `json:"title"`
	// Body is the PR description. The list endpoint populates it; Mercury recognises its OWN still-open PRs
	// by a stable hidden marker in the body (the branch name no longer carries a distinguishing prefix).
	Body string `json:"body"`
	// Head is the source branch of the PR. The list endpoint populates it; it is the ref folded into a new
	// run's base once the PR is identified as Mercury's, and the legacy fallback for recognising an in-flight
	// PR from before branches moved to the <kind>/<description> convention.
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// DefaultBranch returns a repo's default branch — the natural PR base.
func DefaultBranch(ctx context.Context, token, fullName string) (string, error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return "", fmt.Errorf("github: bad repo %q", fullName)
	}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := do(ctx, token, apiBase+"/repos/"+owner+"/"+name, &r); err != nil {
		return "", err
	}
	return r.DefaultBranch, nil
}

// CreatePullRequest opens a PR from head → base on fullName (same-repo branches). GitHub 422s when
// a PR for head already exists or the branches are identical; the caller can fall back to lookup.
func CreatePullRequest(ctx context.Context, token, fullName, head, base, title, body string) (PullRequest, error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return PullRequest{}, fmt.Errorf("github: bad repo %q", fullName)
	}
	payload := map[string]any{"title": title, "head": head, "base": base, "body": body}
	var pr PullRequest
	// typed() attaches the HTTP status so faultclass can classify. A 422 here ("No commits between
	// X and X", or a PR for this head already exists) is a construction fault of the request, NOT a
	// transient hiccup: it must be classified Permanent so it earns ONE attempt, never four. Left
	// untyped it would fall through to the Transient default and be retried pointlessly.
	if res, err := doPost(ctx, token, apiBase+"/repos/"+owner+"/"+name+"/pulls", payload, &pr); err != nil {
		return PullRequest{}, typed(res, err)
	}
	return pr, nil
}

// CreateRepo creates a repository under owner and tags it with topic so it joins the holistic set —
// used by a "Konkrete ToDo" that plans a NEW service. Idempotent: an existing repo is returned as-is
// rather than erroring. Returns the full name (owner/name).
func CreateRepo(ctx context.Context, token, owner, name, description, topic string) (string, error) {
	full := owner + "/" + name
	if _, err := DefaultBranch(ctx, token, full); err == nil {
		return full, nil // already there — creating is a no-op
	}
	payload := map[string]any{"name": name, "description": description, "private": true, "auto_init": true}
	var created struct {
		FullName string `json:"full_name"`
	}
	// Prefer the org namespace (where the holistic set lives); fall back to the caller's own namespace.
	if _, err := doPost(ctx, token, apiBase+"/orgs/"+owner+"/repos", payload, &created); err != nil {
		if _, uerr := doPost(ctx, token, apiBase+"/user/repos", payload, &created); uerr != nil {
			return "", fmt.Errorf("github: create %s: %w", full, err)
		}
	}
	if created.FullName == "" {
		created.FullName = full
	}
	if topic != "" {
		if err := SetTopics(ctx, token, created.FullName, []string{topic}); err != nil {
			return created.FullName, err // the repo exists; it just isn't in the set yet
		}
	}
	return created.FullName, nil
}

// SetTopics replaces a repo's topics (holistic membership is owner + topic).
func SetTopics(ctx context.Context, token, fullName string, topics []string) error {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("github: bad repo %q", fullName)
	}
	_, err := doMethod(ctx, http.MethodPut, token, apiBase+"/repos/"+owner+"/"+name+"/topics",
		map[string]any{"names": topics}, nil)
	return err
}

// GetPullRequest fetches a single PR, including whether it has been merged (the list/search
// endpoints do not report `merged`, so the auto-merge check needs this).
func GetPullRequest(ctx context.Context, token, fullName string, number int) (PullRequest, error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return PullRequest{}, fmt.Errorf("github: bad repo %q", fullName)
	}
	var pr PullRequest
	if _, err := do(ctx, token, fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, name, number), &pr); err != nil {
		return PullRequest{}, err
	}
	return pr, nil
}

// MergePullRequest merges a PR (merge commit). It returns nil only when GitHub confirms the merge —
// a non-mergeable PR (conflicts, required checks) surfaces as an error, never a silent no-op.
func MergePullRequest(ctx context.Context, token, fullName string, number int) error {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("github: bad repo %q", fullName)
	}
	var res struct {
		Merged bool `json:"merged"`
	}
	if _, err := doMethod(ctx, http.MethodPut, token, fmt.Sprintf("%s/repos/%s/%s/pulls/%d/merge", apiBase, owner, name, number), map[string]any{"merge_method": "merge"}, &res); err != nil {
		return err
	}
	if !res.Merged {
		return fmt.Errorf("github: PR %d wurde nicht gemergt", number)
	}
	return nil
}

// ClosePullRequest closes a PR without merging it (PATCH state=closed) — used when a delivery is rolled
// back while its PR is still open (req 11): the work is withdrawn, so the PR is closed with a prior
// justification comment (see AddPullRequestComment).
func ClosePullRequest(ctx context.Context, token, fullName string, number int) error {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("github: bad repo %q", fullName)
	}
	_, err := doMethod(ctx, http.MethodPatch, token, fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, name, number),
		map[string]any{"state": "closed"}, nil)
	return err
}

// AddPullRequestComment posts a comment on a PR (via the issues endpoint — a PR is an issue). Used to
// record the justification for closing a rolled-back PR before it is closed.
func AddPullRequestComment(ctx context.Context, token, fullName string, number int, body string) error {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("github: bad repo %q", fullName)
	}
	_, err := doPost(ctx, token, fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", apiBase, owner, name, number),
		map[string]any{"body": body}, nil)
	return err
}

// ListOpenPullRequests returns the open PRs on fullName (newest first, first page — up to 100).
// Each PR carries its Body and Head.Ref, so the caller can tell a Mercury PR (hidden body marker, with a
// legacy branch-prefix fallback) from a human one. Used by the dedup guard: a repo whose Mercury work is
// still an open PR must not be re-implemented into a duplicate PR — the un-merged PR is treated as already
// productive; its head is folded into the new run's base.
func ListOpenPullRequests(ctx context.Context, token, fullName string) ([]PullRequest, error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("github: bad repo %q", fullName)
	}
	q := url.Values{"state": {"open"}, "per_page": {"100"}, "sort": {"created"}, "direction": {"desc"}}
	var prs []PullRequest
	if _, err := do(ctx, token, apiBase+"/repos/"+owner+"/"+name+"/pulls?"+q.Encode(), &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

// FindOpenPullRequest returns the open PR whose head is `head` on fullName, if one exists (so the
// "Open PR" action is idempotent — it focuses an existing PR instead of erroring on a duplicate).
func FindOpenPullRequest(ctx context.Context, token, fullName, head string) (PullRequest, bool) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return PullRequest{}, false
	}
	q := url.Values{"head": {owner + ":" + head}, "state": {"open"}, "per_page": {"1"}}
	var prs []PullRequest
	if _, err := do(ctx, token, apiBase+"/repos/"+owner+"/"+name+"/pulls?"+q.Encode(), &prs); err != nil {
		return PullRequest{}, false
	}
	if len(prs) == 0 {
		return PullRequest{}, false
	}
	return prs[0], true
}
