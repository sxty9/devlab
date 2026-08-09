// Package api is the THIN HTTP layer (B-03): the complete route table (this file, ONE place,
// frozen after Welle 0), the auth guards, and thin handlers that parse → domain package →
// answer. Error bodies follow the holistic contract: {"detail": "..."}. No domain logic lives
// here; nobody imports this package.
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
	"devlab/backend/internal/axiomauthors"
	"devlab/backend/internal/axiomrepo"
	"devlab/backend/internal/chats"
	"devlab/backend/internal/comments"
	"devlab/backend/internal/discover"
	"devlab/backend/internal/execstate"
	"devlab/backend/internal/links"
	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
	"devlab/backend/internal/report"
	"devlab/backend/internal/runs"
	"devlab/backend/internal/sched"
	"devlab/backend/internal/statepath"
	"devlab/backend/internal/telemetry"
	"devlab/backend/internal/workspace"
)

const version = "0.2.0"

// Server wires the verifier, the state paths and every passive pool into HTTP handlers.
type Server struct {
	v         *auth.Verifier
	paths     *statepath.Paths
	reposBase string

	links        *links.Store // nil when no encryption key is configured (dev sandbox)
	workspaces   *workspace.Manager
	comments     *comments.Store
	chats        *chats.Store
	runs         *runs.Store
	results      *runs.ResultStore
	runPRs       *runs.PRStore
	runNotices   *runs.NoticeStore
	runQuestions *runs.QuestionStore
	deliveries   *runs.DeliveryStore
	prodState    *runs.ProdStateStore
	attachments  *runs.AttachmentStore
	axiomChecks  *runs.AxiomChecks
	settings     *runs.SettingsStore
	axiomAuthors *axiomauthors.Store
	axioms       *axiomrepo.Store
	reportLedger *report.Ledger
	usage        *telemetry.UsageLedger
	assigner     *autoAssigner

	// sessions is the register of the agent conversations open in THIS process — what makes a
	// running execution followable and speakable-to. It holds nothing that outlives the process:
	// an execution that was running when the daemon stopped is simply no longer open, and its
	// journal says what it managed to say.
	sessions *openSessions

	// Wired in cmd/devlabd once their packages are filled (Welle 1/2); nil-safe until then.
	docs      *execstate.Store
	scheduler *sched.Scheduler
	broker    *live.Broker

	staticDir string // built SPA to serve for non-/api routes ("" ⇒ 404, e.g. dev where vite serves)
}

