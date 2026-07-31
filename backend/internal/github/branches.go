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
// (404/403/422 ⇒ Permanent at the classifier; a 404 on a delete-like call is the caller's
// Satisfied).
type StatusError struct {
	Status int
	Msg    string
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
		return &StatusError{Status: res.StatusCode, Msg: err.Error()}
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

// PullState is the maintenance view of one pull request: whether it is open, merged (and
// when), and where its head sits. The list endpoints do not report `merged`, so the delivery
// maintenance reads this single-PR projection.
type PullState struct {
	Number   int
	State    string // "open" | "closed"
	Merged   bool
	MergedAt *time.Time
	HeadRef  string
	HeadSHA  string
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
}

func (p pullWire) toState() PullState {
	return PullState{
		Number: p.Number, State: p.State, Merged: p.Merged, MergedAt: p.MergedAt,
		HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA,
	}
}

// GetPullState fetches one PR's maintenance state (typed errors for faultclass).
func GetPullState(ctx context.Context, token, fullName string, number int) (PullState, error) {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return PullState{}, err
	}
	var p pullWire
	res, gerr := do(ctx, token, fmt.Sprintf("%s/repos/%s/%s/pulls/%d", apiBase, owner, name, number), &p)
	if gerr != nil {
		return PullState{}, typed(res, gerr)
	}
	return p.toState(), nil
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
