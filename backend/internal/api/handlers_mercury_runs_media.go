package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/runs"
)

// Konkrete ToDos can carry media — images and documents — that the AI must take into account while
// implementing the task. The bytes live in a passive pool (runs.AttachmentStore); the metadata lives
// inline on the ToDo (runs.Attachment) as the single source of truth for which attachments exist. This
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

// decodeAttachmentBody decodes the upload JSON with the attachment-sized body cap (see above).
func decodeAttachmentBody(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, maxAttachmentBodyBytes)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "Ungültiger Anfrage-Body")
		return false
	}
	return true
}

// Sentinel validation failures a Patch closure raises so the handler can map them to a precise status.
var (
	errNotTodo             = errors.New("Medien können nur an ToDos angehängt werden")
	errDupAttachment       = errors.New("eine Datei mit diesem Namen ist bereits angehängt")
	errTooManyAttachments  = errors.New("zu viele Anhänge an diesem ToDo")
	errAttachmentsTooLarge = errors.New("die Anhänge dieses ToDos überschreiten das Gesamtlimit")
	errAttachmentNotFound  = errors.New("Kein Anhang mit dieser id")
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

// recomposeTodoPrompt refreshes a ToDo's stored prompt snapshot from its task + current attachments so
// the Prompt-Vorschau stays truthful after an attachment change. A ToDo has no scheme inputs, so this
// needs no catalog (it mirrors composeInto's todo branch).
func recomposeTodoPrompt(run *runs.Run, now time.Time) {
	run.Prompt = mercury.ComposeTodoPrompt(run.Name, run.Task, "", todoAttachmentDescriptors(run.Attachments))
	run.PromptAt = now
	run.PromptHash = ""
}

// runAttachmentUpload attaches one uploaded medium (base64 in the JSON body) to a ToDo: it stores the
// bytes in the passive pool, then records the metadata on the ToDo and recomposes its prompt snapshot.
func (s *Server) runAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.attachments == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
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
		writeErr(w, http.StatusBadRequest, "Dateiname fehlt oder ist ungültig")
		return
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.ContentB64))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Ungültiger Base64-Inhalt")
		return
	}
	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, "Leere Datei")
		return
	}
	if len(data) > maxAttachmentBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "Datei zu groß (max. 25 MiB)")
		return
	}

	att := runs.Attachment{
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
		writeErr(w, http.StatusInternalServerError, "Anhang konnte nicht gespeichert werden")
		return
	}

	now := time.Now().UTC()
	var updated runs.Run
	// Patch, not Mutate: an attachment is task data, not part of the axiom-config lineage the restore
	// history is for — recording it there would both bloat the history and let a restore resurrect a
	// blob that was since deleted.
	if _, perr := s.runs.Patch(func(cur []runs.Run) ([]runs.Run, error) {
		idx := indexOfRun(cur, id)
		if idx < 0 {
			return nil, runs.ErrNotFound
		}
		if !cur[idx].IsTodo() {
			return nil, errNotTodo
		}
		if len(cur[idx].Attachments) >= maxAttachmentsPerTodo {
			return nil, errTooManyAttachments
		}
		var total int64
		for _, a := range cur[idx].Attachments {
			if strings.EqualFold(a.Filename, name) {
				return nil, errDupAttachment
			}
			total += a.Size
		}
		if total+att.Size > maxAttachmentsTotalBytes {
			return nil, errAttachmentsTooLarge
		}
		cur[idx].Attachments = append(cur[idx].Attachments, att)
		cur[idx].UpdatedAt = now
		recomposeTodoPrompt(&cur[idx], now)
		updated = cur[idx]
		return cur, nil
	}); perr != nil {
		_ = s.attachments.Delete(id, att.ID) // roll back the orphaned blob
		s.writeAttachmentErr(w, perr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attachments": updated.Attachments})
}

// runAttachmentRaw streams one attachment's real bytes with its stored content type. Images and PDFs
// are served inline (so a click previews them); anything else is a download, so a crafted document
// (e.g. an SVG or HTML file) can never execute inline in this origin.
func (s *Server) runAttachmentRaw(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil || s.attachments == nil {
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
		return
	}
	id, aid := r.PathValue("id"), r.PathValue("aid")
	run, ok, err := s.runs.Get(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Lauf konnte nicht gelesen werden")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
		return
	}
	var meta *runs.Attachment
	for i := range run.Attachments {
		if run.Attachments[i].ID == aid {
			meta = &run.Attachments[i]
			break
		}
	}
	if meta == nil {
		writeErr(w, http.StatusNotFound, "Kein Anhang mit dieser id")
		return
	}
	data, err := s.attachments.Get(id, aid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Anhang nicht gefunden")
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
		writeErr(w, http.StatusServiceUnavailable, "Läufe-Store nicht verfügbar")
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
		next := make([]runs.Attachment, 0, len(cur[idx].Attachments))
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
		cur[idx].UpdatedAt = now
		recomposeTodoPrompt(&cur[idx], now)
		updated = cur[idx]
		return cur, nil
	}); perr != nil {
		s.writeAttachmentErr(w, perr)
		return
	}
	_ = s.attachments.Delete(id, aid) // metadata gone; drop the bytes (harmless if already absent)
	writeJSON(w, http.StatusOK, map[string]any{"attachments": updated.Attachments})
}

// writeAttachmentErr maps a Patch sentinel to its HTTP status + German message.
func (s *Server) writeAttachmentErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runs.ErrNotFound):
		writeErr(w, http.StatusNotFound, "Kein Lauf mit dieser id")
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
		writeErr(w, http.StatusInternalServerError, "Anhang konnte nicht gespeichert werden")
	}
}
