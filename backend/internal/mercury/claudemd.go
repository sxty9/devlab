// CLAUDE.md constitution block (S5). The constitution reaches every implementation THROUGH THE
// PROMPT (the run's snapshot carries the full wording); the repo's CLAUDE.md carries only the
// short reference text below between two markers. Replacement touches ONLY the span between
// the markers — everything outside stays untouched. There is NO rollout: the replacement rides
// along as part of implement/publish of the one chain (B3 calls ReplaceConstitutionBlock; the
// rendering lives here).
package mercury

// Markers delimiting the managed constitution block inside a repo's CLAUDE.md (distilled from
// the retired rollout renderer).
const (
	ClaudeMDMarkerBegin = "<!-- holistic:constitution:begin -->"
	ClaudeMDMarkerEnd   = "<!-- holistic:constitution:end -->"
)

// ReferenceText is the released CLAUDE.md reference text (D §3) — B6 fills in the VERBATIM
// wording from the constitution source; this placeholder only pins the constant's existence
// and its role. It deliberately names no address (an address would be instance-specific).
const ReferenceText = "" +
	"# Holistic — Verfassung\n" +
	"\n" +
	"TODO(B6): verbatim wording from D-verfassung.md §3 (German reference text; replaced as a\n" +
	"whole, never edited piecemeal).\n"

// ReplaceConstitutionBlock replaces the span between the markers in doc with the reference
// text, leaving everything outside untouched. A document without markers gets the block
// appended (markers included). Returns the new document and whether it changed.
func ReplaceConstitutionBlock(doc string) (string, bool) {
	panic("TODO(B6)")
}
