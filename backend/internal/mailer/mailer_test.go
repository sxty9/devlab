package mailer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The request must carry maild's OWN field names, not this package's. The previous version of this
// test decoded the body into the client's own Message struct — so it agreed with whatever the client
// happened to send and would have stayed green through any contract, which is exactly how a mailer
// that maild refused with "A from user is required" shipped and never delivered a single report.
// It therefore reads the RAW keys.
func TestSendPostsInternalSendWithSecretAndPayload(t *testing.T) {
	t.Setenv("HOLISTIC_MAIL_DOMAIN", "example.test")
	var gotPath, gotSecret, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSecret = r.Header.Get("X-Mail-Internal-Secret")
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := NewWithSecret(srv.URL+"/api/services/mail", "s3cr3t")
	err := c.Send(context.Background(), Message{
		Username: "alice", Subject: "Daily report", Body: "plain", HTMLBody: "<p>rich</p>",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPath != "/api/services/mail/internal/send" {
		t.Errorf("path = %q, want /api/services/mail/internal/send", gotPath)
	}
	if gotSecret != "s3cr3t" {
		t.Errorf("secret header = %q, want s3cr3t", gotSecret)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	// The sender maild insists on, and the recipient as an ADDRESS.
	if gotBody["from"] != "alice" {
		t.Errorf(`from = %v, want "alice" — maild refuses a send without one`, gotBody["from"])
	}
	to, _ := gotBody["to"].([]any)
	if len(to) != 1 || to[0] != "alice@example.test" {
		t.Errorf("to = %v, want [alice@example.test]", gotBody["to"])
	}
	if gotBody["subject"] != "Daily report" || gotBody["body"] != "plain" {
		t.Errorf("subject/body = %v / %v", gotBody["subject"], gotBody["body"])
	}
	// The key the old client sent instead of a sender must be gone — otherwise both shapes are on
	// the wire and the fix is only half done.
	if _, stale := gotBody["username"]; stale {
		t.Errorf("the request still carries the shape maild has never had: %v", gotBody)
	}
}

// Without a known domain the bare username is handed over — DevLab does not invent a domain to make
// the send look successful.
func TestWithoutAKnownDomainTheBareUsernameIsHandedOver(t *testing.T) {
	t.Setenv("HOLISTIC_MAIL_DOMAIN", "")
	t.Setenv("HOLISTIC_INSTANCE", filepath.Join(t.TempDir(), "absent.json"))
	if got := Address("alice"); got != "alice" {
		t.Fatalf("Address = %q, want the bare username", got)
	}
}

// The domain comes from the file maild itself reads — one domain in the landscape, not a second one.
func TestTheDomainComesFromTheInstanceFileMaildReads(t *testing.T) {
	t.Setenv("HOLISTIC_MAIL_DOMAIN", "")
	path := filepath.Join(t.TempDir(), "instance.json")
	if err := os.WriteFile(path, []byte(`{"mail_domain": "landscape.test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOLISTIC_INSTANCE", path)
	if got := Address("alice"); got != "alice@landscape.test" {
		t.Fatalf("Address = %q, want alice@landscape.test", got)
	}
}

func TestSendSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Not authenticated"}`))
	}))
	defer srv.Close()

	c := NewWithSecret(srv.URL, "wrong")
	err := c.Send(context.Background(), Message{Username: "alice", Subject: "s", Body: "b"})
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	if want := "Not authenticated"; !contains(err.Error(), want) {
		t.Errorf("error %q should mention %q", err.Error(), want)
	}
}

func TestSendRejectsEmptyRecipient(t *testing.T) {
	c := NewWithSecret("http://127.0.0.1:0", "s")
	if err := c.Send(context.Background(), Message{Subject: "s", Body: "b"}); err == nil {
		t.Fatal("expected an error for empty recipient")
	}
}

func TestNewFailsWithoutSecret(t *testing.T) {
	t.Setenv("DEVLAB_MAIL_INTERNAL_SECRET", "")
	t.Setenv("DEVLAB_MAIL_INTERNAL_SECRET_FILE", "/nonexistent/definitely/missing")
	if _, err := New(); err == nil {
		t.Fatal("New should return ErrNoSecret when no secret is configured")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