// New builds a server over the given state root. DEVLAB_REPOS_PATH overrides the sandbox base
// for dev-bypass; DEVLAB_STATIC_DIR overrides the SPA directory.
func New(v *auth.Verifier, paths *statepath.Paths) *Server {
	base := os.Getenv("DEVLAB_REPOS_PATH")
	store, err := links.NewStore(paths)
	if err != nil {
		log.Printf("devlabd: GitHub link store disabled: %v", err)
		store = nil
	}
	cstore, err := comments.NewStore(paths)
	if err != nil {
		log.Printf("devlabd: comments store disabled: %v", err)
		cstore = nil
	}
	chatStore, err := chats.NewStore(paths)
	if err != nil {
		log.Printf("devlabd: chat store disabled: %v", err)
		chatStore = nil
	}
	staticDir := os.Getenv("DEVLAB_STATIC_DIR")
	if staticDir == "" && paths != nil {
		staticDir = paths.Www()
	}
	s := &Server{
		v:            v,
		paths:        paths,
		reposBase:    base,
		links:        store,
		workspaces:   workspace.NewManager(paths),
		comments:     cstore,
		chats:        chatStore,
		runs:         runs.NewStore(paths),
		results:      runs.NewResultStore(paths),
		runPRs:       runs.NewPRStore(paths),
		runNotices:   runs.NewNoticeStore(paths),
		runQuestions: runs.NewQuestionStore(paths),
		deliveries:   runs.NewDeliveryStore(paths),
		prodState:    runs.NewProdStateStore(paths),
		attachments:  runs.NewAttachmentStore(paths),
		axiomChecks:  runs.NewAxiomChecks(paths),
		axiomAuthors: axiomauthors.NewStore(paths),
		reportLedger: report.NewLedger(paths),
		usage:        telemetry.OpenUsage(paths),
		sessions:     newOpenSessions(),
		staticDir:    staticDir,
	}
	// The constitution lives in its own repository. Pushing uses the runner's linked account —
	// the same identity the autonomous chain commits with — so an edit works whether or not the
	// person making it has linked GitHub themselves.
	//
	// Without a configured namespace there IS no constitution repository (discover.ErrOwnerUnset):
	// the store stays nil, every operation on it answers axiomrepo.ErrNoStore ("constitution store
	// not configured") — distinguishable from "no axioms" and from "unreachable" (REQ-001) — and the
	// named reason is logged once here. Naming one anyway would clone whatever "/axioms" resolves to
	// on this host.
	if axiomsFull, err := axiomsRepo(); err != nil {
		log.Printf("devlabd: no constitution repository configured: %v", err)
	} else {
		s.axioms = axiomrepo.New(axiomsDir(paths), axiomsFull, func() (string, error) {
			if s.links == nil {
				return "", errors.New("no GitHub link store configured")
			}
			return s.links.Token(axiomsTokenUser())
		})
	}
	// The auto-assigner runs on a caller's forwarded session (like the AI-fill button).
	s.assigner = newAutoAssigner(s)
	// The constitution repository documents itself: seed its README best-effort, create-only.
	if axiomsTokenUser() != "" {
		go func() {
			if err := s.axioms.EnsureReadme(context.Background()); err != nil {
				log.Printf("devlabd: axiom repository README seed skipped: %v", err)
			}
		}()
	}
	return s
}

// SetExecution wires the execution machinery once cmd/devlabd constructed it (Welle 1).
func (s *Server) SetExecution(docs *execstate.Store, scheduler *sched.Scheduler) {
	s.docs = docs
	s.scheduler = scheduler
}

// SetBroker wires the SSE broker (Welle 1, B7).
func (s *Server) SetBroker(b *live.Broker) { s.broker = b }

// SetSettings wires the settings store (constructed in cmd/devlabd with its env seed).
func (s *Server) SetSettings(set *runs.SettingsStore) { s.settings = set }

// RunStore and ResultStore hand out the pools the scheduler reads through. They are handed OUT
// rather than re-opened: two handles over one file would mean two mutexes over one truth, so the
// pool's owner stays the single access point (atomic access, single source of truth).
func (s *Server) RunStore() *runs.Store          { return s.runs }
func (s *Server) ResultStore() *runs.ResultStore { return s.results }

// Settings resolves the current service settings — the one runtime source the executor's tuning
// resolution reads. Without a wired pool the zero settings apply (defaults resolve downstream).
func (s *Server) Settings() (runs.Settings, error) {
	if s.settings == nil {
		return runs.Settings{}, nil
	}
	return s.settings.Get()
}

