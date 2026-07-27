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
	"devlab/backend/internal/axiomrepo"
	"devlab/backend/internal/chats"
	"devlab/backend/internal/comments"
	"devlab/backend/internal/discover"
	"devlab/backend/internal/links"
	"devlab/backend/internal/live"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/workspace"
)

const version = "0.1.0"

// Server wires the verifier + repo base + per-user GitHub link store + workspace manager into
// HTTP handlers.
type Server struct {
	v           *auth.Verifier
	reposBase   string
	links       *links.Store // nil when no encryption key is configured (dev/preview sandbox)
	workspaces  *workspace.Manager
	comments    *comments.Store       // nil if the comments dir can't be created
	chats       *chats.Store          // nil if the chats dir can't be created — AI transcript persistence
	runs        *runs.Store           // Mercury's Automatische Läufe — run instances + config history
	runResults  *runs.Results         // per-execution results/logs (written by the executor, read here)
	runPRs      *runs.PRStore         // run-created PRs awaiting merge (auto-merge after the window)
	runNotices  *runs.NoticeStore     // passive feed of automatic axiom→run assignments (and their failures)
	deliveries  *runs.DeliveryStore   // ledger of per-repo deliveries (commit range + stacked PR) — the growing dev state
	attachments *runs.AttachmentStore // passive media pool for ToDo attachments (bytes; metadata is on the Run)
	axiomChecks *runs.AxiomChecks     // per repo+axiom: the commit it was last examined against (incremental runs)
	scheduler   *runs.Scheduler       // nil until StartScheduler arms it (needs DEVLAB_RUNS_MODE + _USER)
	autoRollout *autoRollout          // debounced background CLAUDE.md rollout on axiom/rule writes
	axioms      *axiomrepo.Store      // the constitution itself: a dedicated Git repository, versioned and unprotected
	assigner    *autoAssigner         // background: assigns any uncovered axiom to a run (reuses the AI-fill machinery)
	runExec     *runExecutor          // the armed executor — also drives rollback/reset; nil when the scheduler is off
	live        *live.Broker          // Mercury's single change-notification bus (SSE fan-out); never nil
	staticDir   string                // built SPA to serve for non-/api routes ("" ⇒ 404, e.g. dev where vite serves)
}

