// Package slug turns human text into a path/ref-safe segment. It is the single slugifier shared by
// the run-branch namer (runs.BranchSlug) and the Mercury scheme path rule (mercury.Slug): the same
// lowercasing, German transliteration and folding, differing only in the separator — so the two
// callers can never drift apart.
package slug

import (
	"regexp"
	"strings"
)

var (
	// umlauts transliterates the German letters a name is most likely to carry, so "Account-Löschung"
	// becomes "account-loeschung" rather than losing the vowel to a bare strip. Keys are lowercase:
	// Make lowercases first, so an uppercase "Ö" is already "ö" by the time this runs.
	umlauts = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")
	// nonAlnum matches every run of characters a slug segment may not keep.
	nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
)

// Make lowercases s, transliterates the German umlauts, collapses every run of remaining
// non-alphanumeric characters to a single sep, and trims sep from both ends. The result is empty (when
// s carries nothing slugifiable) or a sep-joined sequence of [a-z0-9] groups.
func Make(s, sep string) string {
	s = strings.ToLower(s)
	s = umlauts.Replace(s)
	s = nonAlnum.ReplaceAllString(s, sep)
	return strings.Trim(s, sep)
}
