// Bearer-token authentication (D 34): native/API callers present `Authorization: Bearer <jwt>`
// against the SAME verifier the cookie path uses. The guards treat both as equivalent; the
// CSRF double-submit is waived ONLY on the bearer path (a bearer header cannot be sent
// cross-site by a browser form).
package auth

import (
	"net/http"
	"regexp"
)

const bearerScheme = "Bearer "

// jwtChars is the compact-JWS alphabet (base64url segments joined by dots). A presented token is
// matched against it before it is handed on, so nothing that is not token-shaped reaches the
// verifier and no header content can leak into the carrier the verifier reads.
var jwtChars = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

// BearerPresented reports whether the request carries a bearer Authorization header — the
// guards use it to decide whether the CSRF check applies (cookie path only).
func BearerPresented(r *http.Request) bool { return bearerToken(r) != "" }

// bearerToken extracts the token from `Authorization: Bearer <jwt>`, or "" when the header is
// absent, names another scheme, or is not token-shaped.
func bearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	h := r.Header.Get("Authorization")
	if len(h) <= len(bearerScheme) || !equalFoldASCII(h[:len(bearerScheme)], bearerScheme) {
		return ""
	}
	tok := h[len(bearerScheme):]
	if !jwtChars.MatchString(tok) {
		return ""
	}
	return tok
}

// FromAuthorization verifies `Authorization: Bearer <jwt>` with the same fail-closed verifier
// as the cookie path.
//
// It deliberately does not re-implement token validation: the presented token is handed to User()
// through the carrier that verifier already reads, so ONE validation path serves both auth ways
// (fail-closed secret handling, HS256 only, type=="access", live Linux groups, dev-bypass — all
// identical by construction rather than by duplication).
func (v *Verifier) FromAuthorization(r *http.Request) (*User, error) {
	tok := bearerToken(r)
	if tok == "" {
		return nil, ErrNoSession
	}
	probe := &http.Request{Header: http.Header{"Cookie": []string{accessCookie + "=" + tok}}}
	return v.User(probe)
}

// FromRequest is the ONE entry point: cookie OR bearer, same verifier, same User.
func (v *Verifier) FromRequest(r *http.Request) (*User, error) {
	if BearerPresented(r) {
		return v.FromAuthorization(r)
	}
	return v.User(r)
}

// equalFoldASCII compares two equal-length ASCII strings case-insensitively (the scheme name).
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
