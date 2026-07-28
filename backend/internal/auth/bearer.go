// Bearer-token authentication (D 34): native/API callers present `Authorization: Bearer <jwt>`
// against the SAME verifier the cookie path uses. The guards treat both as equivalent; the
// CSRF double-submit is waived ONLY on the bearer path (a bearer header cannot be sent
// cross-site by a browser form).
package auth

import "net/http"

// BearerPresented reports whether the request carries a bearer Authorization header — the
// guards use it to decide whether the CSRF check applies (cookie path only).
func BearerPresented(r *http.Request) bool {
	panic("TODO(B11)")
}

// FromAuthorization verifies `Authorization: Bearer <jwt>` with the same fail-closed verifier
// as the cookie path.
func (v *Verifier) FromAuthorization(r *http.Request) (*User, error) {
	panic("TODO(B11)")
}

// FromRequest is the ONE entry point: cookie OR bearer, same verifier, same User.
func (v *Verifier) FromRequest(r *http.Request) (*User, error) {
	panic("TODO(B11)")
}
