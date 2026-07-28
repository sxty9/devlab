// Attachment staging (B-6): a todo's media must actually REACH the agent. StageAttachments
// materializes each attachment below the workspace's attachment directory (the ONE literal,
// mercury.TodoAttachmentDir) — in production via `devlab-exec write` as the runner user —
// returns a prompt manifest naming every staged file, and a cleanup that MUST run before
// stage/commit (and is additionally deferred) so attachment bytes never enter a commit.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"devlab/backend/internal/mercury"
)

// AttachmentFile is one attachment to stage: its filename and bytes.
type AttachmentFile struct {
	Filename string
	Data     []byte
	Note     string
}

// StagedAttachment names one materialized attachment inside the workspace.
type StagedAttachment struct {
	Filename string
	Path     string // workspace-relative
	Note     string
}

var errBadAttachmentName = errors.New("invalid attachment filename")

// attachmentRel maps a stored filename to its workspace-relative staging path, refusing
// anything that is not a plain basename (defense-in-depth beside safePath).
func attachmentRel(filename string) (string, error) {
	name := strings.TrimSpace(filename)
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, "/\\") || strings.Contains(name, "\x00") {
		return "", fmt.Errorf("%w: %q", errBadAttachmentName, filename)
	}
	return mercury.TodoAttachmentRel(name), nil
}

// StageAttachments writes the files below <wt>/<attachment dir>/ (per-user via the pinned
// devlab-exec write verb; the wrapper re-validates the path), returning the prompt manifest,
// the staged list and the mandatory cleanup.
//
// The manifest names each file's workspace path and declared note so the prompt can reference
// the media verifiably (REQ-007.3). The cleanup removes every staged file again — it is
// idempotent and safe to call both deferred AND explicitly before any stage/commit, so the
// media stays CONTEXT and never leaks into a commit. A partial write is rolled back before
// the error returns, so nothing dangles into the workspace.
func (e Executor) StageAttachments(ctx context.Context, wt string, files []AttachmentFile) (manifest string, staged []StagedAttachment, cleanup func() error, err error) {
	written := make([]string, 0, len(files))
	done := false
	cleanup = func() error {
		if done {
			return nil
		}
		done = true
		var firstErr error
		for _, rel := range written {
			if rerr := e.DeleteFile(wt, rel); rerr != nil && firstErr == nil {
				firstErr = rerr
			}
		}
		return firstErr
	}

	for _, f := range files {
		if ctx.Err() != nil {
			_ = cleanup()
			return "", nil, noopCleanup, ctx.Err()
		}
		rel, rerr := attachmentRel(f.Filename)
		if rerr != nil {
			_ = cleanup()
			return "", nil, noopCleanup, rerr
		}
		if werr := e.WriteFileBytes(wt, rel, f.Data); werr != nil {
			_ = cleanup()
			return "", nil, noopCleanup, fmt.Errorf("stage attachment %s: %w", f.Filename, werr)
		}
		written = append(written, rel)
		staged = append(staged, StagedAttachment{Filename: f.Filename, Path: rel, Note: f.Note})
	}

	if len(staged) > 0 {
		var b strings.Builder
		b.WriteString("The following attached media files are staged in this workspace for context only.\n")
		b.WriteString("They are removed again before anything is committed — never commit them:\n")
		for _, s := range staged {
			b.WriteString("- " + s.Path)
			if s.Note != "" {
				b.WriteString(" — " + s.Note)
			}
			b.WriteString("\n")
		}
		manifest = b.String()
	}
	return manifest, staged, cleanup, nil
}

func noopCleanup() error { return nil }
