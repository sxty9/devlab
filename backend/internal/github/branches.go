// Branch operations (C F14 02e6c75). DeleteBranch is called by deliver.Maintain — the SAME
// place that merges also prunes the delivery branch; a 404 (branch already gone) is classified
// Satisfied by faultclass.
package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// StatusError is a typed GitHub API failure — the classification input for faultclass
// (404/422 ⇒ Permanent at the classifier; a 404 on a delete-like call is the caller's
// Satisfied). RetryAfter carries GitHub's Retry-After header when present: it is the signal
// that a 403 is the SECONDARY (abuse) rate limit — a self-ending obstacle — rather than a
// missing right, so faultclass can tell the two 403 forms apart.
type StatusError struct {
	Status     int
	Msg        string
	RetryAfter string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: status %d: %s", e.Status, e.Msg)
}

// typed wraps a client error into *StatusError when an HTTP status is known, so callers can
// classify (faultclass; 404-Satisfied at delete-like call sites). A transport-level error (no
// response) passes through untyped — that is the connectivity signature.
func typed(res *http.Response, err error) error {
	if err == nil {
		return nil
	}
	if res != nil && res.StatusCode >= 400 {
		return &StatusError{Status: res.StatusCode, Msg: err.Error(), RetryAfter: res.Header.Get("Retry-After")}
	}
	return err
}

// splitFullName splits "owner/repo" with the same validation the rest of the client applies.
func splitFullName(fullName string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("github: bad repo %q", fullName)
	}
	return owner, name, nil
}

// DeleteBranch deletes a branch ref. A missing branch surfaces as *StatusError{Status: 404}.
func DeleteBranch(ctx context.Context, token, fullName, branch string) error {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return err
	}
	res, derr := doMethod(ctx, http.MethodDelete, token,
		fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", apiBase, owner, name, url.PathEscape(branch)), nil, nil)
	return typed(res, derr)
}

// CreateBranch creates a branch ref at sha. It is the RESTORE half of the stacked-PR heal: a pull
// request whose base branch was deleted under it cannot be reopened until that base branch exists
// again, so the chain recreates it before reopening. A branch that already exists surfaces as
// *StatusError{Status: 422} "Reference already exists" — Satisfied at the caller (isBranchExists).
func CreateBranch(ctx context.Context, token, fullName, branch, sha string) error {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return err
	}
	res, cerr := doPost(ctx, token, fmt.Sprintf("%s/repos/%s/%s/git/refs", apiBase, owner, name),
		map[string]any{"ref": "refs/heads/" + branch, "sha": sha}, nil)
	return typed(res, cerr)
}

// ReopenPullRequest reopens a pull request GitHub (or a person) closed without merging (PATCH
// state=open). GitHub refuses the reopen when the pull request's base branch is gone, so the caller
// recreates the base branch first (CreateBranch). Typed errors so faultclass can classify.
func ReopenPullRequest(ctx context.Context, token, fullName string, number int) error {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return err
	}
	res, perr := doMethod(ctx, http.MethodPatch, token,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, name, number),
		map[string]any{"state": "open"}, nil)
	return typed(res, perr)
}

// RetargetPullRequest re-homes an open pull request onto a new base branch (PATCH base). It is the
// "umhaengen vor loeschen" primitive: before a merged delivery's branch is pruned, every open pull
// request stacked on it is moved onto the default branch, because GitHub does NOT reliably re-home a
// stacked pull request when its base branch is deleted — it CLOSES the successor (measured 2026-08-04).
// Typed errors so faultclass can classify.
func RetargetPullRequest(ctx context.Context, token, fullName string, number int, base string) error {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return err
	}
	res, perr := doMethod(ctx, http.MethodPatch, token,
		fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, name, number),
		map[string]any{"base": base}, nil)
	return typed(res, perr)
}

// PullState is the maintenance view of one pull request: whether it is open, merged (and
// when), where its head sits, AND which branch it merges INTO (BaseRef). The list endpoints do
// not report `merged`, so the delivery maintenance reads this single-PR projection. BaseRef is the
// guard that a delivery reached the DEFAULT branch: a pull request merged into any other branch —
// a stale stacked base — never reaches main, so its content did not ship however the ledger might
// otherwise read.
type PullState struct {
	Number   int
	State    string // "open" | "closed"
	Merged   bool
	MergedAt *time.Time
	HeadRef  string
	HeadSHA  string
	BaseRef  string // the branch this pull request merges INTO
}

// pullWire is the wire subset PullState is decoded from.
type pullWire struct {
	Number   int        `json:"number"`
	State    string     `json:"state"`
	Merged   bool       `json:"merged"`
	MergedAt *time.Time `json:"merged_at"`
	Head     struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p pullWire) toState() PullState {
	return PullState{
		Number: p.Number, State: p.State, Merged: p.Merged, MergedAt: p.MergedAt,
		HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA, BaseRef: p.Base.Ref,
	}
}

// GetPullState fetches one PR's maintenance state (typed errors for faultclass). It is CONDITIONAL:
// the last ETag is replayed as If-None-Match, so an unchanged pull request answers 304 and the read
// costs nothing against the request budget — the maintenance re-reads the same PRs on every pass, and
// most of those reads find nothing changed.
func GetPullState(ctx context.Context, token, fullName string, number int) (PullState, error) {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return PullState{}, err
	}
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, name, number)
	etag, cached, hasCache := pullConditional.get(url)
	var p pullWire
	res, notModified, gerr := doCond(ctx, token, url, etag, &p)
	if gerr != nil {
		return PullState{}, typed(res, gerr)
	}
	if notModified && hasCache {
		return cached, nil
	}
	st := p.toState()
	pullConditional.put(url, res.Header.Get("ETag"), st)
	return st, nil
}

// ListOpenPullHeads returns every OPEN pull request on fullName with its head ref AND head SHA
// (newest first, first page — up to 100). The origin-status pass posts the delivery-origin
// status on exactly these heads, a hand-raised PR included.
func ListOpenPullHeads(ctx context.Context, token, fullName string) ([]PullState, error) {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return nil, err
	}
	q := url.Values{"state": {"open"}, "per_page": {"100"}, "sort": {"created"}, "direction": {"desc"}}
	var wire []pullWire
	res, lerr := do(ctx, token, apiBase+"/repos/"+owner+"/"+name+"/pulls?"+q.Encode(), &wire)
	if lerr != nil {
		return nil, typed(res, lerr)
	}
	out := make([]PullState, 0, len(wire))
	for _, p := range wire {
		st := p.toState()
		st.State = "open"
		out = append(out, st)
	}
	return out, nil
}
