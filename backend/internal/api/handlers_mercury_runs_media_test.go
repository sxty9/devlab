package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

func TestSanitizeAttachmentName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"photo.png", "photo.png"},
		{"  spaced.pdf  ", "spaced.pdf"},
		{"../../etc/passwd", "passwd"},         // traversal collapses to the base name
		{"a/b/c.txt", "c.txt"},                 // nested path → last segment only
		{"weird\x00name.bin", "weirdname.bin"}, // control chars stripped
		{"back\\slash.doc", "back_slash.doc"},  // backslash is not a separator here → neutralized
		{"", ""},
		{".", ""},
		{"..", ""},
	}
	for _, c := range cases {
		if got := sanitizeAttachmentName(c.in); got != c.want {
			t.Errorf("sanitizeAttachmentName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := sanitizeAttachmentName(strings.Repeat("a", maxAttachmentNameLen+50)); len(got) != maxAttachmentNameLen {
		t.Errorf("over-long name not bounded: got len %d, want %d", len(got), maxAttachmentNameLen)
	}
}

func TestInlineSafeMIME(t *testing.T) {
	for _, m := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "image/png; charset=binary"} {
		if !inlineSafeMIME(m) {
			t.Errorf("inlineSafeMIME(%q) = false, want true", m)
		}
	}
	// Active-content types (SVG can carry script, HTML executes) must never render inline.
	for _, m := range []string{"image/svg+xml", "text/html", "application/octet-stream", "text/plain", ""} {
		if inlineSafeMIME(m) {
			t.Errorf("inlineSafeMIME(%q) = true, want false", m)
		}
	}
}

func TestResolveMIME(t *testing.T) {
	if got := resolveMIME("a.png", []byte("whatever")); !strings.HasPrefix(got, "image/png") {
		t.Errorf("resolveMIME by extension = %q, want image/png*", got)
	}
	// No extension → sniff the bytes (%PDF-… → application/pdf).
	if got := resolveMIME("blob", []byte("%PDF-1.7\n...")); !strings.HasPrefix(got, "application/pdf") {
		t.Errorf("resolveMIME by sniff = %q, want application/pdf*", got)
	}
}

// newMediaTestServer wires a Server with a run store holding one todo, plus the blob pool.
func newMediaTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_RUNS", filepath.Join(dir, "runs.json"))
	t.Setenv("DEVLAB_MERCURY_RUNS_HISTORY", filepath.Join(dir, "hist"))
	t.Setenv("DEVLAB_MERCURY_ATTACHMENTS", filepath.Join(dir, "att"))
	s := &Server{runs: runs.NewStore(nil), attachments: runs.NewAttachmentStore(nil)}
	id := runs.NewID()
	if err := s.runs.Put(runs.Run{ID: id, Kind: model.KindTodo, Title: "T", Task: "x",
		Targets: []runs.Target{{Repo: "svc"}}}); err != nil {
		t.Fatal(err)
	}
	return s, id
}

