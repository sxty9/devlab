package fsatomic

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// tmpResidue lists the staging files left in a directory. After any finished write — successful or
// failed — the answer must be none: a leftover is either litter or, worse, a staging area a second
// writer could join.
func tmpResidue(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestWriteJSONCreatesDirsRoundTripsIndented(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	p := filepath.Join(dir, "data.json")
	type doc struct {
		A int
		B string
	}
	in := doc{A: 7, B: "x"}
	if err := WriteJSON(p, in); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out doc
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip: got %+v want %+v", out, in)
	}
	if !strings.Contains(string(b), "\n  ") {
		t.Errorf("expected indented JSON, got %q", b)
	}
	if left := tmpResidue(t, dir); len(left) != 0 {
		t.Errorf("staging files lingered after a successful write: %v", left)
	}
}

func TestWriteFileOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := WriteFile(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Errorf("got %q want two", b)
	}
}

// The staging file must NOT be the predictable "<path>.tmp" neighbour. Proof without reaching into
// the implementation: occupy that exact name with a directory. A writer that insists on the fixed
// name cannot write there (it is a directory) and loses the write; a writer that stages under its
// own unique name is untroubled.
func TestWriteFileDoesNotUseTheFixedNeighbourName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pool.json")
	if err := os.Mkdir(p+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("payload"), 0o600); err != nil {
		t.Fatalf("the write must not depend on the fixed neighbour name: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "payload" {
		t.Errorf("content = %q, want payload", b)
	}
}

// Two writers of ONE path must never publish a mixed — or empty — file. With a shared fixed staging
// name the second writer truncates the first one's staging area under it, so a reader can observe a
// zero-length file at the final path (and the loser's rename can even fail outright). Run with
// -race; the assertion, however, holds without it.
func TestWriteFileConcurrentWritersNeverPublishAMixedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pool.json")
	const size, rounds = 64 * 1024, 60
	fills := []byte{'a', 'b', 'c'}

	// A first complete version, so a reader never legitimately finds the file missing.
	if err := WriteFile(p, bytes.Repeat([]byte{fills[0]}, size), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	errs := make(chan error, len(fills)*rounds)
	var writers sync.WaitGroup
	for _, fill := range fills {
		writers.Add(1)
		go func(fill byte) {
			defer writers.Done()
			payload := bytes.Repeat([]byte{fill}, size)
			for i := 0; i < rounds; i++ {
				if err := WriteFile(p, payload, 0o600); err != nil {
					errs <- err
					return
				}
			}
		}(fill)
	}

	var reader sync.WaitGroup
	reader.Add(1)
	var mixed []byte
	go func() {
		defer reader.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			b, err := os.ReadFile(p)
			if err != nil {
				errs <- err
				return
			}
			if len(b) != size || bytes.Count(b, b[:1]) != len(b) {
				if mixed == nil {
					mixed = append([]byte(nil), b...)
				}
				return
			}
		}
	}()

	writers.Wait()
	close(done)
	reader.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write/read failed: %v", err)
	}
	if mixed != nil {
		t.Errorf("a reader observed a file that is neither version whole: %d bytes, prefix %q",
			len(mixed), mixed[:min(16, len(mixed))])
	}
	if left := tmpResidue(t, dir); len(left) != 0 {
		t.Errorf("staging files lingered after the concurrent run: %v", left)
	}
	// Whatever finished last, it is one writer's payload in full.
	final, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != size || bytes.Count(final, final[:1]) != len(final) {
		t.Errorf("final file is not one whole version: %d bytes", len(final))
	}
}

