package runs

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"devlab/backend/internal/fsatomic"
)

// AttachmentStore is the passive media pool for ToDo attachments: a plain on-disk blob store holding
// one file per attachment at <base>/<runID>/<attID>. It is a pure PASSIVE Data Pool — it keeps bytes
// and never interprets or evaluates them; which attachments exist and what they mean is the Run's
// concern (its Attachment metadata), not the pool's. Writes are atomic (tmp+rename, 0600) and the base
// is created lazily, mirroring the runs Store's storage discipline.
type AttachmentStore struct {
	base string
}

// runIDRe / attIDRe bound the two ids to the [a-z0-9] shape NewID / NewAttachmentID mint. They are the
// only guard between a caller-supplied id and a filesystem path segment, so they must reject anything
// that could traverse ("/", ".", "..").
var (
	runIDRe = regexp.MustCompile(`^run_[a-z0-9]+$`)
	attIDRe = regexp.MustCompile(`^att_[a-z0-9]+$`)
)

// ErrBadID is returned when a run/attachment id does not match its minted shape (a crafted id that
// could otherwise escape the pool base).
var ErrBadID = errors.New("invalid id")

// NewAttachmentStore builds the pool from the environment. Like NewStore it never errors — a missing
// directory is an empty pool. The pool lives next to runs.json so all Mercury run state is co-located.
func NewAttachmentStore() *AttachmentStore { return &AttachmentStore{base: attachmentsDir()} }

func attachmentsDir() string {
	if p := os.Getenv("DEVLAB_MERCURY_ATTACHMENTS"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(runsPath()), "attachments")
}

// path resolves the on-disk file for one attachment, refusing any id that is not of the minted shape.
func (a *AttachmentStore) path(runID, attID string) (string, error) {
	if !runIDRe.MatchString(runID) || !attIDRe.MatchString(attID) {
		return "", ErrBadID
	}
	return filepath.Join(a.base, runID, attID), nil
}

// Put stores an attachment's bytes atomically (tmp+rename), creating the per-run directory as needed.
func (a *AttachmentStore) Put(runID, attID string, data []byte) error {
	p, err := a.path(runID, attID)
	if err != nil {
		return err
	}
	return fsatomic.WriteFile(p, data)
}

// Get returns an attachment's bytes (os.ErrNotExist when absent).
func (a *AttachmentStore) Get(runID, attID string) ([]byte, error) {
	p, err := a.path(runID, attID)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Delete removes a single attachment's bytes (idempotent — a missing blob is not an error).
func (a *AttachmentStore) Delete(runID, attID string) error {
	p, err := a.path(runID, attID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteAll drops a run's whole attachment directory — called when the ToDo itself is deleted so no
// orphaned blobs linger after their owning record is gone.
func (a *AttachmentStore) DeleteAll(runID string) error {
	if !runIDRe.MatchString(runID) {
		return ErrBadID
	}
	return os.RemoveAll(filepath.Join(a.base, runID))
}
