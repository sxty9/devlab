package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// rewriteTransport sends every client request to the fixture server, whatever host the code
// under test addressed — the fixture-GitHub seam (B-13).
type rewriteTransport struct{ base *url.URL }

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.base.Scheme
	clone.URL.Host = t.base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// withFixture points the package's HTTP client at a fixture server for one test.
func withFixture(t *testing.T, h http.Handler) {
	t.Helper()
	srv := httptest.NewServer(h)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	old := httpClient
	httpClient = &http.Client{Transport: rewriteTransport{base: base}, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		httpClient = old
		srv.Close()
	})
}

// TestDeleteBranch pins the delete call and its typed 404: the ref-delete goes to the exact
// git-refs route, and a branch that is already gone surfaces as *StatusError{404} so the
// caller (deliver.Maintain) can classify it Satisfied.
func TestDeleteBranch(t *testing.T) {
	var deleted []string
	withFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		deleted = append(deleted, r.URL.EscapedPath())
		if r.URL.Path == "/repos/o/x/git/refs/heads/gone" {
			http.Error(w, `{"message":"Reference does not exist"}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	if err := DeleteBranch(context.Background(), "tok", "o/x", "fix/login-abc123"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "/repos/o/x/git/refs/heads/fix%2Flogin-abc123" {
		t.Errorf("delete path = %v, want the escaped git-refs route", deleted)
	}

	err := DeleteBranch(context.Background(), "tok", "o/x", "gone")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusNotFound {
		t.Fatalf("missing branch must surface as *StatusError{404}, got %v", err)
	}

	if err := DeleteBranch(context.Background(), "tok", "bad", "b"); err == nil {
		t.Error("a bad full name must be rejected")
	}
}

// TestGetPullState pins the maintenance projection: state, merged (+time) and the head
// ref/SHA come from the single-PR endpoint (the list endpoints do not report `merged`).
func TestGetPullState(t *testing.T) {
	mergedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	withFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/x/pulls/7" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 7, "state": "closed", "merged": true, "merged_at": mergedAt,
			"head": map[string]any{"ref": "fix/login-abc123", "sha": "cafe1234"},
		})
	}))

	st, err := GetPullState(context.Background(), "tok", "o/x", 7)
	if err != nil {
		t.Fatalf("GetPullState: %v", err)
	}
	if !st.Merged || st.State != "closed" || st.HeadRef != "fix/login-abc123" || st.HeadSHA != "cafe1234" {
		t.Errorf("state = %+v", st)
	}
	if st.MergedAt == nil || !st.MergedAt.Equal(mergedAt) {
		t.Errorf("MergedAt = %v, want %v", st.MergedAt, mergedAt)
	}

	if _, err := GetPullState(context.Background(), "tok", "o/x", 8); err == nil {
		t.Error("a missing PR must error (typed)")
	} else {
		var se *StatusError
		if !errors.As(err, &se) || se.Status != http.StatusNotFound {
			t.Errorf("want *StatusError{404}, got %v", err)
		}
	}
}

// TestListOpenPullHeads pins the origin-status input: every open PR with number, head ref AND
// head SHA (the status is posted on the SHA).
func TestListOpenPullHeads(t *testing.T) {
	withFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/x/pulls" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Errorf("state query = %q, want open", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"number": 5, "state": "open", "head": map[string]any{"ref": "fix/a-1", "sha": "aaa1"}},
			{"number": 6, "state": "open", "head": map[string]any{"ref": "hand-branch", "sha": "bbb2"}},
		})
	}))

	heads, err := ListOpenPullHeads(context.Background(), "tok", "o/x")
	if err != nil {
		t.Fatalf("ListOpenPullHeads: %v", err)
	}
	if len(heads) != 2 || heads[0].Number != 5 || heads[0].HeadSHA != "aaa1" || heads[1].HeadRef != "hand-branch" {
		t.Errorf("heads = %+v", heads)
	}
}

// TestTokenNeverInURL guards the secret discipline (B-13): the token travels in the
// Authorization header only — never in the URL the fixture sees.
func TestTokenNeverInURL(t *testing.T) {
	withFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
			t.Errorf("Authorization = %q, want the bearer token", got)
		}
		if q := r.URL.RawQuery; q != "" && (r.URL.Query().Get("access_token") != "" || r.URL.Query().Get("token") != "") {
			t.Errorf("token must never ride in the URL, got query %q", q)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	_ = DeleteBranch(context.Background(), "s3cret", "o/x", "b")
}