// publish emits a Mercury change-topic to every open UI stream. Nil-safe on the broker, so it is always
// callable. Kept as a one-liner so mutation handlers can announce a change in a single line.
func (s *Server) publish(topic string) { s.live.Publish(topic) }

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
	// The single change-notification bus. Wired into the run store and delivery ledger so every mutation
	// through them notifies open UIs; the scheduler is wired in StartScheduler, the axiom handlers publish
	// directly. One bus, many publishers — the one live mechanism the whole Mercury surface shares.
	broker := live.NewBroker()
	runStore := runs.NewStore()
	runStore.SetPublisher(broker) // every run/ToDo mutation now notifies open UIs
	deliveries := runs.NewDeliveryStore()
	deliveries.SetPublisher(broker) // every delivery-ledger change notifies open UIs
	s := &Server{
		v:           v,
		reposBase:   base,
		links:       store,
		workspaces:  workspace.NewManager(),
		comments:    cstore,
		chats:       chatStore,
		runs:        runStore,
		runResults:  runs.NewResults(),
		runPRs:      runs.NewPRStore(),
		runNotices:  runs.NewNoticeStore(),
		deliveries:  deliveries,
		attachments: runs.NewAttachmentStore(),
		axiomChecks: runs.NewAxiomChecks(),
		live:        broker,
		staticDir:   os.Getenv("DEVLAB_STATIC_DIR"),
	}
	// The constitution lives in its own repository. Pushing uses the runner's linked account — the same
	// identity the autonomous pipeline commits with — so an edit works whether or not the person making
	// it has linked GitHub themselves. With no link store (dev/preview, no encryption key) there is no
	// token: report that cleanly so the store maps it to ErrNoToken rather than panicking on a nil store.
	s.axioms = axiomrepo.New(axiomsDir(), axiomsRepo(), func() (string, error) {
		if s.links == nil {
			return "", errors.New("no GitHub link store configured")
		}
		return s.links.Token(axiomsTokenUser())
	})
	s.autoRollout = newAutoRollout(s)
	// The auto-assigner runs on a caller's forwarded session (like the AI-fill button), so it needs no
	// scheduler-style provisioning — arm it whenever the run store exists.
	s.assigner = newAutoAssigner(s)
	// The constitution repository documents itself. Seed its README on startup when an account is
	// configured to push with — best-effort and create-only, so it never blocks and never fights a
	// hand-edited README. Without a token user we skip it entirely (no background network I/O).
	if axiomsTokenUser() != "" {
		go func() {
			if err := s.axioms.EnsureReadme(context.Background()); err != nil {
				log.Printf("devlabd: axiom repository README seed skipped: %v", err)
			}
		}()
	}
	return s
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
	mux.HandleFunc("POST /api/repos/{id}/pr", s.guardWrite(s.openPR))

	// Vision Catalog — the repo's /vision folder. Reads under guard; raw bytes for the viewer;
	// upload writes into vision/ (needs push). Threaded comments live in a DevLab-side store.
	mux.HandleFunc("GET /api/repos/{id}/vision", s.guard(s.visionList))
	mux.HandleFunc("GET /api/repos/{id}/raw", s.guard(s.visionRaw))
	mux.HandleFunc("POST /api/repos/{id}/vision/upload", s.guardWrite(s.visionUpload))
	mux.HandleFunc("POST /api/repos/{id}/vision/delete", s.guardWrite(s.visionDelete))
	mux.HandleFunc("GET /api/repos/{id}/comments", s.guard(s.commentsList))
	mux.HandleFunc("POST /api/repos/{id}/comments", s.guardWrite(s.commentAdd))
	mux.HandleFunc("DELETE /api/repos/{id}/comments/{cid}", s.guardWrite(s.commentDelete))

	// AI assistant — proxied to the aigentic service with the open repo as context, plus a
	// per-user-per-repo transcript so a conversation survives a reload.
	mux.HandleFunc("POST /api/repos/{id}/assistant", s.guardWrite(s.assistant))
	mux.HandleFunc("GET /api/repos/{id}/assistant/history", s.guard(s.getHistory))
	mux.HandleFunc("PUT /api/repos/{id}/assistant/history", s.guardWrite(s.putHistory))
	mux.HandleFunc("GET /api/assistant/models", s.guard(s.assistantModels))

	// Agentic AI — the FULL claude CLI run AS the user inside their repo workspace (Edit/Write/Bash,
	// Plan/Auto/Full modes, their own ~/.claude config). Can modify the tree, so it's a write op.
	mux.HandleFunc("POST /api/repos/{id}/agent", s.guardWrite(s.agent))

	// Terminal — a real shell in the caller's repo workspace, proxied over loopback to the
	// remshel provider (which owns the pty + recording + run-as-user escalation). WebSocket:
	// authenticated by guard, upgraded inside the handler.
	mux.HandleFunc("GET /api/repos/{id}/pty", s.guard(s.pty))

	// Preview — per-user, passphrase-gated branch previews via sxgate (branch code runs as the
	// user, never root). Create/delete mutate (guardWrite); listing is read-only.
	mux.HandleFunc("GET /api/repos/{id}/previews", s.guard(s.previewList))
	mux.HandleFunc("POST /api/repos/{id}/previews", s.guardWrite(s.previewCreate))
	mux.HandleFunc("DELETE /api/repos/{id}/previews/{slug}", s.guardWrite(s.previewDelete))

	// Atlas — the deployed Holistic landscape, derived from the host's own config (the services'
	// rights manifests + their Caddy routes). Read-only, host-scoped, no workspace access.
	mux.HandleFunc("GET /api/atlas", s.guard(s.atlasGraph))

	// Mercury — the axiom-management model over the constitution's own Git repository (package
	// axiomrepo). Read tier (the tree and a record); both are served from the local working copy.
	mux.HandleFunc("GET /api/mercury/tree", s.guard(s.mercuryTree))
	mux.HandleFunc("GET /api/mercury/item", s.guard(s.mercuryItem))
	// The single live-update stream (Server-Sent Events): one long-lived connection per open Mercury
	// surface over which the server pushes coarse "topic X changed" notifications. Read-tier (a GET, no
	// CSRF); the client refetches through the normal read endpoints on each notification.
	mux.HandleFunc("GET /api/mercury/events", s.guard(s.mercuryEvents))
	// Add an axiom: aigentic classifies it into the tree, DevLab commits it to the constitution repo.
	// CSRF-guarded; the push uses the runner's linked account (see New), so the caller needs no GitHub
	// link of their own.
	mux.HandleFunc("POST /api/mercury/axiom", s.guardCSRF(s.addAxiom))
	// Edit an axiom's text; move/rename it; delete it; rename a whole category. The user owns the
	// tree by hand. All CSRF-guarded store writes; no GitHub involved.
	mux.HandleFunc("PUT /api/mercury/axiom", s.guardCSRF(s.editAxiom))
	mux.HandleFunc("DELETE /api/mercury/axiom", s.guardCSRF(s.deleteAxiom))
	mux.HandleFunc("POST /api/mercury/move", s.guardCSRF(s.moveAxiom))
	mux.HandleFunc("POST /api/mercury/move-category", s.guardCSRF(s.moveCategory))
	mux.HandleFunc("POST /api/mercury/reorder", s.guardCSRF(s.mercuryReorder))
	// Polish a record (orthography, generalization, brevity) via aigentic — the "Mit KI optimieren"
	// button and the auto-optimize on add. Returns the polished text; does not persist.
	mux.HandleFunc("POST /api/mercury/optimize", s.guardCSRF(s.optimizeRecord))
	// Check an axiom strictly against every meta-axiom (the binding requirements an axiom must satisfy).
	// Returns conformance + violations + a corrected proposal; does not persist.
	mux.HandleFunc("POST /api/mercury/conform", s.guardCSRF(s.conformRecord))
	// The Mercury-WIDE assistant: knows axioms, rules, Laufregeln, runs and ToDos. Lives at the
	// Mercury level (not inside a section), and may return a reviewable run-plan proposal.
	mux.HandleFunc("POST /api/mercury/chat", s.guardCSRF(s.mercuryChat))
	// Roll the axioms + rules into every holistic repo's CLAUDE.md. Dry-run by default (?apply=true
	// pushes). This DOES touch GitHub repos, so it needs the full write guard (a linked account). The
	// automatic rollout (on axiom/rule writes) runs in the background; GET reports its last result.
	mux.HandleFunc("GET /api/mercury/rollout", s.guard(s.mercuryRolloutStatus))
	mux.HandleFunc("POST /api/mercury/rollout", s.guardWrite(s.mercuryRollout))
	// One-time constitution migration: decompose the original axiom document into atoms and commit
	// each into the constitution repo. Dry-run by default (?apply=true writes).
	mux.HandleFunc("POST /api/mercury/migrate", s.guardCSRF(s.mercuryMigrate))

	// Automatische Läufe — run instances: scheduled autonomous runs over all holistic repos, whose
	// prompt is composed from their axioms + all global Laufregeln. Reads under guard; store writes
	// (CRUD, AI-planning proposals + apply, config history) under guardCSRF (aigentic/store authority,
	// no GitHub). Literal sub-paths (coverage/history/ai-*) precede the {id} routes so Go's mux picks
	// them first. Execution ("run now"/scheduler) is a separate, gated phase.
	mux.HandleFunc("GET /api/mercury/runs", s.guard(s.runsList))
	mux.HandleFunc("GET /api/mercury/runs/coverage", s.guard(s.runsCoverage))
	mux.HandleFunc("GET /api/mercury/runs/history", s.guard(s.runsHistoryList))
	mux.HandleFunc("GET /api/mercury/runs/calendar", s.guard(s.runsCalendar))
	mux.HandleFunc("GET /api/mercury/runs/executions", s.guard(s.runsExecutions))
	mux.HandleFunc("GET /api/mercury/runs/active", s.guard(s.runActive))
	// Automatic axiom→run assignment feed: the user is informed of every background assignment (and any
	// failure) here, and can dismiss acknowledged entries. Reads under guard; dismiss/clear under CSRF.
	mux.HandleFunc("GET /api/mercury/runs/notices", s.guard(s.runsNoticesList))
	mux.HandleFunc("POST /api/mercury/runs/notices/dismiss", s.guardCSRF(s.runsNoticeDismiss))
	mux.HandleFunc("POST /api/mercury/runs/notices/clear", s.guardCSRF(s.runsNoticesClear))
	mux.HandleFunc("GET /api/mercury/runs/deliveries", s.guard(s.runDeliveriesList))
	// Blocked prod-deliveries: the ones the scheduler stopped auto-retrying after repeated permanent
	// failures (read), and the explicit per-delivery resume that clears the block (CSRF).
	mux.HandleFunc("GET /api/mercury/runs/deploys", s.guard(s.runDeploysBlocked))
	mux.HandleFunc("GET /api/mercury/runs/{id}", s.guard(s.runGet))
	mux.HandleFunc("GET /api/mercury/runs/{id}/prompt", s.guard(s.runPromptPreview))
	mux.HandleFunc("GET /api/mercury/runs/{id}/results", s.guard(s.runResultsList))
	mux.HandleFunc("GET /api/mercury/runs/{id}/results/{rid}", s.guard(s.runResultGet))
	mux.HandleFunc("POST /api/mercury/runs", s.guardCSRF(s.runCreate))
	mux.HandleFunc("PUT /api/mercury/runs/{id}", s.guardCSRF(s.runUpdate))
	mux.HandleFunc("DELETE /api/mercury/runs/{id}", s.guardCSRF(s.runDelete))
	mux.HandleFunc("POST /api/mercury/runs/ai-fill", s.guardCSRF(s.runsAiFill))
	mux.HandleFunc("POST /api/mercury/runs/ai-finetune", s.guardCSRF(s.runsAiFinetune))
	mux.HandleFunc("POST /api/mercury/runs/apply-proposal", s.guardCSRF(s.runsApplyProposal))
	mux.HandleFunc("POST /api/mercury/runs/history/restore", s.guardCSRF(s.runsHistoryRestore))
	// Execution controls (Phase 2). Inert until the scheduler is armed (DEVLAB_RUNS_MODE + _USER). With
	// several runs concurrent, cancel and defer each target ONE run by id (task point 4).
	mux.HandleFunc("POST /api/mercury/runs/{id}/run", s.guardCSRF(s.runNow))
	mux.HandleFunc("POST /api/mercury/runs/{id}/cancel", s.guardCSRF(s.runCancel))
	mux.HandleFunc("POST /api/mercury/runs/{id}/defer", s.guardCSRF(s.runDefer))
	// Deliveries: roll back a shipped delivery (counter-booking), or reset a repo's dev branch to default.
	mux.HandleFunc("POST /api/mercury/runs/deliveries/{id}/rollback", s.guardCSRF(s.runDeliveryRollback))
	mux.HandleFunc("POST /api/mercury/runs/reset", s.guardCSRF(s.runRepoReset))
	mux.HandleFunc("POST /api/mercury/runs/deploys/resume", s.guardCSRF(s.runDeployResume))

	// ToDo media — images/documents attached to a concrete ToDo, materialized into the agent's
	// workspace at run time so the AI considers them. Upload/remove mutate (CSRF); raw serves bytes.
	mux.HandleFunc("POST /api/mercury/runs/{id}/attachments", s.guardCSRF(s.runAttachmentUpload))
	mux.HandleFunc("GET /api/mercury/runs/{id}/attachments/{aid}/raw", s.guard(s.runAttachmentRaw))
	mux.HandleFunc("DELETE /api/mercury/runs/{id}/attachments/{aid}", s.guardCSRF(s.runAttachmentDelete))

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
			writeErr(w, http.StatusForbidden, errNoGitHubLink)
			return
		}
		h(w, r)
	})
}

