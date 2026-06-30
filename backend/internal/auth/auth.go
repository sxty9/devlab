// Package auth gates the DevLab API. Identity is the Holistic session: a shared HS256 JWT in
// the h_access cookie, validated locally (no RPC to holistic), with the caller's groups and
// admin status resolved live from the single Linux source of truth.
//
// DevLab has exactly ONE Holistic right — hp_devlab_access — deciding whether a user may use
// DevLab at all (admins implicitly hold it). Everything finer (which repos, read vs push) is
// governed by GitHub via the user's own OAuth token, not by Holistic groups. CSRF double-submit
// (the shared h_csrf cookie) additionally guards mutating requests.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	accessCookie  = "h_access"
	refreshCookie = "h_refresh"
	csrfCookie    = "h_csrf"
	csrfHeader    = "X-CSRF-Token"

	// devlabGroup is the sole DevLab right: the distributable "developer" Linux group.
	devlabGroup = "hp_devlab_access"

	// AccessTTL is the minted access-token lifetime, matching holistic's 15 minutes. The cookie
	// max-age is set to match so browser and token expire together.
	AccessTTL = 15 * time.Minute
)

// ErrNoSession means the request carried no valid, unexpired access token.
var ErrNoSession = errors.New("not authenticated")

// User is the resolved identity for a request. Admin and group membership are read live from
// the OS, never trusted from the token (the access token carries only {sub, type, exp}).
type User struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
	IsAdmin  bool     `json:"isAdmin"`
}

// Can reports membership in a backing Linux group under the holistic rights standard:
// admins implicitly hold every right.
func (u *User) Can(group string) bool { return u.IsAdmin || contains(u.Groups, group) }

// CanUseDevlab reports whether the user may use DevLab at all (the hp_devlab_access gate).
func (u *User) CanUseDevlab() bool { return u.Can(devlabGroup) }

// Verifier holds the shared signing secret, the admin group, and the dev-bypass flag.
type Verifier struct {
	secret     []byte
	adminGroup string
	devBypass  bool
}

// New builds a Verifier from the environment. The secret is read the same way the holistic
// backend reads it, so the same h_access JWTs validate here. When the secret is missing the
// verifier holds an empty key; callers MUST refuse to start in SSO mode (see HasSecret) because
// HMAC verification with an empty key would otherwise accept forged tokens. User() also
// fail-closes on an empty secret as defense-in-depth.
func New() *Verifier {
	secret, _ := LoadSecret()
	adminGroup := os.Getenv("HOLISTIC_ADMIN_GROUP")
	if adminGroup == "" {
		adminGroup = "sudo"
	}
	return &Verifier{
		secret:     secret,
		adminGroup: adminGroup,
		devBypass:  os.Getenv("DEVLAB_DEV_BYPASS_AUTH") == "1",
	}
}

// DevBypass reports whether localhost dev-bypass is active (full access, no JWT/CSRF).
func (v *Verifier) DevBypass() bool { return v.devBypass }

// HasSecret reports whether a non-empty signing secret was loaded. main.go refuses to start in
// SSO mode without one (an empty HMAC key would validate forged tokens — fail closed).
func (v *Verifier) HasSecret() bool { return len(v.secret) > 0 }

// LoadSecret reads the shared JWT secret: DEVLAB_PREVIEW_SECRET_FILE (override) or
// HOLISTIC_SECRET_FILE (default /etc/holistic/jwt-secret), else HOLISTIC_SECRET.
func LoadSecret() ([]byte, error) {
	paths := []string{os.Getenv("DEVLAB_PREVIEW_SECRET_FILE"), os.Getenv("HOLISTIC_SECRET_FILE"), "/etc/holistic/jwt-secret"}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return []byte(s), nil
			}
		}
	}
	if env := os.Getenv("HOLISTIC_SECRET"); env != "" {
		return []byte(env), nil
	}
	return nil, errors.New("no JWT secret: set HOLISTIC_SECRET_FILE or HOLISTIC_SECRET")
}

