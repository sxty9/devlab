package fsatomic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteJSONCreatesDirsRoundTripsIndented(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "deep", "data.json")
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
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file lingered after a successful write")
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

func TestAppendLineJournals(t *testing.T) {
	p := filepath.Join(t.TempDir(), "exec", "transcript.jsonl")
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
}
