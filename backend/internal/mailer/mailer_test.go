package mailer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendPostsInternalSendWithSecretAndPayload(t *testing.T) {
	var gotPath, gotSecret, gotCT string
	var gotBody Message
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
	if gotBody.Username != "alice" || gotBody.Subject != "Daily report" || gotBody.Body != "plain" || gotBody.HTMLBody != "<p>rich</p>" {
		t.Errorf("body = %+v", gotBody)
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
