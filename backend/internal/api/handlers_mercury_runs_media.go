package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/runs"
)

// Konkrete ToDos can carry media — images and documents — that the AI must take into account while
// implementing the task. The bytes live in a passive pool (runs.AttachmentStore); the metadata lives
// inline on the ToDo (runs.AttachmentRef) as the single source of truth for which attachments exist. This
// file is the management surface: attach, download the raw bytes (for preview/download), and remove.
// The executor materializes the pool into the agent's workspace at run time and references it in the
// prompt (see handlers_mercury_runs_exec.go), which is where "considered by the AI" actually happens.

const (
	maxAttachmentBytes       = 25 << 20 // 25 MiB per file — matches the workspace write cap (WriteFileBytes)
	maxAttachmentsPerTodo    = 20       // a ToDo is a focused task, not a media library
	maxAttachmentsTotalBytes = 60 << 20 // ceiling on the media a single ToDo may carry
	maxAttachmentNameLen     = 200
	// maxAttachmentBodyBytes bounds the JSON upload body. A 25 MiB file is ~33.4 MiB of base64, so the
	// body cap must exceed that — the shared decodeJSON caps at 8 MiB and would silently truncate a real
	// upload, so this handler decodes with its own, larger limit.
	maxAttachmentBodyBytes = 40 << 20
)

// decodeAttachmentBody decodes the upload JSON with the attachment-sized body cap (see above),
// reusing the shared size-capped decoder.
func decodeAttachmentBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONLimit(w, r, v, maxAttachmentBodyBytes, "Invalid request body")
}

// Sentinel validation failures a Patch closure raises so the handler can map them to a precise status.
var (
	errNotTodo             = errors.New("media can only be attached to todos")
	errDupAttachment       = errors.New("a file with this name is already attached")
	errTooManyAttachments  = errors.New("too many attachments on this todo")
	errAttachmentsTooLarge = errors.New("this todo's attachments exceed the total limit")
	errAttachmentNotFound  = errors.New("no attachment with this id")
)

// sanitizeAttachmentName reduces a client filename to a single safe path segment: base name only, no
// separators or traversal, no control characters, trimmed and length-bounded. The workspace SafePath
// guard re-validates it again at write time; this keeps the stored name and its `.mercury/attachments/`
// reference clean and predictable.
func sanitizeAttachmentName(name string) string {
	name = filepath.Base(filepath.FromSlash(strings.TrimSpace(name))) // strip any directory part
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "." || name == ".." {
		return ""
	}
	if len(name) > maxAttachmentNameLen {
		name = name[:maxAttachmentNameLen]
	}
	return name
}

// resolveMIME determines a stored attachment's content type from its extension, falling back to a
// content sniff (so an extensionless file still gets a usable type).
func resolveMIME(name string, data []byte) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return http.DetectContentType(data)
}

// recomposeTodoPrompt refreshes a todo's stored prompt snapshot from its task + current
// attachments so the prompt preview stays truthful after an attachment change (REQ-003:
// every write path recomposes; the todo branch of the ONE composition path).
func recomposeTodoPrompt(run *runs.Run) {
	runs.ComposeInto(run, runs.Catalog{})
}

// runAttachmentUpload attaches one uploaded medium (base64 in the JSON body) to a ToDo: it stores the
// bytes in the passive pool, then records the metadata on the ToDo and recomposes its prompt snapshot.
func (s *Server) runAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.attachments == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	id := r.PathValue("id")
	var body struct {
		Filename   string `json:"filename"`
		ContentB64 string `json:"contentB64"`
	}
	if !decodeAttachmentBody(w, r, &body) {
		return
	}
	name := sanitizeAttachmentName(body.Filename)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "Filename missing or invalid")
		return
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.ContentB64))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid base64 content")
		return
	}
	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, "Empty file")
		return
	}
	if len(data) > maxAttachmentBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "File too large (max. 25 MiB)")
		return
	}

	att := runs.AttachmentRef{
		ID:         runs.NewAttachmentID(),
		Filename:   name,
		MIME:       resolveMIME(name, data),
		Size:       int64(len(data)),
		UploadedAt: time.Now().UTC(),
		UploadedBy: actor(r),
	}
	sum := sha256.Sum256(data)
	att.SHA256 = hex.EncodeToString(sum[:])

	// Write the bytes FIRST so the recorded metadata never dangles over a missing blob; the Patch below
	// re-checks all invariants (still a ToDo, capacity, unique name) atomically under the store lock.
	if err := s.attachments.Put(id, att.ID, data); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not store the attachment")
		return
	}

	now := time.Now().UTC()
	var updated runs.Run
	deduped := false
	// Patch, not Mutate: an attachment is task data, not part of the axiom-config lineage the restore
	// history is for — recording it there would both bloat the history and let a restore resurrect a
	// blob that was since deleted.
	if _, perr := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		idx := indexOfRun(cur, id)
		if idx < 0 {
			return nil, runs.ErrNotFound
		}
		if cur[idx].Kind != "todo" {
			return nil, errNotTodo
		}
		var total int64
		for _, a := range cur[idx].Attachments {
			// SHA dedup (REQ-007): the same bytes attached twice — a double paste, or dialog
			// then paste — coalesce to the one existing attachment instead of a duplicate row.
			if a.SHA256 != "" && a.SHA256 == att.SHA256 {
				deduped, updated = true, cur[idx]
				return cur, nil
			}
			if strings.EqualFold(a.Filename, name) {
				return nil, errDupAttachment
			}
			total += a.Size
		}
		if len(cur[idx].Attachments) >= maxAttachmentsPerTodo {
			return nil, errTooManyAttachments
		}
		if total+att.Size > maxAttachmentsTotalBytes {
			return nil, errAttachmentsTooLarge
		}
		cur[idx].Attachments = append(cur[idx].Attachments, att)
		cur[idx].Authorship.UpdatedAt = now
		recomposeTodoPrompt(&cur[idx])
		updated = cur[idx]
		return cur, nil
	}); perr != nil {
		_ = s.attachments.Delete(id, att.ID) // roll back the orphaned blob
		s.writeAttachmentErr(w, perr)
		return
	}
	if deduped {
		_ = s.attachments.Delete(id, att.ID) // the freshly written blob is redundant — same bytes exist
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": updated.Attachments})
}

