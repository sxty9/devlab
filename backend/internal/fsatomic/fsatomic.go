// Package fsatomic is the single durable-write access point for the backend's file-backed data
// pools. Every store (runs, results, PRs, history, attachments, comments, chats, links, the report
// ledger, the Mercury order map) persists through the same tmp-then-rename dance here, so the
// crash-safety guarantee lives in one place instead of being re-derived per store.
package fsatomic

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteFile writes data to path atomically. It creates the parent directory (0700), writes the
// bytes to a sibling ".tmp" file with the given permissions, then renames it into place. The
// rename is atomic on a single filesystem, so a concurrent reader never observes a half-written
// file and a crash mid-write leaves the previous contents intact; a failed rename cleans up its
// tmp.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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
// concurrent appenders never interleave mid-line; readers tolerate a torn LAST line (a crash
// mid-append loses at most that line). This is the ONLY second write primitive — it lives in
// this package so there is still exactly one write path (W-G).
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
	return f.Close()
}
