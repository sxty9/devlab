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
// bytes to a sibling ".tmp" file (0600), then renames it into place. The rename is atomic on a
// single filesystem, so a concurrent reader never observes a half-written file and a crash
// mid-write leaves the previous contents intact; a failed rename cleans up its tmp.
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// WriteJSON marshals v as indented JSON and writes it to path via WriteFile.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, b)
}