// runAttachmentRaw streams one attachment's real bytes with its stored content type. Images and PDFs
// are served inline (so a click previews them); anything else is a download, so a crafted document
// (e.g. an SVG or HTML file) can never execute inline in this origin.
func (s *Server) runAttachmentRaw(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.attachments == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	id, aid := r.PathValue("id"), r.PathValue("aid")
	run, ok, err := s.runs.Get(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not read the run")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "No run with this id")
		return
	}
	var meta *runs.AttachmentRef
	for i := range run.Attachments {
		if run.Attachments[i].ID == aid {
			meta = &run.Attachments[i]
			break
		}
	}
	if meta == nil {
		writeErr(w, http.StatusNotFound, errAttachmentNotFound.Error())
		return
	}
	data, err := s.attachments.Get(id, aid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Attachment not found")
		return
	}
	if meta.MIME != "" {
		w.Header().Set("Content-Type", meta.MIME)
	}
	// nosniff is set globally; a correct Content-Type above is what lets the browser render an image/PDF.
	disp := "attachment"
	if inlineSafeMIME(meta.MIME) {
		disp = "inline"
	}
	w.Header().Set("Content-Disposition", disp+"; filename="+strconv.Quote(meta.Filename))
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(data)
}

// inlineSafeMIME is the small allowlist of types safe to render inline (they carry no active content).
func inlineSafeMIME(m string) bool {
	if i := strings.IndexByte(m, ';'); i >= 0 { // drop any "; charset=…" parameter
		m = m[:i]
	}
	switch strings.TrimSpace(m) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf":
		return true
	}
	return false
}

// runAttachmentDelete removes an attachment from a ToDo: it drops the metadata (and recomposes the
// prompt) first, then deletes the bytes — so a listed attachment is always backed by a blob.
func (s *Server) runAttachmentDelete(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.attachments == nil {
		writeErr(w, http.StatusServiceUnavailable, "Run store unavailable")
		return
	}
	id, aid := r.PathValue("id"), r.PathValue("aid")
	now := time.Now().UTC()
	var updated runs.Run
	if _, perr := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		idx := indexOfRun(cur, id)
		if idx < 0 {
			return nil, runs.ErrNotFound
		}
		next := make([]runs.AttachmentRef, 0, len(cur[idx].Attachments))
		found := false
		for _, a := range cur[idx].Attachments {
			if a.ID == aid {
				found = true
				continue
			}
			next = append(next, a)
		}
		if !found {
			return nil, errAttachmentNotFound // abort before any write — a no-op must not rewrite the file
		}
		cur[idx].Attachments = next
		cur[idx].Authorship.UpdatedAt = now
		recomposeTodoPrompt(&cur[idx])
		updated = cur[idx]
		return cur, nil
	}); perr != nil {
		s.writeAttachmentErr(w, perr)
		return
	}
	_ = s.attachments.Delete(id, aid) // metadata gone; drop the bytes (harmless if already absent)
	writeJSON(w, http.StatusOK, map[string]any{"attachments": updated.Attachments})
}

// writeAttachmentErr maps a Patch sentinel to its HTTP status + message.
func (s *Server) writeAttachmentErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runs.ErrNotFound):
		writeErr(w, http.StatusNotFound, "No run with this id")
	case errors.Is(err, errAttachmentNotFound):
		writeErr(w, http.StatusNotFound, errAttachmentNotFound.Error())
	case errors.Is(err, errNotTodo):
		writeErr(w, http.StatusBadRequest, errNotTodo.Error())
	case errors.Is(err, errDupAttachment):
		writeErr(w, http.StatusConflict, errDupAttachment.Error())
	case errors.Is(err, errTooManyAttachments):
		writeErr(w, http.StatusBadRequest, errTooManyAttachments.Error())
	case errors.Is(err, errAttachmentsTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, errAttachmentsTooLarge.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "Could not store the attachment")
	}
}