// publish notifies the SSE broker after a successful write — nil-safe (pools stay passive;
// the publisher is always the caller).
func (s *Server) publish(t live.Topic) {
	if s.broker != nil {
		s.broker.Publish(t)
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

// Handler returns the routed handler — THE route table (ARCHITEKTUR §7; one place, frozen
// after Welle 0). Guards: authed = session · guard = +hp_devlab_access · write =
// guard+CSRF+GitHub link · csrf = guard+CSRF without link. Bearer is equivalent in all four
// (CSRF applies only on the cookie path — B11 activates the bearer entry point).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Identity + session.
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/user", s.guardAuthed(s.user))
	mux.HandleFunc("POST /api/auth/refresh", s.refresh)

	// GitHub linking.
	mux.HandleFunc("GET /api/github/authorize", s.guard(s.githubAuthorize))
	mux.HandleFunc("GET /api/github/callback", s.guard(s.githubCallback))
	mux.HandleFunc("POST /api/github/unlink", s.guardWrite(s.githubUnlink))

	// Repos: read tier.
	mux.HandleFunc("GET /api/repos", s.guard(s.repos))
	mux.HandleFunc("GET /api/repos/{id}", s.guard(s.repoData))
	mux.HandleFunc("GET /api/repos/{id}/file", s.guard(s.file))
	mux.HandleFunc("GET /api/repos/{id}/diff", s.guard(s.diff))
	mux.HandleFunc("GET /api/repos/{id}/raw", s.guard(s.visionRaw))

	// Repos: write loop.
	mux.HandleFunc("POST /api/repos/{id}/ensure", s.guardWrite(s.gitEnsure))
	mux.HandleFunc("POST /api/repos/{id}/file", s.guardWrite(s.gitWriteFile))
	mux.HandleFunc("POST /api/repos/{id}/stage", s.guardWrite(s.gitStage))
	mux.HandleFunc("POST /api/repos/{id}/unstage", s.guardWrite(s.gitUnstage))
	mux.HandleFunc("POST /api/repos/{id}/commit", s.guardWrite(s.gitCommit))
	mux.HandleFunc("POST /api/repos/{id}/push", s.guardWrite(s.gitPush))
	mux.HandleFunc("POST /api/repos/{id}/pull", s.guardWrite(s.gitPull))
	mux.HandleFunc("POST /api/repos/{id}/branch", s.guardWrite(s.gitBranch))
	mux.HandleFunc("POST /api/repos/{id}/checkout", s.guardWrite(s.gitCheckout))
	// The IDE PR route rides the ONE PR path: deliver.OpenOrAdoptPR, non-ledger branch (B-1).
	mux.HandleFunc("POST /api/repos/{id}/pr", s.guardWrite(s.openPR))

	// Vision catalog + threaded comments.
	mux.HandleFunc("GET /api/repos/{id}/vision", s.guard(s.visionList))
	mux.HandleFunc("POST /api/repos/{id}/vision/upload", s.guardWrite(s.visionUpload))
	mux.HandleFunc("POST /api/repos/{id}/vision/delete", s.guardWrite(s.visionDelete))
	mux.HandleFunc("GET /api/repos/{id}/comments", s.guard(s.commentsList))
	mux.HandleFunc("POST /api/repos/{id}/comments", s.guardWrite(s.commentAdd))
	mux.HandleFunc("DELETE /api/repos/{id}/comments/{cid}", s.guardWrite(s.commentDelete))

	// AI assistant + agent.
	mux.HandleFunc("POST /api/repos/{id}/assistant", s.guardWrite(s.assistant))
	mux.HandleFunc("GET /api/repos/{id}/assistant/history", s.guard(s.getHistory))
	mux.HandleFunc("PUT /api/repos/{id}/assistant/history", s.guardWrite(s.putHistory))
	mux.HandleFunc("GET /api/assistant/models", s.guard(s.assistantModels))
	mux.HandleFunc("POST /api/repos/{id}/agent", s.guardWrite(s.agent))

	// Terminal (WS; Δ `session` query param for reattach after a reload).
	mux.HandleFunc("GET /api/repos/{id}/pty", s.guard(s.pty))

	// Atlas.
	mux.HandleFunc("GET /api/atlas", s.guard(s.atlasGraph))
	mux.HandleFunc("GET /api/atlas/ports", s.guard(s.atlasPorts))

	// Mercury: constitution.
	mux.HandleFunc("GET /api/mercury/tree", s.guard(s.mercuryTree))
	mux.HandleFunc("GET /api/mercury/item", s.guard(s.mercuryItem))
	mux.HandleFunc("POST /api/mercury/axiom", s.guardCSRF(s.addAxiom))
	mux.HandleFunc("PUT /api/mercury/axiom", s.guardCSRF(s.editAxiom))
	mux.HandleFunc("DELETE /api/mercury/axiom", s.guardCSRF(s.deleteAxiom))
	mux.HandleFunc("POST /api/mercury/move", s.guardCSRF(s.moveAxiom))
	mux.HandleFunc("POST /api/mercury/move-category", s.guardCSRF(s.moveCategory))
	mux.HandleFunc("POST /api/mercury/reorder", s.guardCSRF(s.mercuryReorder))
	mux.HandleFunc("POST /api/mercury/optimize", s.guardCSRF(s.optimizeRecord))
	mux.HandleFunc("POST /api/mercury/conform", s.guardCSRF(s.conformRecord))
	mux.HandleFunc("POST /api/mercury/chat", s.guardCSRF(s.mercuryChat))

	// Mercury: tasks & runs (one machinery, two kinds).
	mux.HandleFunc("GET /api/mercury/runs", s.guard(s.runsList))
	mux.HandleFunc("POST /api/mercury/runs", s.guardCSRF(s.runCreate))
	mux.HandleFunc("GET /api/mercury/runs/coverage", s.guard(s.runsCoverage))
	mux.HandleFunc("GET /api/mercury/runs/history", s.guard(s.runsHistoryList))
	mux.HandleFunc("GET /api/mercury/runs/calendar", s.guard(s.runsCalendar))
	mux.HandleFunc("GET /api/mercury/runs/executions", s.guard(s.runsExecutions))
	mux.HandleFunc("GET /api/mercury/runs/notices", s.guard(s.runsNoticesList))
	mux.HandleFunc("GET /api/mercury/runs/report-status", s.guard(s.runsReportStatus))
	// The same access point in the writing direction: the explicit resumption of a BLOCKED report
	// delivery (K-5). One access, two verbs — not a second, similar sibling.
	mux.HandleFunc("POST /api/mercury/runs/report-status", s.guardCSRF(s.runsReportStatus))
	// Δ Executions & slots (S7/S13).
	mux.HandleFunc("GET /api/mercury/runs/active", s.guard(s.runActive))
	mux.HandleFunc("GET /api/mercury/runs/slots", s.guard(s.runSlots))
	mux.HandleFunc("GET /api/mercury/runs/{id}", s.guard(s.runGet))
	mux.HandleFunc("PUT /api/mercury/runs/{id}", s.guardCSRF(s.runUpdate))
	mux.HandleFunc("DELETE /api/mercury/runs/{id}", s.guardCSRF(s.runDelete))
	mux.HandleFunc("GET /api/mercury/runs/{id}/prompt", s.guard(s.runPromptPreview))
	mux.HandleFunc("GET /api/mercury/runs/{id}/results", s.guard(s.runResultsList))
	mux.HandleFunc("GET /api/mercury/runs/{id}/results/{rid}", s.guard(s.runResultGet))
	// The session an execution works in — read and written on ONE path, behind the TWO rights it
	// takes: following it is not steering it.
	mux.HandleFunc("GET /api/mercury/runs/{id}/results/{rid}/session", s.guardSessionWatch(s.runSessionRead))
	mux.HandleFunc("POST /api/mercury/runs/{id}/results/{rid}/session", s.guardSessionSpeak(s.runSessionSpeak))
	mux.HandleFunc("POST /api/mercury/runs/{id}/run", s.guardCSRF(s.runNow))
	mux.HandleFunc("POST /api/mercury/runs/{id}/cancel", s.guardCSRF(s.runCancel))
	mux.HandleFunc("POST /api/mercury/runs/{id}/defer", s.guardCSRF(s.runDefer))
	mux.HandleFunc("POST /api/mercury/runs/{id}/resume", s.guardCSRF(s.runResume))
	// The EXPLICIT trigger of the automatic assignment (REQ-004): the same pass an axiom write kicks,
	// startable on demand — a state that never passed a write path has nothing left to kick it.
	mux.HandleFunc("POST /api/mercury/runs/assign", s.guardCSRF(s.runsAssign))
	mux.HandleFunc("POST /api/mercury/runs/ai-fill", s.guardCSRF(s.runsAiFill))
	mux.HandleFunc("POST /api/mercury/runs/ai-finetune", s.guardCSRF(s.runsAiFinetune))
	mux.HandleFunc("POST /api/mercury/runs/apply-proposal", s.guardCSRF(s.runsApplyProposal))
	mux.HandleFunc("POST /api/mercury/runs/history/restore", s.guardCSRF(s.runsHistoryRestore))
	mux.HandleFunc("POST /api/mercury/runs/notices/dismiss", s.guardCSRF(s.runsNoticeDismiss))
	mux.HandleFunc("POST /api/mercury/runs/notices/clear", s.guardCSRF(s.runsNoticesClear))

	// Mercury: the Blocked surface — every run stopped on an open question, and the answer that
	// resumes it.
	mux.HandleFunc("GET /api/mercury/runs/questions", s.guard(s.runsQuestionsList))
	mux.HandleFunc("POST /api/mercury/runs/questions/answer", s.guardCSRF(s.runsQuestionAnswer))

	// Mercury: deliveries (S10).
	mux.HandleFunc("GET /api/mercury/runs/deliveries", s.guard(s.runDeliveriesList))
	mux.HandleFunc("POST /api/mercury/runs/deliveries/{id}/rollback", s.guardCSRF(s.runDeliveryRollback))
	// The explicit resume a blocked pull request waits for (K-5) — the state existed, the way out did not.
	mux.HandleFunc("POST /api/mercury/runs/deliveries/resume", s.guardCSRF(s.runPRResume))
	mux.HandleFunc("POST /api/mercury/runs/reset", s.guardCSRF(s.runRepoReset))

	// Todo media.
	mux.HandleFunc("POST /api/mercury/runs/{id}/attachments", s.guardCSRF(s.runAttachmentUpload))
	mux.HandleFunc("GET /api/mercury/runs/{id}/attachments/{aid}/raw", s.guard(s.runAttachmentRaw))
	mux.HandleFunc("DELETE /api/mercury/runs/{id}/attachments/{aid}", s.guardCSRF(s.runAttachmentDelete))

	// Δ The ONE SSE stream (S12): guard, no CSRF (read-only), heartbeat inside the handler.
	mux.HandleFunc("GET /api/events", s.guard(s.events))

	// Δ Service interfaces (cross-cutting 5; convention fixed once, B-14): config, telemetry,
	// storage, AI usage.
	mux.HandleFunc("GET /api/service/config", s.guard(s.serviceConfigGet))
	mux.HandleFunc("PUT /api/service/config", s.guardCSRF(s.serviceConfigPut))
	mux.HandleFunc("GET /api/service/telemetry", s.guard(s.serviceTelemetry))
	mux.HandleFunc("GET /api/service/storage", s.guard(s.serviceStorage))
	mux.HandleFunc("GET /api/service/ai-usage", s.guard(s.serviceAiUsage))

	// Δ MCP (REQ-043): the tool table with per-tool rights coverage lives in handlers_mcp.go.
	// The endpoint carries the CSRF-enforcing guard like every other mutating route — an agent
	// authenticates by bearer, where the double submit does not apply, so the uniform tier costs
	// the protocol nothing and leaves no route reachable cross-site with the caller's cookies.
	// Per-tool CSRF stays in force inside the handler (a reading tool still needs it here).
	mux.HandleFunc("POST /api/mcp", s.guardCSRF(s.mcpEndpoint))

	// GET /ready exists ONLY on the Unix socket (ready.go) — never in this TCP mux (A2-7).

	// Unknown /api paths 404 as JSON; everything else serves the built SPA.
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

// serveSPA serves a file from staticDir, falling back to index.html for client-side routes.
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

// resolveUser is the ONE user-resolution entry of the guards: the session cookie OR an
// `Authorization: Bearer` token, verified by the same verifier (auth.FromRequest, D 34). The
// entry point sits here so every guard resolves identity the one same way.
func (s *Server) resolveUser(r *http.Request) (*auth.User, error) {
	return s.v.FromRequest(r)
}

// guardAuthed requires a valid Holistic session and stashes the resolved user on the request.
func (s *Server) guardAuthed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.resolveUser(r)
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

// guardSessionWatch gates READING the session a run works in; guardSessionSpeak gates WRITING
// into it (and carries the CSRF check every mutating request carries). They are two guards
// because they are two rights: the first one alone never opens the second.
func (s *Server) guardSessionWatch(h http.HandlerFunc) http.HandlerFunc {
	return s.guard(func(w http.ResponseWriter, r *http.Request) {
		if u := userFrom(r); u == nil || !u.CanWatchSession() {
			writeErr(w, http.StatusForbidden, "Following a run's session requires the hp_devlab_session_watch right")
			return
		}
		h(w, r)
	})
}

func (s *Server) guardSessionSpeak(h http.HandlerFunc) http.HandlerFunc {
	return s.guardCSRF(func(w http.ResponseWriter, r *http.Request) {
		if u := userFrom(r); u == nil || !u.CanSpeakInSession() {
			writeErr(w, http.StatusForbidden, "Writing into a run's session requires the hp_devlab_session_speak right")
			return
		}
		h(w, r)
	})
}

// checkCSRF applies the double-submit check on the cookie path. On a bearer request the check is
// waived — a bearer header cannot be sent cross-site by a browser form (D 34).
func (s *Server) checkCSRF(r *http.Request) bool {
	return auth.BearerPresented(r) || s.v.CheckCSRF(r)
}

// guardWrite gates mutating requests: a DevLab session (guard) + CSRF + a linked GitHub
// account. Per-repo GitHub permission is enforced inside the write handlers.
func (s *Server) guardWrite(h http.HandlerFunc) http.HandlerFunc {
	return s.guard(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkCSRF(r) {
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

// guardCSRF gates a mutating request that does NOT require the CALLER to have linked GitHub:
// where such a write reaches GitHub (the constitution repo) it is pushed server-side with the
// runner's own linked account, never the caller's.
func (s *Server) guardCSRF(h http.HandlerFunc) http.HandlerFunc {
	return s.guard(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkCSRF(r) {
			writeErr(w, http.StatusForbidden, "Invalid CSRF token")
			return
		}
		h(w, r)
	})
}

// githubLinked reports whether the user has a usable GitHub link. Dev-bypass is treated as
// linked (the sandbox operates on local clones with no per-user token).
func (s *Server) githubLinked(u *auth.User) bool {
	if s.v.DevBypass() {
		return true
	}
	return s.links != nil && u != nil && s.links.Linked(u.Username)
}

// userToken returns the user's decrypted GitHub OAuth token (server-side GitHub calls only).
func (s *Server) userToken(u *auth.User) (string, error) {
	if s.links == nil || u == nil {
		return "", errNoLink
	}
	return s.links.Token(u.Username)
}

var errNoLink = errors.New("no github link")

// health is deliberately MINIMAL (A1-5): no operational internals on the public endpoint.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, model.Health{OK: true, Mode: "devlab/" + version})
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

// user returns the caller's identity, DevLab access, and GitHub-link status — the SPA's
// bootstrap probe.
func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	ghLogin := ""
	if s.links != nil {
		if l, err := s.links.Get(u.Username); err == nil && l != nil {
			ghLogin = l.GHLogin
		}
	}
	writeJSON(w, http.StatusOK, model.User{
		Username:     u.Username,
		DisplayName:  auth.DisplayName(u.Username),
		IsAdmin:      u.IsAdmin,
		CanUseDevlab: u.CanUseDevlab(),
		// The surface must know both session rights up front: a viewer without the write right is
		// shown the session WITHOUT an input, instead of an input that is refused when used.
		CanWatchSession: u.CanWatchSession(),
		CanSpeakSession: u.CanSpeakInSession(),
		GithubLinked:    s.githubLinked(u),
		GithubLogin:     ghLogin,
	})
}

// repoPath resolves {id} to the working tree the caller operates on.
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

// resolveFullName maps a repo id to its GitHub owner/repo from the user's visible set.
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

// requireRealGitHub rejects a feature that needs a real per-user GitHub account under
// dev-bypass. `feature` names the plural subject, e.g. "Pull requests".
func (s *Server) requireRealGitHub(w http.ResponseWriter, feature string) bool {
	if s.v.DevBypass() {
		writeErr(w, http.StatusBadRequest, feature+" need a linked GitHub account")
		return false
	}
	return true
}

// requirePrompt writes a 400 and returns false when the prompt is blank.
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
