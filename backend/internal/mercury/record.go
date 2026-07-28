// Record is one stored axiom or rule: its path and parsed content — the shared read shape of
// the constitution store (catalog scans, reconcile, chat context).
package mercury

import "devlab/backend/internal/slug"

// Record is one stored record: its path and parsed content.
type Record struct {
	Path  string
	Axiom Axiom
}

// Slug reduces a title to a lowercase kebab path segment (ascii-only, matching the
// classifier's path rule): umlauts transliterated, everything else collapsed to single dashes.
func Slug(s string) string {
	return slug.Make(s, "-")
}
