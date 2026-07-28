package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/mercury"
)

// The tests run the Executor in bypass mode (PerUser=false) against a temp worktree — the
// wrapper boundary is identical code either way (writeBytes / DeleteFile), minus sudo.
func benchExecutor(t *testing.T) (Executor, string) {
	t.Helper()
	wt := t.TempDir()
	return Executor{User: "tester", PerUser: false}, wt
}

func TestStageAttachmentsWritesManifestAndFiles(t *testing.T) {
	ex, wt := benchExecutor(t)
	files := []AttachmentFile{
		{Filename: "mock.png", Data: []byte("PNGDATA"), Note: "UI mock"},
		{Filename: "spec.pdf", Data: []byte("PDFDATA")},
	}
	manifest, staged, cleanup, err := ex.StageAttachments(context.Background(), wt, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 2 {
		t.Fatalf("staged %d, want 2", len(staged))
	}
	for _, s := range staged {
		b, rerr := os.ReadFile(filepath.Join(wt, s.Path))
		if rerr != nil {
			t.Fatalf("staged file missing: %v", rerr)
		}
		if len(b) == 0 {
			t.Fatalf("staged file empty: %s", s.Path)
		}
		if !strings.HasPrefix(s.Path, mercury.TodoAttachmentDir+"/") {
			t.Fatalf("staged outside the ONE attachment dir: %s", s.Path)
		}
		if !strings.Contains(manifest, s.Path) {
			t.Fatalf("manifest misses %s:\n%s", s.Path, manifest)
		}
	}
	if !strings.Contains(manifest, "UI mock") {
		t.Fatalf("manifest misses the note:\n%s", manifest)
	}
	if !strings.Contains(manifest, "never commit") {
		t.Fatalf("manifest misses the context-only rule:\n%s", manifest)
	}

	// Cleanup removes everything and is idempotent (deferred AND explicit call).
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	for _, s := range staged {
		if _, err := os.Stat(filepath.Join(wt, s.Path)); !os.IsNotExist(err) {
			t.Fatalf("cleanup left %s", s.Path)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup not idempotent: %v", err)
	}
}

func TestStageAttachmentsEmptyIsNoManifest(t *testing.T) {
	ex, wt := benchExecutor(t)
	manifest, staged, cleanup, err := ex.StageAttachments(context.Background(), wt, nil)
	if err != nil || manifest != "" || len(staged) != 0 {
		t.Fatalf("empty staging not empty: %q %d %v", manifest, len(staged), err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
}

// A hostile filename must never escape the workspace (B-6 + safePath defense-in-depth).
func TestStageAttachmentsRefusesTraversal(t *testing.T) {
	ex, wt := benchExecutor(t)
	for _, name := range []string{"../evil.sh", "a/b.png", "..", "", "a\\b"} {
		_, _, cleanup, err := ex.StageAttachments(context.Background(), wt, []AttachmentFile{{Filename: name, Data: []byte("x")}})
		if err == nil {
			t.Fatalf("filename %q accepted", name)
		}
		_ = cleanup()
	}
	// Nothing dangles after the refusals.
	if _, err := os.Stat(filepath.Join(wt, mercury.TodoAttachmentDir)); err == nil {
		entries, _ := os.ReadDir(filepath.Join(wt, mercury.TodoAttachmentDir))
		if len(entries) != 0 {
			t.Fatalf("refused staging left files behind")
		}
	}
}

// A partial failure rolls back everything already written.
func TestStageAttachmentsPartialRollback(t *testing.T) {
	ex, wt := benchExecutor(t)
	files := []AttachmentFile{
		{Filename: "ok.png", Data: []byte("fine")},
		{Filename: "../escape", Data: []byte("x")}, // fails after the first write
	}
	_, _, cleanup, err := ex.StageAttachments(context.Background(), wt, files)
	if err == nil {
		t.Fatalf("want error")
	}
	_ = cleanup()
	if _, serr := os.Stat(filepath.Join(wt, mercury.TodoAttachmentRel("ok.png"))); !os.IsNotExist(serr) {
		t.Fatalf("partial write not rolled back")
	}
}
