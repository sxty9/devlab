package api

import (
	"net/http"
	"os"
	"time"

	"devlab/backend/internal/auth"
)

// Cookie names mirror the holistic session contract — DevLab re-mints the same shared cookies.
const (
	accessCookie = "h_access"
	csrfCookie   = "h_csrf"
	// csrfTTL keeps the readable CSRF cookie alive across many access refreshes (matches the
	// holistic refresh-token horizon so it never expires before the session does).
	csrfTTL = 7 * 24 * time.Hour
)

// cookieSecure reports whether cookies should carry the Secure attribute (prod over TLS).
func cookieSecure() bool { return os.Getenv("DEVLAB_COOKIE_SECURE") == "1" }

// cookieDomain returns the shared parent domain (e.g. .henrysoase.org) so the re-minted cookies
// overwrite holistic's domain-scoped variants rather than creating parallel host-only ones.
func cookieDomain() string { return os.Getenv("DEVLAB_COOKIE_DOMAIN") }

// setSessionCookies re-mints h_access (HttpOnly) + h_csrf (JS-readable) on the response. When a
// shared cookie domain is configured it first evicts any stale host-only variant (mirroring
// holistic's set_session) so the browser does not carry a shadowing duplicate.
func setSessionCookies(w http.ResponseWriter, access, csrf string) {
	secure := cookieSecure()
	domain := cookieDomain()

	if domain != "" {
		// Evict host-only leftovers (MaxAge<0 with no Domain ⇒ deletes the host-scoped cookie).
		for _, name := range []string{accessCookie, csrfCookie} {
			http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Secure: secure, SameSite: http.SameSiteLaxMode})
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     accessCookie,
		Value:    access,
		Path:     "/",
		Domain:   domain,
		MaxAge:   int(auth.AccessTTL / time.Second),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    csrf,
		Path:     "/",
		Domain:   domain,
		MaxAge:   int(csrfTTL / time.Second),
		HttpOnly: false, // double-submit: the SPA reads this and echoes it as X-CSRF-Token
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
