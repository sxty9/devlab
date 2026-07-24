package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/discover"
	"devlab/backend/internal/github"
	"devlab/backend/internal/model"
	"devlab/backend/internal/workspace"
)

const maxBodyBytes = 8 << 20 // 8 MiB request cap (file content ≤2 MiB + slack)

// writeCtx is everything a mutating handler needs once the guards have passed.
type writeCtx struct {
	wt      string // working-tree path
	user    string
	token   string // "" under dev-bypass (ambient git credentials)
	ghLogin string
	ghID    int64
	exec    workspace.Executor // runs mutations as the user (per-user) or directly (dev-bypass)
}

// mutateCtx resolves the working tree and enforces per-repo write authorization (GitHub is the
// source of truth). needPush requires push|admin; otherwise pull (guaranteed by visibility) is
// enough. It returns the per-user-per-repo unlock the caller must defer. Under dev-bypass it
// operates on the local sandbox clone with the operator's ambient git credentials.
func (s *Server) mutateCtx(w http.ResponseWriter, r *http.Request, needPush bool) (*writeCtx, func(), bool) {
	id := r.PathValue("id")
	u := userFrom(r)

	if s.v.DevBypass() {
		p, ok := discover.Path(s.reposBase, id)
		if !ok {
			writeErr(w, http.StatusNotFound, errRepoNotFound)
			return nil, nil, false
		}
		return &writeCtx{wt: p, user: u.Username, exec: workspace.Executor{User: u.Username, PerUser: false}}, func() {}, true
	}

	full, ok := s.resolveFullName(r.Context(), u, id)
	if !ok {
		writeErr(w, http.StatusNotFound, errRepoNotFound)
		return nil, nil, false
	}
	if needPush && !s.canPush(r.Context(), u, id, full) {
		writeErr(w, http.StatusForbidden, "You have read-only access to this repository")
		return nil, nil, false
	}
	token, err := s.userToken(u)
	if err != nil {
		writeErr(w, http.StatusForbidden, errNoGitHubLink)
		return nil, nil, false
	}
	if _, err := s.workspaces.Ensure(r.Context(), u.Username, id, full, token, true); err != nil {
		writeErr(w, http.StatusBadGateway, "Could not prepare the workspace")
		return nil, nil, false
	}
	unlock, err := s.workspaces.Lock(u.Username, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Workspace error")
		return nil, nil, false
	}
	wt, _ := s.workspaces.Path(u.Username, id)
	wc := &writeCtx{wt: wt, user: u.Username, token: token, exec: workspace.Executor{User: u.Username, PerUser: true}}
	if s.links != nil {
		if l, e := s.links.Get(u.Username); e == nil && l != nil {
			wc.ghLogin, wc.ghID = l.GHLogin, l.GHID
		}
	}
	return wc, unlock, true
}

// canPush reports whether the user may push to a repo. The cached permission is the fast path;
// when it says pull/unknown we re-check authoritatively against GitHub before refusing.
func (s *Server) canPush(ctx context.Context, u *auth.User, id, full string) bool {
	if perm, ok := discover.PermissionFor(u.Username, id); ok && (perm == "push" || perm == "admin") {
		return true
	}
	token, err := s.userToken(u)
	if err != nil {
		return false
	}
	perm, err := github.RepoPermission(ctx, token, full)
	if err != nil {
		return false
	}
	return perm == "push" || perm == "admin"
}

// decodeJSON reads a size-capped JSON body into v, writing a 400 on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid request body")
		return false
	}
	return true
}

// gitEnsure clones the repo into the caller's workspace if needed (idempotent).
func (s *Server) gitEnsure(w http.ResponseWriter, r *http.Request) {
	_, unlock, ok := s.mutateCtx(w, r, false)
	if !ok {
		return
	}
	unlock()
	w.WriteHeader(http.StatusNoContent)
}

// gitWriteFile writes editor content to a path and returns the refreshed change set.
func (s *Server) gitWriteFile(w http.ResponseWriter, r *http.Request) {
	wc, unlock, ok := s.mutateCtx(w, r, true)
	if !ok {
		return
	}
	defer unlock()
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Path == "" {
		writeErr(w, http.StatusBadRequest, errMissingPath)
		return
	}
	if err := wc.exec.WriteFile(wc.wt, body.Path, []byte(body.Content)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, model.WriteResult{Changes: workspace.Changes(wc.wt)})
}

