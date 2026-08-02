package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Emit assembles notifyd's own request shape, authenticates with the internal secret in the header
// notifyd reads, and posts to the internal emit path — the fields DevLab used to lack when it never
// spoke at all.
func TestEmitPostsNotifydShape(t *testing.T) {
	var got wireEvent
	var gotSecret, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSecret = r.Header.Get("X-Notify-Internal-Secret")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cl := NewWithSecret(srv.URL+"/api/services/notify", "s3cr3t")
	err := cl.Emit(context.Background(), Event{
		User: "ada", Service: "devlab", Title: "Delivery blocked — o/x",
		Body: "pull request #7 is blocked", URL: "https://d/#/mercury", Level: LevelWarning, Dedupe: "ntc_1",
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if gotPath != "/api/services/notify/internal/emit" {
		t.Errorf("path = %q, want the internal emit endpoint", gotPath)
	}
	if gotSecret != "s3cr3t" {
		t.Errorf("secret header = %q, want the configured secret", gotSecret)
	}
	if got.User != "ada" || got.Title != "Delivery blocked — o/x" || got.Level != "warning" || got.Dedupe != "ntc_1" {
		t.Errorf("wire shape mismatch: %+v", got)
	}
}

// A non-2xx answer is surfaced with notifyd's own reason, so a failed emit is never mistaken for a
// delivery.
func TestEmitSurfacesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"A title is required"}`))
	}))
	defer srv.Close()

	cl := NewWithSecret(srv.URL, "s")
	err := cl.Emit(context.Background(), Event{User: "ada", Title: "x"})
	if err == nil {
		t.Fatal("a 400 must surface as an error, not a silent success")
	}
}

// The recipient and the title are the two fields notifyd insists on; the client refuses to send
// without them rather than posting a request notifyd would reject.
func TestEmitRequiresUserAndTitle(t *testing.T) {
	cl := NewWithSecret("http://127.0.0.1:0", "s")
	if err := cl.Emit(context.Background(), Event{Title: "x"}); err == nil {
		t.Error("an empty user must be refused")
	}
	if err := cl.Emit(context.Background(), Event{User: "ada"}); err == nil {
		t.Error("an empty title must be refused")
	}
}

// New fails soft with ErrNoSecret when nothing is readable, so the caller degrades to a logged skip
// instead of crashing devlabd over a mail-style misconfiguration.
func TestNewWithoutSecret(t *testing.T) {
	t.Setenv("DEVLAB_NOTIFY_INTERNAL_SECRET", "")
	t.Setenv("DEVLAB_NOTIFY_INTERNAL_SECRET_FILE", "/nonexistent/notify-secret-xyz")
	if _, err := New(); err != ErrNoSecret {
		t.Errorf("want ErrNoSecret, got %v", err)
	}
}
