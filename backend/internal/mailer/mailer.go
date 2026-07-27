// Package mailer is a thin loopback client to the holistic `mail` service (maild). DevLab does NOT
// run its own SMTP and holds no mail account of its own: it hands maild a Holistic username and a
// message, and maild's local delivery agent resolves that user's mailbox from their landscape
// identity and delivers it. There is thus exactly one delivery path in the landscape (no second
// delivery truth), and DevLab configures no recipient address — it names the user, maild owns the
// address.
//
// The internal-send endpoint is authenticated by the shared landscape internal secret (the same file
// maild reads via MAILD_INTERNAL_SECRET_FILE), presented in the X-Mail-Internal-Secret header. It is
// reachable only over loopback (behind Holistic's Caddy in prod, but DevLab reaches the daemon
// directly). Mirrors the aigentic client's shape on purpose — one established pattern for calling a
// landscape service, not a new one per integration.
package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrNoSecret means no internal secret was configured/readable, so the client cannot authenticate to
// maild. It is returned by New so the caller can fail-soft (log a skip) rather than crash — a mail
// misconfiguration must never take devlabd down.
var ErrNoSecret = errors.New("mailer: no internal secret configured")

// Client posts messages to maild's internal LDA endpoint. Safe for concurrent use.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

// baseURL is maild's HTTP prefix on the loopback. Override with DEVLAB_MAIL_URL.
func baseURL() string {
	if u := os.Getenv("DEVLAB_MAIL_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://127.0.0.1:8775/api/services/mail"
}

// loadSecret reads the internal secret: DEVLAB_MAIL_INTERNAL_SECRET (raw, mainly for tests) wins,
// else the file at DEVLAB_MAIL_INTERNAL_SECRET_FILE, else the landscape default
// /etc/holistic/icaly-mail-secret (the same secret maild trusts for internal sends). A landscape
// service running in the `holistic` group can read that file; a powerless account cannot, which is
// the intended fail-closed.
func loadSecret() string {
	if s := strings.TrimSpace(os.Getenv("DEVLAB_MAIL_INTERNAL_SECRET")); s != "" {
		return s
	}
	path := os.Getenv("DEVLAB_MAIL_INTERNAL_SECRET_FILE")
	if path == "" {
		path = "/etc/holistic/icaly-mail-secret"
	}
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// New builds a client from the environment. It returns ErrNoSecret when no secret is available so the
// caller decides how to degrade (the daily-report reporter records a visible send failure and retries).
func New() (*Client, error) {
	secret := loadSecret()
	if secret == "" {
		return nil, ErrNoSecret
	}
	return NewWithSecret(baseURL(), secret), nil
}

// NewWithSecret builds a client with an explicit base URL and secret (used by tests and by callers
// that resolve configuration themselves).
func NewWithSecret(base, secret string) *Client {
	return &Client{
		baseURL: strings.TrimRight(base, "/"),
		secret:  secret,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Message is one internal send: a Holistic user's mailbox (by username — maild resolves the address
// from their identity) plus a subject and both plaintext and HTML bodies.
type Message struct {
	Username string `json:"username"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	HTMLBody string `json:"htmlBody,omitempty"`
}

// Send delivers a message to the named user's mailbox via maild. It returns a non-nil error on any
// transport failure or non-2xx status (with maild's {"detail":...} reason when present), so the
// caller can surface and retry a failed send without losing it.
func (c *Client) Send(ctx context.Context, m Message) error {
	if strings.TrimSpace(m.Username) == "" {
		return errors.New("mailer: empty recipient username")
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Mail-Internal-Secret", c.secret)
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail := strings.TrimSpace(string(raw))
		if d := parseDetail(raw); d != "" {
			detail = d
		}
		return fmt.Errorf("mailer: %s: %s", res.Status, detail)
	}
	return nil
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
