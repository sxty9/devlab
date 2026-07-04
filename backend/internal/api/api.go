// Package api serves the DevLab HTTP surface under /api, behind the auth tiers in package auth.
// Error bodies follow the holistic contract: {"detail": "..."}.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/chats"
	"devlab/backend/internal/comments"
	"devlab/backend/internal/discover"
	"devlab/backend/internal/links"
	"devlab/backend/internal/workspace"
)

const version = "0.1.0"

// Server wires the verifier + repo base + per-user GitHub link store + workspace manager into
// HTTP handlers.
type Server struct {
	v          *auth.Verifier
	reposBase  string
	links      *links.Store // nil when no encryption key is configured (dev/preview sandbox)
	workspaces *workspace.Manager
	comments   *comments.Store // nil if the comments dir can't be created
	chats      *chats.Store    // nil if the chats dir can't be created — AI transcript persistence
	staticDir  string          // built SPA to serve for non-/api routes ("" ⇒ 404, e.g. dev where vite serves)
}

// New builds a server. DEVLAB_REPOS_PATH is the base dir holding local working copies (sandbox).
// The link store needs DEVLAB_LINK_ENC_KEY_FILE; when absent (dev-bypass/preview), GitHub linking
// is disabled and discovery/paths fall back to the local sandbox set. DEVLAB_STATIC_DIR, when set,
// makes devlabd serve the built SPA (dist/) itself so one process serves both UI and API.
func New(v *auth.Verifier) *Server {
	base := os.Getenv("DEVLAB_REPOS_PATH")
	if base == "" {
		base = "/home/nanu"
	}
	store, err := links.NewStore()
	if err != nil {
		log.Printf("devlabd: GitHub link store disabled: %v", err)
		store = nil
	}
	cstore, err := comments.NewStore()
	if err != nil {
		log.Printf("devlabd: comments store disabled: %v", err)
		cstore = nil
	}
	chatStore, err := chats.NewStore()
	if err != nil {
		log.Printf("devlabd: chat store disabled: %v", err)
		chatStore = nil
	}
	return &Server{
		v:          v,
		reposBase:  base,
		links:      store,
		workspaces: workspace.NewManager(),
		comments:   cstore,
		chats:      chatStore,
		staticDir:  os.Getenv("DEVLAB_STATIC_DIR"),
	}
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

	// Local mint-only refresh: re-mints a fresh h_access from the shared h_refresh cookie so a
	// 15-minute access expiry doesn't bounce the SPA to Holistic. No CSRF gate (SameSite=Lax on
	// h_refresh blocks cross-site POSTs; the call only refreshes the caller's own session).
	mux.HandleFunc("POST /api/auth/refresh", s.refresh)

	// GitHub linking (mandatory before the workspace loads). authorize/callback identify the
	// user via the session; unlink mutates so it additionally checks CSRF.
	mux.HandleFunc("GET /api/github/authorize", s.guard(s.githubAuthorize))
	mux.HandleFunc("GET /api/github/callback", s.guard(s.githubCallback))
	mux.HandleFunc("POST /api/github/unlink", s.guardWrite(s.githubUnlink))

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

	// Write tier — guardWrite: session + hp_devlab_access + CSRF + GitHub link; the handlers
	// additionally enforce the per-repo GitHub permission (push for mutations, pull for fetch).
	mux.HandleFunc("POST /api/repos/{id}/ensure", s.guardWrite(s.gitEnsure))
	mux.HandleFunc("POST /api/repos/{id}/file", s.guardWrite(s.gitWriteFile))
	mux.HandleFunc("POST /api/repos/{id}/stage", s.guardWrite(s.gitStage))
	mux.HandleFunc("POST /api/repos/{id}/unstage", s.guardWrite(s.gitUnstage))
	mux.HandleFunc("POST /api/repos/{id}/commit", s.guardWrite(s.gitCommit))
	mux.HandleFunc("POST /api/repos/{id}/push", s.guardWrite(s.gitPush))
	mux.HandleFunc("POST /api/repos/{id}/pull", s.guardWrite(s.gitPull))
	mux.HandleFunc("POST /api/repos/{id}/branch", s.guardWrite(s.gitBranch))
	mux.HandleFunc("POST /api/repos/{id}/checkout", s.guardWrite(s.gitCheckout))

	// Vision Catalog — the repo's /vision folder. Reads under guard; raw bytes for the viewer;
	// upload writes into vision/ (needs push). Threaded comments live in a DevLab-side store.
	mux.HandleFunc("GET /api/repos/{id}/vision", s.guard(s.visionList))
	mux.HandleFunc("GET /api/repos/{id}/raw", s.guard(s.visionRaw))
	mux.HandleFunc("POST /api/repos/{id}/vision/upload", s.guardWrite(s.visionUpload))
	mux.HandleFunc("GET /api/repos/{id}/comments", s.guard(s.commentsList))
	mux.HandleFunc("POST /api/repos/{id}/comments", s.guardWrite(s.commentAdd))
	mux.HandleFunc("DELETE /api/repos/{id}/comments/{cid}", s.guardWrite(s.commentDelete))

	// AI assistant — proxied to the aigentic service with the open repo as context, plus a
	// per-user-per-repo transcript so a conversation survives a reload.
	mux.HandleFunc("POST /api/repos/{id}/assistant", s.guardWrite(s.assistant))
	mux.HandleFunc("GET /api/repos/{id}/assistant/history", s.guard(s.getHistory))
	mux.HandleFunc("PUT /api/repos/{id}/assistant/history", s.guardWrite(s.putHistory))

	// Unknown /api paths 404 as JSON; everything else serves the built SPA (with client-routing
	// fallback to index.html) when DEVLAB_STATIC_DIR is set, else 404.
	mux.HandleFunc("/", s.root)

	return secureHeaders(mux)
}

