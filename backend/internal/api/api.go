// Package api serves the DevLab HTTP surface under /api, behind the auth tiers in package auth.
// Error bodies follow the holistic contract: {"detail": "..."}.
package api

import (
	"context"
	"encoding/json"
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

// ctxKey namespaces the resolved user stashed in the request context by the guards.
type ctxKey int

const userCtxKey ctxKey = 0

// userFrom returns the authenticated user a guard placed on the request (nil if none).
func userFrom(r *http.Request) *auth.User {
	u, _ := r.Context().Value(userCtxKey).(*auth.User)
	return u
}

// Handler returns the routed handler (Go 1.22 method+path patterns).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/user", s.guardAuthed(s.user))

	// Read tier — a valid Holistic session that holds hp_devlab_access.
	mux.HandleFunc("GET /api/repos", s.guard(s.repos))
	mux.HandleFunc("GET /api/repos/{id}", s.guard(s.repoData))
	mux.HandleFunc("GET /api/repos/{id}/branches", s.guard(s.branches))
	mux.HandleFunc("GET /api/repos/{id}/tree", s.guard(s.tree))
	mux.HandleFunc("GET /api/repos/{id}/file", s.guard(s.file))
	mux.HandleFunc("GET /api/repos/{id}/changes", s.guard(s.changes))
	mux.HandleFunc("GET /api/repos/{id}/diff", s.guard(s.diff))
	mux.HandleFunc("GET /api/repos/{id}/commits", s.guard(s.commits))
	mux.HandleFunc("GET /api/repos/{id}/worktrees", s.guard(s.worktrees))

	// Anything else under /api 404s as JSON; non-/api is served by Caddy (static_proxy).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "Not found")
	})

	return secureHeaders(mux)
}

// guardAuthed requires a valid Holistic session and stashes the resolved user on the request.
// It does NOT enforce the DevLab right — used by /api/user so the SPA can tell "signed in but
// no access" from "not signed in".
func (s *Server) guardAuthed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.v.User(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, u)))
	}
}

// guard additionally enforces the single DevLab right (hp_devlab_access; admin implicit).
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return s.guardAuthed(func(w http.ResponseWriter, r *http.Request) {
		if u := userFrom(r); u == nil || !u.CanUseDevlab() {
			writeErr(w, http.StatusForbidden, "DevLab requires the hp_devlab_access right")
			return
		}
		h(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "devlab", "version": version})
}

// user returns the caller's identity + whether they may use DevLab (the SPA's bootstrap probe).
func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"username":     u.Username,
		"displayName":  auth.DisplayName(u.Username),
		"isAdmin":      u.IsAdmin,
		"canUseDevlab": u.CanUseDevlab(),
	})
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