// The durability steps must actually run, and in the order that makes them worth anything: the data
// is flushed WHILE the final path still holds the old version, the directory entry afterwards. Read
// off the two flush seams — with no flush at all (plain write + rename) both counters stay zero.
func TestWriteFileFlushesBeforeAndAfterTheRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pool.json")
	if err := WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	origFile, origDir := syncFile, syncDir
	defer func() { syncFile, syncDir = origFile, origDir }()
	var atFileFlush, atDirFlush string
	var fileFlushes, dirFlushes int
	syncFile = func(f *os.File) error {
		fileFlushes++
		b, _ := os.ReadFile(p)
		atFileFlush = string(b)
		return origFile(f)
	}
	syncDir = func(d *os.File) error {
		dirFlushes++
		b, _ := os.ReadFile(p)
		atDirFlush = string(b)
		return origDir(d)
	}

	if err := WriteFile(p, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fileFlushes != 1 || dirFlushes != 1 {
		t.Fatalf("want one data flush and one directory flush, got %d/%d", fileFlushes, dirFlushes)
	}
	if atFileFlush != "old" {
		t.Errorf("the data flush must happen BEFORE the rename (path held %q, want old)", atFileFlush)
	}
	if atDirFlush != "new" {
		t.Errorf("the directory flush must happen AFTER the rename (path held %q, want new)", atDirFlush)
	}
}

// A write that cannot be completed must leave the previous version byte-for-byte and drop its
// staging file — never a truncated or empty file under the real name.
func TestWriteFileAbortedWriteKeepsThePreviousVersion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pool.json")
	if err := WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := syncFile
	defer func() { syncFile = orig }()
	syncFile = func(*os.File) error { return os.ErrInvalid } // the disk gives up mid-write

	if err := WriteFile(p, bytes.Repeat([]byte("new"), 4096), 0o600); err == nil {
		t.Fatal("a failed flush must be reported, not swallowed")
	}
	if b, _ := os.ReadFile(p); string(b) != "old" {
		t.Errorf("the previous version was damaged: %q", b)
	}
	if left := tmpResidue(t, dir); len(left) != 0 {
		t.Errorf("the aborted write left staging files behind: %v", left)
	}
}

// Same promise when the publish itself fails: an occupied, non-empty directory at the target path
// makes the rename impossible. The error surfaces and nothing is left lying around.
func TestWriteFileFailedRenameCleansUp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "occupied")
	if err := os.MkdirAll(filepath.Join(p, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("payload"), 0o600); err == nil {
		t.Fatal("a rename onto a non-empty directory must fail loudly")
	}
	if left := tmpResidue(t, dir); len(left) != 0 {
		t.Errorf("the failed publish left staging files behind: %v", left)
	}
}

// The file carries exactly the requested permissions — the staging file is created 0600, so a wider
// mode must be applied explicitly, and it must not be filtered by the process umask.
func TestWriteFileHonoursPermissions(t *testing.T) {
	dir := t.TempDir()
	for _, perm := range []os.FileMode{0o600, 0o644, 0o640} {
		p := filepath.Join(dir, "f")
		if err := WriteFile(p, []byte("x"), perm); err != nil {
			t.Fatalf("WriteFile(%v): %v", perm, err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != perm {
			t.Errorf("mode = %v, want %v", fi.Mode().Perm(), perm)
		}
	}
}

func TestAppendLineJournals(t *testing.T) {
	p := filepath.Join(t.TempDir(), "exec", "transcript.jsonl")

	orig := syncFile
	defer func() { syncFile = orig }()
	flushes := 0
	syncFile = func(f *os.File) error { flushes++; return orig(f) }

	if err := AppendLine(p, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("AppendLine (creates dirs + file): %v", err)
	}
	if err := AppendLine(p, []byte(`{"b":2}`)); err != nil {
		t.Fatalf("AppendLine (appends): %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{\"a\":1}\n{\"b\":2}\n" {
		t.Errorf("journal = %q, want two newline-terminated lines in order", b)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("journal mode = %v, want 0600", fi.Mode().Perm())
	}
	// Every appended line is flushed before the call returns: a line reported as written must
	// survive a crash.
	if flushes != 2 {
		t.Errorf("want one flush per appended line, got %d", flushes)
	}
}