// guardCSRF gates a mutating request that does NOT require the CALLER to have linked GitHub: a DevLab
// session (guard) + CSRF. Used by Mercury's axiom writes and run CRUD, whose authority is the DevLab
// session; where such a write reaches GitHub (the constitution repo) it is pushed server-side with the
// runner's own linked account (see New), never the caller's.
func (s *Server) guardCSRF(h http.HandlerFunc) http.HandlerFunc {
	return s.guard(func(w http.ResponseWriter, r *http.Request) {
		if !s.v.CheckCSRF(r) {
			writeErr(w, http.StatusForbidden, "Invalid CSRF token")
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
			writeErr(w, http.StatusNotFound, errRepoNotFound)
			return "", false
		}
		return p, true
	}
	u := userFrom(r)
	full, ok := s.resolveFullName(r.Context(), u, id)
	if !ok {
		writeErr(w, http.StatusNotFound, errRepoNotFound)
		return "", false
	}
	token, err := s.userToken(u)
	if err != nil {
		writeErr(w, http.StatusForbidden, errNoGitHubLink)
		return "", false
	}
	p, err := s.workspaces.Ensure(r.Context(), u.Username, id, full, token, !s.v.DevBypass())
	if err != nil {
		log.Printf("devlabd: prepare workspace %s/%s: %v", u.Username, id, err)
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

// Error details the handlers repeat — single source of truth so the wording stays in step.
const (
	errRepoNotFound = "Repository not found"
	errNoGitHubLink = "Link your GitHub account first"
	errMissingPath  = "Missing path"
)

// requireRealGitHub rejects a feature that needs a real per-user GitHub account when running under
// dev-bypass (the sandbox has no per-user token). Writes a 400 and returns false in that case.
// `feature` names the plural subject, e.g. "Previews", "Pull requests".
func (s *Server) requireRealGitHub(w http.ResponseWriter, feature string) bool {
	if s.v.DevBypass() {
		writeErr(w, http.StatusBadRequest, feature+" need a linked GitHub account")
		return false
	}
	return true
}

// requirePrompt writes a 400 and returns false when the prompt is blank — shared by the assistant
// and agent entry points.
func requirePrompt(w http.ResponseWriter, prompt string) bool {
	if strings.TrimSpace(prompt) == "" {
		writeErr(w, http.StatusBadRequest, "Empty prompt")
		return false
	}
	return true
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
