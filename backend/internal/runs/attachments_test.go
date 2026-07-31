package runs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAttachmentStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_ATTACHMENTS", dir)
	a := NewAttachmentStore(nil)

	runID, attID := NewID(), NewAttachmentID()
	data := []byte("\x89PNG\r\n\x1a\n not really a png")
	if err := a.Put(runID, attID, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// The blob lives at <base>/<runID>/<attID>, keyed by both ids.
	if _, err := os.Stat(filepath.Join(dir, runID, attID)); err != nil {
		t.Fatalf("blob not at expected path: %v", err)
	}
	got, err := a.Get(runID, attID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, data)
	}

	if err := a.Delete(runID, attID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := a.Get(runID, attID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Get after Delete: want ErrNotExist, got %v", err)
	}
	if err := a.Delete(runID, attID); err != nil { // idempotent
		t.Fatalf("Delete idempotent: %v", err)
	}
}

func TestAttachmentStoreDeleteAll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_ATTACHMENTS", dir)
	a := NewAttachmentStore(nil)

	runID := NewID()
	for i := 0; i < 3; i++ {
		if err := a.Put(runID, NewAttachmentID(), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.DeleteAll(runID); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, runID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run dir still present after DeleteAll: %v", err)
	}
	if err := a.DeleteAll(NewID()); err != nil { // absent run is not an error
		t.Fatalf("DeleteAll absent: %v", err)
	}
}

// A crafted id must never let a caller read or write outside the pool base.
func TestAttachmentStoreRejectsCraftedIDs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_ATTACHMENTS", dir)
	a := NewAttachmentStore(nil)

	bad := []struct{ run, att string }{
		{"../escape", "att_ok"},
		{"run_ok", "../escape"},
		{"run_ok", "att_../../etc"},
		{"run/slash", "att_ok"},
		{"", ""},
		{"run_ok", "notanid"},
	}
	for _, b := range bad {
		if err := a.Put(b.run, b.att, []byte("x")); !errors.Is(err, ErrBadID) {
			t.Errorf("Put(%q,%q): want ErrBadID, got %v", b.run, b.att, err)
		}
		if _, err := a.Get(b.run, b.att); !errors.Is(err, ErrBadID) {
			t.Errorf("Get(%q,%q): want ErrBadID, got %v", b.run, b.att, err)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("crafted ids wrote into the base dir: %v", entries)
	}
}