func (s *Server) gitStage(w http.ResponseWriter, r *http.Request)   { s.stageOp(w, r, true) }
func (s *Server) gitUnstage(w http.ResponseWriter, r *http.Request) { s.stageOp(w, r, false) }

func (s *Server) stageOp(w http.ResponseWriter, r *http.Request, stage bool) {
	wc, unlock, ok := s.mutateCtx(w, r, true)
	if !ok {
		return
	}
	defer unlock()
	var body struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Path == "" {
		writeErr(w, http.StatusBadRequest, errMissingPath)
		return
	}
	var err error
	if stage {
		err = wc.exec.Stage(r.Context(), wc.wt, body.Path)
	} else {
		err = wc.exec.Unstage(r.Context(), wc.wt, body.Path)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, model.WriteResult{Changes: workspace.Changes(wc.wt)})
}

// gitCommit commits the staged index as the linked GitHub identity.
func (s *Server) gitCommit(w http.ResponseWriter, r *http.Request) {
	wc, unlock, ok := s.mutateCtx(w, r, true)
	if !ok {
		return
	}
	defer unlock()
	var body struct {
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	hash, branch, err := wc.exec.Commit(r.Context(), wc.wt, body.Message, wc.ghLogin, wc.ghID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, model.CommitResult{Hash: hash, Branch: branch, Changes: workspace.Changes(wc.wt)})
}

// gitPush pushes the current branch (requires push); gitPull fast-forwards (requires pull).
func (s *Server) gitPush(w http.ResponseWriter, r *http.Request) {
	wc, unlock, ok := s.mutateCtx(w, r, true)
	if !ok {
		return
	}
	defer unlock()
	msg, err := wc.exec.Push(r.Context(), wc.wt, wc.token)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.writePushResult(w, r, wc, msg)
}

func (s *Server) gitPull(w http.ResponseWriter, r *http.Request) {
	wc, unlock, ok := s.mutateCtx(w, r, false)
	if !ok {
		return
	}
	defer unlock()
	msg, err := wc.exec.Pull(r.Context(), wc.wt, wc.token)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.writePushResult(w, r, wc, msg)
}

func (s *Server) writePushResult(w http.ResponseWriter, r *http.Request, wc *writeCtx, msg string) {
	branch := wc.exec.CurrentBranch(r.Context(), wc.wt)
	branches := workspace.Branches(wc.wt)
	ahead, behind := 0, 0
	for _, b := range branches {
		if b.Name == branch {
			ahead, behind = b.Ahead, b.Behind
		}
	}
	writeJSON(w, http.StatusOK, model.PushResult{Branch: branch, Ahead: ahead, Behind: behind, Message: msg, Branches: branches})
}

// writeBranchResult returns the current branch + refreshed branch list — shared by branch/checkout.
func writeBranchResult(w http.ResponseWriter, r *http.Request, wc *writeCtx) {
	writeJSON(w, http.StatusOK, model.BranchResult{
		Branch:   wc.exec.CurrentBranch(r.Context(), wc.wt),
		Branches: workspace.Branches(wc.wt),
	})
}

// gitBranch creates+checks out a branch; gitCheckout switches to an existing one.
func (s *Server) gitBranch(w http.ResponseWriter, r *http.Request) {
	wc, unlock, ok := s.mutateCtx(w, r, true)
	if !ok {
		return
	}
	defer unlock()
	var body struct {
		Name string `json:"name"`
		From string `json:"from"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := wc.exec.CreateBranch(r.Context(), wc.wt, body.Name, body.From); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeBranchResult(w, r, wc)
}

func (s *Server) gitCheckout(w http.ResponseWriter, r *http.Request) {
	wc, unlock, ok := s.mutateCtx(w, r, false)
	if !ok {
		return
	}
	defer unlock()
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := wc.exec.Checkout(r.Context(), wc.wt, body.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeBranchResult(w, r, wc)
}
