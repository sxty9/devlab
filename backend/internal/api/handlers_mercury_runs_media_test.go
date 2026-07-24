package api

import (
	"strings"
	"testing"
)

func TestSanitizeAttachmentName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"photo.png", "photo.png"},
		{"  spaced.pdf  ", "spaced.pdf"},
		{"../../etc/passwd", "passwd"},         // traversal collapses to the base name
		{"a/b/c.txt", "c.txt"},                 // nested path → last segment only
		{"weird\x00name.bin", "weirdname.bin"}, // control chars stripped
		{"back\\slash.doc", "back_slash.doc"},  // backslash is not a separator here → neutralized
		{"", ""},
		{".", ""},
		{"..", ""},
	}
	for _, c := range cases {
		if got := sanitizeAttachmentName(c.in); got != c.want {
			t.Errorf("sanitizeAttachmentName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := sanitizeAttachmentName(strings.Repeat("a", maxAttachmentNameLen+50)); len(got) != maxAttachmentNameLen {
		t.Errorf("over-long name not bounded: got len %d, want %d", len(got), maxAttachmentNameLen)
	}
}

func TestInlineSafeMIME(t *testing.T) {
	for _, m := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf", "image/png; charset=binary"} {
		if !inlineSafeMIME(m) {
			t.Errorf("inlineSafeMIME(%q) = false, want true", m)
		}
	}
	// Active-content types (SVG can carry script, HTML executes) must never render inline.
	for _, m := range []string{"image/svg+xml", "text/html", "application/octet-stream", "text/plain", ""} {
		if inlineSafeMIME(m) {
			t.Errorf("inlineSafeMIME(%q) = true, want false", m)
		}
	}
}

func TestResolveMIME(t *testing.T) {
	if got := resolveMIME("a.png", []byte("whatever")); !strings.HasPrefix(got, "image/png") {
		t.Errorf("resolveMIME by extension = %q, want image/png*", got)
	}
	// No extension → sniff the bytes (%PDF-… → application/pdf).
	if got := resolveMIME("blob", []byte("%PDF-1.7\n...")); !strings.HasPrefix(got, "application/pdf") {
		t.Errorf("resolveMIME by sniff = %q, want application/pdf*", got)
	}
}
