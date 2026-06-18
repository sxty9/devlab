// Package auth gates the DevLab API. Three credential paths resolve to two access levels:
//
//	dev-bypass (DEVLAB_DEV_BYPASS_AUTH=1)  -> Full   (localhost dev)
//	valid holistic JWT (h_access cookie)   -> Full   (mounted as a holistic service)
//	shared preview password (dl_preview)   -> Read   (the public sxgate preview)
//
// Per the chosen posture (read-only public, full power local), "power" operations — terminal,
// Claude, sxgate deploy, git writes — require Full; the public preview (Read) cannot perform
// them. CSRF double-submit additionally guards mutating power requests.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Level is the access tier resolved for a request.
type Level int

const (
	None Level = iota
	Read
	Full
)

const (
	previewCookie = "dl_preview"
	csrfCookie    = "dl_csrf"
	csrfHeader    = "X-CSRF-Token"
	accessCookie  = "h_access"
)

// Verifier holds the resolved auth configuration (read once at startup).
type Verifier struct {
	devBypass     bool
	jwtSecret     []byte
	previewExpect string // hex(sha256(password)); "" when no preview gate is configured
}

// New builds a Verifier from the environment.
func New() *Verifier {
	v := &Verifier{devBypass: os.Getenv("DEVLAB_DEV_BYPASS_AUTH") == "1"}
	v.jwtSecret = loadSecret()
	if pw := loadPreviewPassword(); pw != "" {
		sum := sha256.Sum256([]byte(pw))
		v.previewExpect = hex.EncodeToString(sum[:])
	}
	return v
}

// PreviewGated reports whether a shared preview password is required (public preview mode).
func (v *Verifier) PreviewGated() bool { return v.previewExpect != "" }

// Level resolves the access tier for a request.
func (v *Verifier) Level(r *http.Request) Level {
	if v.devBypass {
		return Full
	}
	if v.jwtValid(r) {
		return Full
	}
	if v.previewExpect != "" {
		if c, err := r.Cookie(previewCookie); err == nil &&
			subtle.ConstantTimeCompare([]byte(c.Value), []byte(v.previewExpect)) == 1 {
			return Read
		}
	}
	return None
}

// CheckPassword validates a submitted preview password (constant-time).
func (v *Verifier) CheckPassword(pw string) bool {
	if v.previewExpect == "" {
		return false
	}
	sum := sha256.Sum256([]byte(pw))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(v.previewExpect)) == 1
}

// PreviewCookieValue is the value to store in dl_preview after a successful login.
func (v *Verifier) PreviewCookieValue() string { return v.previewExpect }

// CheckCSRF enforces the double-submit guard (skipped under dev-bypass).
func (v *Verifier) CheckCSRF(r *http.Request) bool {
	if v.devBypass {
		return true
	}
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return hmac.Equal([]byte(r.Header.Get(csrfHeader)), []byte(c.Value))
}

func (v *Verifier) jwtValid(r *http.Request) bool {
	if len(v.jwtSecret) == 0 {
		return false
	}
	c, err := r.Cookie(accessCookie)
	if err != nil || c.Value == "" {
		return false
	}
	tok, err := jwt.Parse(c.Value, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return v.jwtSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !tok.Valid {
		return false
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return false
	}
	if t, _ := claims["type"].(string); t != "access" {
		return false
	}
	sub, _ := claims["sub"].(string)
	return sub != ""
}

func loadSecret() []byte {
	for _, k := range []string{"DEVLAB_PREVIEW_SECRET_FILE", "HOLISTIC_SECRET_FILE"} {
		if p := os.Getenv(k); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				if s := strings.TrimSpace(string(b)); s != "" {
					return []byte(s)
				}
			}
		}
	}
	if env := os.Getenv("HOLISTIC_SECRET"); env != "" {
		return []byte(env)
	}
	return nil
}

func loadPreviewPassword() string {
	p := os.Getenv("DEVLAB_PREVIEW_PASSWORD_FILE")
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
