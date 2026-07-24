package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"

	"devlab/backend/internal/discover"
	"devlab/backend/internal/github"
)

// oauthStateCookie carries the anti-CSRF nonce across the GitHub OAuth round-trip. Path-scoped to
// /api/github and SameSite=Lax so it survives GitHub's top-level redirect back to the callback.
const oauthStateCookie = "dl_oauth_state"

// randHex returns n random bytes as a hex string (anti-CSRF state, etc.).
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// githubOAuthConfig loads the OAuth config and verifies the link store is available, writing the
// matching 503 and returning ok=false when GitHub linking can't proceed. Shared by authorize/callback.
func (s *Server) githubOAuthConfig(w http.ResponseWriter) (*github.OAuthConfig, bool) {
	cfg, err := github.LoadOAuthConfig()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "GitHub linking is not configured")
		return nil, false
	}
	if s.links == nil {
		writeErr(w, http.StatusServiceUnavailable, "GitHub link store unavailable")
		return nil, false
	}
	return cfg, true
}

// githubAuthorize starts the OAuth flow: stash a state nonce and redirect to GitHub.
func (s *Server) githubAuthorize(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.githubOAuthConfig(w)
	if !ok {
		return
	}
	state := randHex(16)
	if state == "" {
		writeErr(w, http.StatusInternalServerError, "Could not start GitHub flow")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    state,
		Path:     "/api/github",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   cookieSecure(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, cfg.AuthorizeURL(state), http.StatusFound)
}

// clearOAuthState expires the state cookie (one-time use).
func clearOAuthState(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: oauthStateCookie, Value: "", Path: "/api/github", MaxAge: -1,
		HttpOnly: true, Secure: cookieSecure(), SameSite: http.SameSiteLaxMode,
	})
}

// githubCallback completes the OAuth flow: verify state, exchange the code, read the GitHub
// identity, persist the encrypted token, and redirect back into the SPA.
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	c, cErr := r.Cookie(oauthStateCookie)
	got := r.URL.Query().Get("state")
	clearOAuthState(w)

	if cErr != nil || c.Value == "" || got == "" ||
		subtle.ConstantTimeCompare([]byte(c.Value), []byte(got)) != 1 {
		writeErr(w, http.StatusBadRequest, "Invalid OAuth state")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		writeErr(w, http.StatusBadRequest, "GitHub authorization was denied")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeErr(w, http.StatusBadRequest, "Missing authorization code")
		return
	}
	cfg, ok := s.githubOAuthConfig(w)
	if !ok {
		return
	}
	token, scope, err := cfg.Exchange(r.Context(), code)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "GitHub token exchange failed")
		return
	}
	viewer, err := github.GetViewer(r.Context(), token)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Could not read GitHub identity")
		return
	}
	if err := s.links.Save(u.Username, viewer.Login, viewer.ID, token, scope, time.Now()); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save GitHub link")
		return
	}
	discover.InvalidateUser(u.Username)
	http.Redirect(w, r, "/", http.StatusFound)
}

// githubUnlink removes the user's GitHub association (guardWrite: session + CSRF + linked).
func (s *Server) githubUnlink(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if s.links != nil {
		_ = s.links.Delete(u.Username)
	}
	discover.InvalidateUser(u.Username)
	w.WriteHeader(http.StatusNoContent)
}
