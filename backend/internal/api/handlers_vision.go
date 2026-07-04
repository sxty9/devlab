package api

import (
	"encoding/base64"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"devlab/backend/internal/auth"
	"devlab/backend/internal/comments"
	"devlab/backend/internal/git"
	"devlab/backend/internal/model"
	"devlab/backend/internal/workspace"
)

// visionPrefix confines the Vision Catalog (list/raw/upload) to the repo's vision/ subtree.
const visionPrefix = "vision/"

// cleanRel normalizes a repo-relative path from a query/body (anchored, no leading slash).
func cleanRel(rel string) string {
	return strings.TrimPrefix(path.Clean("/"+rel), "/")
}

// visionList returns the classified files in the repo's vision/ folder.
func (s *Server) visionList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, git.VisionFiles(p))
}

// visionRaw streams a vision file's real bytes with a proper MIME type (for image/PDF viewers).
// Confined to vision/, traversal/symlink-guarded, size is bounded by the on-disk file.
func (s *Server) visionRaw(w http.ResponseWriter, r *http.Request) {
	wt, ok := s.repoPath(w, r)
	if !ok {
		return
	}
	rel := cleanRel(r.URL.Query().Get("path"))
	if !strings.HasPrefix(rel, visionPrefix) {
		writeErr(w, http.StatusForbidden, "Only vision/ files are served raw")
		return
	}
	abs, err := workspace.SafePath(wt, rel)
	if err != nil {
		writeErr(w, http.StatusForbidden, "Path refused")
		return
	}
	if ct := mime.TypeByExtension(path.Ext(rel)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// nosniff is set globally; a correct Content-Type above is required for the browser to render.
	http.ServeFile(w, r, abs)
}

// visionUpload writes an uploaded (base64) asset into vision/ and returns the refreshed listing.
func (s *Server) visionUpload(w http.ResponseWriter, r *http.Request) {
	wc, unlock, ok := s.mutateCtx(w, r, true)
	if !ok {
		return
	}
	defer unlock()
	var body struct {
		Path      string `json:"path"`
		ContentB64 string `json:"contentB64"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	rel := cleanRel(body.Path)
	if !strings.HasPrefix(rel, visionPrefix) || rel == visionPrefix {
		writeErr(w, http.StatusBadRequest, "Uploads must target vision/")
		return
	}
	data, err := base64.StdEncoding.DecodeString(body.ContentB64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid base64 content")
		return
	}
	if err := workspace.WriteFileBytes(wc.wt, rel, data); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, git.VisionFiles(wc.wt))
}

// visionDelete removes a vision file from the working tree and returns the refreshed listing.
// The removal is shared via the normal stage→commit→push loop (symmetric with upload).
func (s *Server) visionDelete(w http.ResponseWriter, r *http.Request) {
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
	rel := cleanRel(body.Path)
	if !strings.HasPrefix(rel, visionPrefix) || rel == visionPrefix {
		writeErr(w, http.StatusBadRequest, "Only vision/ files can be deleted here")
		return
	}
	if err := workspace.DeleteFile(wc.wt, rel); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, git.VisionFiles(wc.wt))
}

// ─── Comments (DevLab-side threaded store, per repo) ────────────────────────

// commentsList returns the thread for a vision file (or the whole repo when path is empty).
func (s *Server) commentsList(w http.ResponseWriter, r *http.Request) {
	if s.comments == nil {
		writeJSON(w, http.StatusOK, []model.Comment{})
		return
	}
	// Reuse repoPath only to authorize repo visibility; the store is keyed by repo id.
	if _, ok := s.repoPath(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	list, err := s.comments.List(id, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read comments")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// commentAdd stores a new comment/reply, stamping the author from the session (never the client).
func (s *Server) commentAdd(w http.ResponseWriter, r *http.Request) {
	if s.comments == nil {
		writeErr(w, http.StatusServiceUnavailable, "Comments are unavailable")
		return
	}
	if _, ok := s.repoPath(w, r); !ok {
		return
	}
	u := userFrom(r)
	id := r.PathValue("id")
	var body struct {
		Path     string `json:"path"`
		ParentID string `json:"parentId"`
		Body     string `json:"body"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Body) == "" || body.Path == "" {
		writeErr(w, http.StatusBadRequest, "Missing path or body")
		return
	}
	c := model.Comment{
		Path:       cleanRel(body.Path),
		ParentID:   body.ParentID,
		Author:     u.Username,
		AuthorName: auth.DisplayName(u.Username),
		Body:       body.Body,
	}
	saved, err := s.comments.Add(id, c, time.Now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// commentDelete removes a comment (author or admin) and cascades to its replies.
func (s *Server) commentDelete(w http.ResponseWriter, r *http.Request) {
	if s.comments == nil {
		writeErr(w, http.StatusServiceUnavailable, "Comments are unavailable")
		return
	}
	if _, ok := s.repoPath(w, r); !ok {
		return
	}
	u := userFrom(r)
	id := r.PathValue("id")
	cid := r.PathValue("cid")
	if err := s.comments.Delete(id, cid, u.Username, u.IsAdmin); err != nil {
		if err == comments.ErrForbidden {
			writeErr(w, http.StatusForbidden, "You can only delete your own comments")
			return
		}
		writeErr(w, http.StatusInternalServerError, "Could not delete comment")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
