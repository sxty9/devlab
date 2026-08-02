// Package notify is a thin loopback client to the holistic `notify` service (notifyd). It is the
// ONE outward voice DevLab uses to reach a user: DevLab does NOT run its own push, mail or socket to
// the browser — it hands a finding to notify, and notify decides how it reaches the user (a live
// dashboard badge, a push, whatever the user has). There is thus exactly one delivery path in the
// landscape and no second delivery truth built beside it.
//
// The endpoint is notify's INTERNAL emit (POST /api/services/notify/internal/emit), authenticated by
// the shared landscape internal secret (the same file notifyd reads via NOTIFY_INTERNAL_SECRET_FILE),
// presented in the X-Notify-Internal-Secret header. It is reachable only over loopback (behind
// Holistic's Caddy in prod, but DevLab reaches the daemon directly). This mirrors the mailer client's
// shape on purpose — one established pattern for calling a landscape service, not a new one per
// integration (Reuse before Build).
package notify

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
// notifyd. New returns it so the caller can fail-soft (log a skip) rather than crash — a notify
// misconfiguration must never take devlabd down, and never blocks the write that recorded the notice.
var ErrNoSecret = errors.New("notify: no internal secret configured")

// Levels notifyd understands. A disturbance is delivered as a warning; the others are named so a
// caller never invents an unknown level notifyd would reject.
const (
	LevelInfo     = "info"
	LevelSuccess  = "success"
	LevelWarning  = "warning"
	LevelCritical = "critical"
)

// Client posts one notification to notifyd's internal emit endpoint. Safe for concurrent use.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

// baseURL is notifyd's HTTP prefix on the loopback. Override with DEVLAB_NOTIFY_URL (mainly tests).
func baseURL() string {
	if u := strings.TrimSpace(os.Getenv("DEVLAB_NOTIFY_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://127.0.0.1:8778/api/services/notify"
}

// loadSecret reads the internal secret: DEVLAB_NOTIFY_INTERNAL_SECRET (raw, mainly for tests) wins,
// else the file at DEVLAB_NOTIFY_INTERNAL_SECRET_FILE, else the landscape default
// /etc/holistic/notify-secret (the same secret notifyd trusts for internal emits). A landscape
// service running in the `holistic` group can read that file; a powerless account cannot, which is
// the intended fail-closed.
func loadSecret() string {
	if s := strings.TrimSpace(os.Getenv("DEVLAB_NOTIFY_INTERNAL_SECRET")); s != "" {
		return s
	}
	path := strings.TrimSpace(os.Getenv("DEVLAB_NOTIFY_INTERNAL_SECRET_FILE"))
	if path == "" {
		path = "/etc/holistic/notify-secret"
	}
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// New builds a client from the environment. It returns ErrNoSecret when no secret is available so the
// caller decides how to degrade (the notice dispatcher records a logged skip and moves on).
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
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Event is one notification in the CALLER's vocabulary: the Holistic user it reaches, the source
// service, a title (notifyd insists on one) and body, an optional deep link and icon, a level and a
// dedupe key. What goes over the wire is notifyd's own shape, assembled in Emit.
type Event struct {
	// User is the Holistic username the notification reaches. Required — DevLab always names a user
	// (the run owner) rather than broadcasting.
	User    string
	Service string
	Title   string
	Body    string
	URL     string
	Icon    string
	Level   string
	// Dedupe collapses repeats notifyd-side: two emits with the same key are ONE notification that
	// updates in place instead of two rows. DevLab already delivers a disturbance once (the pool
	// only fires on a new record), so this is a second, defensive guard — e.g. across a daemon
	// restart — never the primary mechanism.
	Dedupe string
}

// wireEvent is notifyd's /internal/emit request, exactly as notifyd declares its fields.
type wireEvent struct {
	User    string `json:"user"`
	Service string `json:"service"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	URL     string `json:"url"`
	Icon    string `json:"icon"`
	Level   string `json:"level"`
	Dedupe  string `json:"dedupe"`
}

// Emit delivers one notification to the named user via notifyd. It returns a non-nil error on any
// transport failure or non-2xx status (with notifyd's {"detail":…} reason when present), so the
// caller can surface and log a failed emit without pretending it succeeded.
func (c *Client) Emit(ctx context.Context, e Event) error {
	if strings.TrimSpace(e.User) == "" {
		return errors.New("notify: empty recipient user")
	}
	if strings.TrimSpace(e.Title) == "" {
		return errors.New("notify: a title is required")
	}
	body, err := json.Marshal(wireEvent{
		User:    strings.TrimSpace(e.User),
		Service: strings.TrimSpace(e.Service),
		Title:   e.Title,
		Body:    e.Body,
		URL:     e.URL,
		Icon:    e.Icon,
		Level:   e.Level,
		Dedupe:  e.Dedupe,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/emit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Notify-Internal-Secret", c.secret)
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
		return fmt.Errorf("notify: %s: %s", res.Status, detail)
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
