package aigentic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A call's bound belongs to the WORK, not to the transport. The client used to carry ONE fixed
// 120 s timeout, so a detached job that may legitimately think for minutes was cut off mid-answer
// and its healthy-but-slow plan arrived as a transport error.
func TestACallerWithItsOwnDeadlineKeepsIt(t *testing.T) {
	own := time.Now().Add(30 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), own)
	defer cancel()

	bounded, release := withDeadline(ctx)
	defer release()
	got, ok := bounded.Deadline()
	if !ok {
		t.Fatal("the caller's deadline was dropped")
	}
	if !got.Equal(own) {
		t.Fatalf("deadline %v, want the caller's own %v — a detached job must not be cut at the transport default", got, own)
	}
}

// A caller without a deadline still gets one: no call may hang forever.
func TestACallWithoutADeadlineIsStillBounded(t *testing.T) {
	bounded, release := withDeadline(context.Background())
	defer release()
	got, ok := bounded.Deadline()
	if !ok {
		t.Fatal("a call without its own deadline must still be bounded")
	}
	if left := time.Until(got); left <= 0 || left > CallTimeout+time.Second {
		t.Fatalf("bound %v, want about %v", left, CallTimeout)
	}
}

// A non-2xx answer is a TYPE, so a caller can name the failure — and its message stays the wording
// the client has always produced.
func TestStatusErrorCarriesTheStatusAndKeepsItsWording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"no AI right"}`))
	}))
	defer srv.Close()
	t.Setenv("DEVLAB_AIGENTIC_URL", srv.URL)

	_, status, err := Run(context.Background(), "", "", "claude-cli", Request{Prompt: "x"})
	if status != http.StatusForbidden || err == nil {
		t.Fatalf("status %d err %v, want 403 with an error", status, err)
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %T is not a *StatusError — a caller cannot name what went wrong", err)
	}
	if se.Status != http.StatusForbidden || se.Detail != "no AI right" {
		t.Errorf("status error = %+v, want 403 with aigentic's own detail", se)
	}
	if !strings.HasPrefix(err.Error(), "aigentic: 403 Forbidden: ") {
		t.Errorf("message %q lost the wording the client has always produced", err)
	}
}

// Reason turns a failure into a sentence the user can act on — and stays silent about failures it
// does not know, so a caller does not present a guess as a diagnosis.
func TestReasonNamesWhatItKnowsAndNothingElse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string // a fragment the reason must carry; "" = no reason at all
	}{
		{"nothing failed", nil, ""},
		{"cancelled", context.Canceled, "cancelled"},
		{"no answer in time", context.DeadlineExceeded, "did not answer"},
		{"right or key missing", &StatusError{Status: 403, Text: "403 Forbidden"}, "aigentic"},
		{"session refused", &StatusError{Status: 401, Text: "401 Unauthorized"}, "sign in"},
		{"usage window", &StatusError{Status: 429, Text: "429 Too Many Requests"}, "usage limit"},
		{"engine missing", &StatusError{Status: 503, Text: "503 Service Unavailable"}, "unavailable"},
		{"service error", &StatusError{Status: 500, Text: "500 Internal Server Error"}, "reported an error"},
		{"not reachable", &url.Error{Op: "Post", Err: errors.New("connection refused")}, "not reachable"},
		{"unusable answer", errors.New("run plan: no JSON object"), ""},
	}
	for _, c := range cases {
		got := Reason(c.err)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: reason %q, want none (the caller names its own failure modes)", c.name, got)
			}
			continue
		}
		if !strings.Contains(strings.ToLower(got), strings.ToLower(c.want)) {
			t.Errorf("%s: reason %q does not say %q", c.name, got, c.want)
		}
		if strings.EqualFold(strings.TrimSpace(got), "failed") {
			t.Errorf("%s: %q is not a named reason", c.name, got)
		}
	}
}