func uploadMedia(t *testing.T, s *Server, todoID, filename string, content []byte) (*int, []runs.AttachmentRef) {
	t.Helper()
	rec := doJSON(t, s.runAttachmentUpload, http.MethodPost, "/api/mercury/runs/"+todoID+"/attachments",
		"uploader", todoID, map[string]string{"filename": filename, "contentB64": base64.StdEncoding.EncodeToString(content)})
	var out struct {
		Attachments []runs.AttachmentRef `json:"attachments"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return &rec.Code, out.Attachments
}

// SHA dedup (REQ-007): the same bytes attached twice — double paste, or dialog then paste —
// coalesce to ONE attachment; the response is a success carrying the unchanged set.
func TestAttachmentUploadShaDedup(t *testing.T) {
	s, id := newMediaTestServer(t)
	content := []byte("the very same screenshot bytes")

	code, atts := uploadMedia(t, s, id, "shot.png", content)
	if *code != http.StatusOK || len(atts) != 1 {
		t.Fatalf("first upload: %d, %d attachments", *code, len(atts))
	}
	// Same content under ANOTHER name (a paste mints "pasted-…"): still one attachment.
	code, atts = uploadMedia(t, s, id, "pasted-xyz.png", content)
	if *code != http.StatusOK {
		t.Fatalf("dedup upload must succeed: %d", *code)
	}
	if len(atts) != 1 || atts[0].Filename != "shot.png" {
		t.Fatalf("same bytes must coalesce to the one existing attachment: %+v", atts)
	}
	// Different bytes attach normally.
	code, atts = uploadMedia(t, s, id, "other.png", []byte("different bytes"))
	if *code != http.StatusOK || len(atts) != 2 {
		t.Fatalf("distinct content must attach: %d, %+v", *code, atts)
	}
	// The recorded metadata carries the digest — the dedup key is visible, not hidden state.
	if atts[0].SHA256 == "" || atts[1].SHA256 == "" || atts[0].SHA256 == atts[1].SHA256 {
		t.Fatalf("attachments must carry distinct SHA256 digests: %+v", atts)
	}
}

// The caps hold at the upload access point: a per-file size ceiling, a per-todo count ceiling,
// and a duplicate NAME with different content is a named conflict (never silent replacement).
func TestAttachmentUploadCaps(t *testing.T) {
	s, id := newMediaTestServer(t)

	// Same name, different bytes → 409, the stored attachment stays.
	if code, _ := uploadMedia(t, s, id, "doc.txt", []byte("v1")); *code != http.StatusOK {
		t.Fatalf("seed upload: %d", *code)
	}
	if code, _ := uploadMedia(t, s, id, "doc.txt", []byte("v2")); *code != http.StatusConflict {
		t.Fatalf("duplicate name with new content must 409, got %d", *code)
	}

	// Count cap: fill to the ceiling, then one more is refused.
	for i := 1; i < maxAttachmentsPerTodo; i++ {
		if code, _ := uploadMedia(t, s, id, fmt.Sprintf("f%02d.txt", i), []byte(fmt.Sprintf("content %d", i))); *code != http.StatusOK {
			t.Fatalf("fill %d: %d", i, *code)
		}
	}
	if code, _ := uploadMedia(t, s, id, "straw.txt", []byte("one too many")); *code != http.StatusBadRequest {
		t.Fatalf("count cap must refuse the %dth attachment, got %d", maxAttachmentsPerTodo+1, *code)
	}
	// Re-pasting EXISTING bytes at the cap is not an error — it is the dedup no-op.
	if code, atts := uploadMedia(t, s, id, "again.txt", []byte("content 1")); *code != http.StatusOK || len(atts) != maxAttachmentsPerTodo {
		t.Fatalf("dedup at the cap must stay a success: %d (%d attachments)", *code, len(atts))
	}

	// Per-file size ceiling (25 MiB) → 413.
	big := make([]byte, maxAttachmentBytes+1)
	if code, _ := uploadMedia(t, s, id, "huge.bin", big); *code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized file must 413, got %d", *code)
	}
}

// Deleting an attachment removes exactly that metadata row and its blob; the todo's prompt
// snapshot recomposes in the same pass (REQ-003 — every write path recomposes).
func TestAttachmentDeleteRecomposes(t *testing.T) {
	s, id := newMediaTestServer(t)
	if code, _ := uploadMedia(t, s, id, "keep.txt", []byte("keep")); *code != http.StatusOK {
		t.Fatal("seed keep")
	}
	code, atts := uploadMedia(t, s, id, "drop.txt", []byte("drop"))
	if *code != http.StatusOK || len(atts) != 2 {
		t.Fatal("seed drop")
	}
	run, _, _ := s.runs.Get(id)
	if !strings.Contains(run.PromptSnapshot, "drop.txt") {
		t.Fatal("upload must fold the attachment into the prompt snapshot")
	}

	var dropID string
	for _, a := range atts {
		if a.Filename == "drop.txt" {
			dropID = a.ID
		}
	}
	req := doJSON(t, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("aid", dropID)
		s.runAttachmentDelete(w, r)
	}, http.MethodDelete, "/api/mercury/runs/"+id+"/attachments/"+dropID, "uploader", id, nil)
	if req.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", req.Code, req.Body)
	}
	run, _, _ = s.runs.Get(id)
	if len(run.Attachments) != 1 || run.Attachments[0].Filename != "keep.txt" {
		t.Fatalf("exactly the named attachment must go: %+v", run.Attachments)
	}
	if strings.Contains(run.PromptSnapshot, "drop.txt") || !strings.Contains(run.PromptSnapshot, "keep.txt") {
		t.Fatal("the prompt snapshot must recompose on delete")
	}
	if _, err := s.attachments.Get(id, dropID); err == nil {
		t.Fatal("the deleted attachment's blob must be gone")
	}
}