// User extracts and validates the session from the request. Under dev-bypass it returns the
// current OS user as an admin (full access).
func (v *Verifier) User(r *http.Request) (*User, error) {
	if v.devBypass {
		return v.devUser(), nil
	}
	// Fail closed on a missing secret: HMAC verification with an empty key accepts tokens
	// signed with the empty key, so never attempt validation without a real secret.
	if len(v.secret) == 0 {
		return nil, ErrNoSession
	}
	c, err := r.Cookie(accessCookie)
	if err != nil || c.Value == "" {
		return nil, ErrNoSession
	}
	tok, err := jwt.Parse(c.Value, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return v.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !tok.Valid {
		return nil, ErrNoSession
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrNoSession
	}
	// Reject refresh tokens — only access tokens authenticate API requests.
	if t, _ := claims["type"].(string); t != "access" {
		return nil, ErrNoSession
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, ErrNoSession
	}
	// Reject a token whose backing Linux account no longer exists. Admin derived live.
	groups, exists := resolveGroups(sub)
	if !exists {
		return nil, ErrNoSession
	}
	return &User{Username: sub, Groups: groups, IsAdmin: contains(groups, v.adminGroup)}, nil
}

// devUser synthesizes a full-access identity for localhost dev-bypass.
func (v *Verifier) devUser() *User {
	name := "dev"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	groups, _ := resolveGroups(name)
	return &User{Username: name, Groups: groups, IsAdmin: true}
}

// RefreshAccess implements DevLab's mint-only local refresh. It reads the shared h_refresh
// cookie, validates it (HS256, type=="refresh", unexpired, backing account still exists),
// consults the holistic revocation set READ-ONLY, and mints a fresh h_access + h_csrf.
//
// It deliberately does NOT rotate h_refresh and does NOT write any holistic state (no new sid,
// no revocation). Holistic remains the single writer of session state (Single Source of Truth);
// DevLab only re-mints short-lived access locally so the SPA survives a 15-minute expiry without
// a redirect (preserving unsaved editor state) and without CORS-coupling to the holistic origin.
func (v *Verifier) RefreshAccess(r *http.Request) (access, csrf string, err error) {
	if len(v.secret) == 0 {
		return "", "", ErrNoSession
	}
	c, err := r.Cookie(refreshCookie)
	if err != nil || c.Value == "" {
		return "", "", ErrNoSession
	}
	tok, err := jwt.Parse(c.Value, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return v.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !tok.Valid {
		return "", "", ErrNoSession
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", ErrNoSession
	}
	if t, _ := claims["type"].(string); t != "refresh" {
		return "", "", ErrNoSession
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", "", ErrNoSession
	}
	// Honor a holistic logout/rotation if we can read the revocation set; if it's unreadable we
	// fall back to the refresh-token TTL (documented degradation), never failing open louder.
	if sid, _ := claims["sid"].(string); sid != "" && isRevoked(sid) {
		return "", "", ErrNoSession
	}
	if _, exists := resolveGroups(sub); !exists {
		return "", "", ErrNoSession
	}
	access, err = v.mintAccess(sub)
	if err != nil {
		return "", "", err
	}
	return access, newCSRF(), nil
}

// mintAccess signs a holistic-compatible access token {sub, type:"access", exp}.
func (v *Verifier) mintAccess(sub string) (string, error) {
	claims := jwt.MapClaims{
		"sub":  sub,
		"type": "access",
		"exp":  time.Now().Add(AccessTTL).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(v.secret)
}

// newCSRF generates a fresh double-submit token (32 hex chars), matching holistic's format.
func newCSRF() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic; an empty token simply fails the next CSRF check.
		return ""
	}
	return hex.EncodeToString(b)
}

// revokedPath resolves the holistic revocation set (read-only for DevLab).
func revokedPath() string {
	if p := os.Getenv("HOLISTIC_REVOKED"); p != "" {
		return p
	}
	return "/var/lib/holistic/revoked.json"
}

// isRevoked reports whether a session id appears in the holistic revocation set. An unreadable
// or malformed set is treated as "not revoked" (fail-soft: logout honoring then degrades to the
// refresh-token TTL, the documented fallback).
func isRevoked(sid string) bool {
	b, err := os.ReadFile(revokedPath())
	if err != nil {
		return false
	}
	var sids []string
	if json.Unmarshal(b, &sids) != nil {
		return false
	}
	for _, s := range sids {
		if s == sid {
			return true
		}
	}
	return false
}

// CheckCSRF enforces the double-submit guard: header X-CSRF-Token must equal the readable
// h_csrf cookie. Skipped under dev-bypass. Required on mutating requests; not on GETs.
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

// DisplayName resolves a human name for a username from the OS gecos field, falling back to
// the username itself.
func DisplayName(username string) string {
	if u, err := user.Lookup(username); err == nil {
		if name := strings.SplitN(u.Name, ",", 2)[0]; strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return username
}

// resolveGroups reads the user's Linux groups live from the OS, with a shell fallback for NSS
// sources the pure-Go os/user path may miss. The bool reports whether the account exists at all.
func resolveGroups(username string) ([]string, bool) {
	exists := false
	var groups []string
	if u, err := user.Lookup(username); err == nil {
		exists = true
		if gids, err := u.GroupIds(); err == nil {
			for _, gid := range gids {
				if g, err := user.LookupGroupId(gid); err == nil {
					groups = append(groups, g.Name)
				}
			}
		}
	}
	if len(groups) == 0 {
		if out, err := exec.Command("id", "-nG", username).Output(); err == nil {
			groups = strings.Fields(string(out))
			exists = true
		}
	}
	return groups, exists
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
