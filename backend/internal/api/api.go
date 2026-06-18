// Package api serves the DevLab HTTP surface under /api, behind the auth tiers in package auth.
// Error bodies follow the holistic contract: {"detail": "..."}.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/discover"
)

const version = "0.1.0"

// Server wires the verifier + repo base into HTTP handlers.
type Server struct {
	v         *auth.Verifier
	reposBase string
}

// New builds a server. DEVLAB_REPOS_PATH is the base dir holding local working copies.
func New(v *auth.Verifier) *Server {
	base := os.Getenv("DEVLAB_REPOS_PATH")
	if base == "" {
		base = "/home/nanu"
	}
	return &Server{v: v, reposBase: base}
}

// Handler returns the routed handler (Go 1.22 method+path patterns).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/session", s.session)

	// Read tier (preview password OR full).
	mux.HandleFunc("GET /api/repos", s.guard(false, false, s.repos))
	mux.HandleFunc("GET /api/repos/{id}", s.guard(false, false, s.repoData))
	mux.HandleFunc("GET /api/repos/{id}/branches", s.guard(false, false, s.branches))
	mux.HandleFunc("GET /api/repos/{id}/tree", s.guard(false, false, s.tree))
	mux.HandleFunc("GET /api/repos/{id}/file", s.guard(false, false, s.file))
	mux.HandleFunc("GET /api/repos/{id}/changes", s.guard(false, false, s.changes))
	mux.HandleFunc("GET /api/repos/{id}/diff", s.guard(false, false, s.diff))
	mux.HandleFunc("GET /api/repos/{id}/commits", s.guard(false, false, s.commits))
	mux.HandleFunc("GET /api/repos/{id}/worktrees", s.guard(false, false, s.worktrees))

	// Anything else under /api 404s as JSON; non-/api is served by Caddy (static_proxy).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "Not found")
	})

	return secureHeaders(mux)
}

type handler func(w http.ResponseWriter, r *http.Request)

// guard enforces the access tier: any auth for reads; Full for power ops; optional CSRF.
func (s *Server) guard(power, csrf bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lvl := s.v.Level(r)
		if lvl == auth.None {
			writeErr(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		if power && lvl != auth.Full {
			writeErr(w, http.StatusForbidden, "This action is disabled on the public preview")
			return
		}
		if csrf && !s.v.CheckCSRF(r) {
			writeErr(w, http.StatusForbidden, "CSRF check failed")
			return
		}
		h(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "devlab", "version": version, "previewGated": s.v.PreviewGated()})
}

// session exchanges the shared preview password for the dl_preview + dl_csrf cookies.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if !s.v.PreviewGated() {
		// Not a password-gated deployment (dev/JWT): nothing to do.
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gated": false})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if !s.v.CheckPassword(body.Password) {
		writeErr(w, http.StatusUnauthorized, "Wrong password")
		return
	}
	secure := os.Getenv("DEVLAB_COOKIE_SECURE") == "1"
	http.SetCookie(w, &http.Cookie{
		Name: previewCookieName, Value: s.v.PreviewCookieValue(), Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 60 * 60 * 12,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: randomToken(), Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 60 * 60 * 12,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gated": true})
}

const (
	previewCookieName = "dl_preview"
	csrfCookieName    = "dl_csrf"
)

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// repoPath resolves {id} to a local working copy, writing 404 if unknown.
func (s *Server) repoPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	p, ok := discover.Path(s.reposBase, id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Repository not found")
		return "", false
	}
	return p, true
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}
