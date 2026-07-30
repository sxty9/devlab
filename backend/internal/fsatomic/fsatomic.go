// Package fsatomic is the single durable-write access point for the backend's file-backed data
// pools. Every store (runs, results, PRs, history, attachments, comments, chats, links, the report
// ledger, the Mercury order map) persists through the same tmp-then-rename dance here, so the
// crash-safety guarantee lives in one place instead of being re-derived per store.
package fsatomic

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// syncFile and syncDir are the two durability steps, indirected so a test can prove they actually
// run and can stage a failing flush. A power cut cannot be staged in a unit test; the seam is how
// "the bytes are on the disk before the name moves" stays a tested promise instead of a comment.
var (
	syncFile = func(f *os.File) error { return f.Sync() }
	syncDir  = func(d *os.File) error { return d.Sync() }
)

// WriteFile writes data to path atomically and durably. It creates the parent directory (0700),
// stages the bytes in a UNIQUE intermediate file inside the target's own directory, flushes them to
// the disk, and only then renames the intermediate onto path.
//
// Three properties follow, and each one needs its step:
//
//   - No reader ever sees a half file, because the rename is atomic on a single filesystem and the
//     intermediate lives in the same directory (never across a mount).
//   - Two writers of the same path never mix: each stages in its own intermediate, so the loser of
//     the race is simply overwritten as a whole. A fixed "<path>.tmp" neighbour would let the second
//     writer truncate the first one's staging area under it — publishing an empty or mixed file.
//   - A crash leaves either the previous contents or the complete new ones, because the data is
//     fsynced before the rename and the directory entry after it.
//
// A failure at any step removes the intermediate and leaves the previous contents untouched. The
// file at path carries exactly perm (set explicitly, so the umask cannot narrow it) and this
// process's ownership — the intermediate is created fresh, never inherited from what was there.
// The intermediate keeps the ".tmp" extension, so the state-root hygiene sweep recognises the
// leftover of a killed process and directory listings that filter by extension skip it.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("fsatomic: stage %s: %w", path, err)
	}
	tmp := f.Name()
	// abort is the single exit for every failure before the rename: close the handle, drop the
	// intermediate, name the step. Nothing half-written is left behind, under either name.
	abort := func(step string, cause error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsatomic: %s %s: %w", step, path, cause)
	}
	// CreateTemp opens with 0600; Chmod (unlike the create mode) is not filtered by the umask.
	if err := f.Chmod(perm); err != nil {
		return abort("chmod", err)
	}
	if _, err := f.Write(data); err != nil {
		return abort("write", err)
	}
	if err := syncFile(f); err != nil {
		return abort("sync", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsatomic: close %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsatomic: rename %s: %w", path, err)
	}
	// The new contents are visible to readers from the rename on; flushing the directory is what
	// makes the name itself outlive a power cut.
	return flushDir(dir)
}

// flushDir fsyncs a directory so a rename inside it becomes durable.
func flushDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("fsatomic: open dir %s: %w", dir, err)
	}
	if err := syncDir(d); err != nil {
		_ = d.Close()
		return fmt.Errorf("fsatomic: sync dir %s: %w", dir, err)
	}
	return d.Close()
}

// WriteJSON marshals v as indented JSON and writes it to path via WriteFile (0600 — the pool
// default from the persistence inventory).
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, b, 0o600)
}

// AppendLine appends one line to an O_APPEND journal (the execution transcript,
// transcript.jsonl). The line is written with a trailing newline in a single write call, so
// concurrent appenders never interleave mid-line, and it is flushed to the disk before the call
// returns, so a crash cannot swallow lines that were already reported as written; readers still
// tolerate a torn LAST line, because no fsync makes a single write indivisible against a power cut.
// This is the ONLY second write primitive — it lives in this package so there is still exactly one
// write path (W-G).
func AppendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return err
	}
	if err := syncFile(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsatomic: sync %s: %w", path, err)
	}
	return f.Close()
}
