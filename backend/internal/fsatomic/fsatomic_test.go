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
	if err := WriteFile(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Errorf("got %q want two", b)
	}
}
