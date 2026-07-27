package runs

import (
	"crypto/rand"
	"encoding/base32"
	"strings"

	"devlab/backend/internal/slug"
)

// Mercury names every run branch it pushes by a single, uniform convention:
//
//	<kind>/<description>
//
// kind is the nature of the change (fix | feature); description is the run's own name, slugified so it
// reads at a glance (spaces → "_", lowercased, punctuation folded away). A short per-firing token is
// appended to the description so the name stays UNIQUE — a run may fire repeatedly and its workspace is
// reused, so a stable name would collide on the next `git switch -c` and on a non-fast-forward push.
// The result replaces the opaque legacy shape (mercury-run/<id>/<timestamp>) with a human-readable one
// like `feature/dark_mode-k3f9a2` or `fix/passive_comment_pool-7bq1zx`.

// Branch kinds. "e.g. fix, feature" — a deliberately tiny, closed vocabulary (Minimalism): a change to
// an existing service is a fix, a newly planned service is a feature. Both are plain lowercase words, so
// they satisfy every git ref rule without escaping.
const (
	BranchKindFix     = "fix"
	BranchKindFeature = "feature"
)

// branchDescMax caps the description slug so a rambling run name does not produce an unwieldy ref. The
// cut lands on a word boundary (an underscore) when one is in range, never mid-word.
const branchDescMax = 40

// BranchKindFor picks the kind for a run branch: a newly planned service is a feature, everything else
// (a change to an existing repo, an axiom sweep) is a fix.
func BranchKindFor(newService bool) string {
	if newService {
		return BranchKindFeature
	}
	return BranchKindFix
}

// BranchSlug turns a display name into the description segment of a run branch: transliterated,
// lowercased, every run of non-alphanumerics collapsed to a single "_", trimmed of leading/trailing
// separators, and length-capped on a word boundary. It returns "" when the name carries nothing
// slugifiable (e.g. only punctuation or non-Latin script) — the caller supplies the fallback.
func BranchSlug(name string) string {
	s := slug.Make(name, "_")
	if len(s) <= branchDescMax {
		return s
	}
	s = s[:branchDescMax]
	if i := strings.LastIndexByte(s, '_'); i > 0 {
		s = s[:i] // prefer a whole-word cut over a mid-word one
	}
	return strings.Trim(s, "_")
}

// BranchDescGoodEnough reports whether name already yields a usable description on its own — the "use
// the run name if good enough" path. When it does not (empty or a one/two-character slug), the caller
// falls back to an AI-optimized description. The threshold is deliberately low: a real run name almost
// always passes, and the AI round-trip is reserved for the genuinely unusable ones.
func BranchDescGoodEnough(name string) bool {
	return len(BranchSlug(name)) >= 3
}

// BranchName assembles the full run branch: <kind>/<description>-<uniq>. desc is slugified (falling back
// to "change" when it carries nothing usable); uniq is sanitized to the same alphabet and appended so the
// ref stays unique per firing. The result is guaranteed to satisfy git's ref rules — only [a-z0-9._/-],
// no "..", and no segment that starts with "-".
func BranchName(kind, desc, uniq string) string {
	seg := BranchSlug(desc)
	if seg == "" {
		seg = "change"
	}
	if u := slug.Make(uniq, "_"); u != "" {
		seg += "-" + u
	}
	return kind + "/" + seg
}

// NewBranchToken mints the short, unguessable per-firing token appended to a run branch's description.
// Six lowercase base32 characters — enough that the handful of branches open at once never collide,
// short enough to keep the ref readable.
func NewBranchToken() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))[:6]
}