// root serves the SPA for non-API routes; unknown /api paths 404 as JSON.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || s.staticDir == "" {
		writeErr(w, http.StatusNotFound, "Not found")
		return
	}
	s.serveSPA(w, r)
}

// serveSPA serves a file from staticDir, falling back to index.html for client-side routes. The
// path is cleaned and confined to staticDir; index.html is never long-cached so new deploys show.
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	root := filepath.Clean(s.staticDir)
	upath := path.Clean("/" + r.URL.Path)
	full := filepath.Join(root, filepath.FromSlash(upath))
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		writeErr(w, http.StatusNotFound, "Not found")
		return
	}
	if upath != "/" {
		if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, full)
			return
		}
	}
	// SPA entrypoint: revalidate so a redeploy is picked up promptly.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(root, "index.html"))
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

// guardWrite gates mutating requests: a DevLab session (guard) + CSRF double-submit + a linked
// GitHub account. Per-repo GitHub push permission is enforced inside the write handlers (Slice 3),
// which know the target repo. Under dev-bypass the GitHub-link requirement is waived (sandbox).
func (s *Server) guardWrite(h http.HandlerFunc) http.HandlerFunc {
	return s.guard(func(w http.ResponseWriter, r *http.Request) {
		if !s.v.CheckCSRF(r) {
			writeErr(w, http.StatusForbidden, "Invalid CSRF token")
			return
		}
		u := userFrom(r)
		if !s.githubLinked(u) {
			writeErr(w, http.StatusForbidden, "Link your GitHub account first")
			return
		}
		h(w, r)
	})
}

// githubLinked reports whether the user has a usable GitHub link. Dev-bypass is treated as linked
// (the sandbox operates on local clones with no per-user token).
func (s *Server) githubLinked(u *auth.User) bool {
	if s.v.DevBypass() {
		return true
	}
	return s.links != nil && u != nil && s.links.Linked(u.Username)
}

// userToken returns the user's decrypted GitHub OAuth token (for server-side GitHub calls only —
// never returned to the client). Errors when no link store or no link exists.
func (s *Server) userToken(u *auth.User) (string, error) {
	if s.links == nil || u == nil {
		return "", errNoLink
	}
	return s.links.Token(u.Username)
}

var errNoLink = errors.New("no github link")

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "devlab", "version": version})
}

// refresh re-mints the access + CSRF cookies from a valid h_refresh, or 401s.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	access, csrf, err := s.v.RefreshAccess(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "Session expired")
		return
	}
	setSessionCookies(w, access, csrf)
	w.WriteHeader(http.StatusNoContent)
}

// user returns the caller's identity, DevLab access, and GitHub-link status — the SPA's bootstrap
// probe that drives the login / access-denied / github-link / ready gates.
func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	ghLogin := ""
	if s.links != nil {
		if l, err := s.links.Get(u.Username); err == nil && l != nil {
			ghLogin = l.GHLogin
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":     u.Username,
		"displayName":  auth.DisplayName(u.Username),
		"isAdmin":      u.IsAdmin,
		"canUseDevlab": u.CanUseDevlab(),
		"githubLinked": s.githubLinked(u),
		"githubLogin":  ghLogin,
	})
}

// repoPath resolves {id} to the working tree the caller operates on. Under dev-bypass that is the
// local sandbox clone; in production it is the caller's per-user workspace, cloned on first access
// from GitHub with their own token. Writes 404/403/502 and returns false on failure.
func (s *Server) repoPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if s.v.DevBypass() {
		p, ok := discover.Path(s.reposBase, id)
		if !ok {
			writeErr(w, http.StatusNotFound, "Repository not found")
			return "", false
		}
		return p, true
	}
	u := userFrom(r)
	full, ok := s.resolveFullName(r.Context(), u, id)
	if !ok {
		writeErr(w, http.StatusNotFound, "Repository not found")
		return "", false
	}
	token, err := s.userToken(u)
	if err != nil {
		writeErr(w, http.StatusForbidden, "Link your GitHub account first")
		return "", false
	}
	p, err := s.workspaces.Ensure(r.Context(), u.Username, id, full, token)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "Could not prepare the workspace")
		return "", false
	}
	return p, true
}

// resolveFullName maps a repo id to its GitHub owner/repo from the user's visible set, refreshing
// the per-user cache once on a miss so a cold cache (or a direct deep-link) still resolves.
func (s *Server) resolveFullName(ctx context.Context, u *auth.User, id string) (string, bool) {
	if u == nil {
		return "", false
	}
	if full, ok := discover.FullName(u.Username, id); ok {
		return full, true
	}
	token, err := s.userToken(u)
	if err != nil {
		return "", false
	}
	if _, err := discover.ReposForUser(ctx, u.Username, token); err != nil {
		return "", false
	}
	return discover.FullName(u.Username, id)
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
