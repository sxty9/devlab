package mercury

import (
	_ "embed"
	"regexp"
	"strings"

	"devlab/backend/internal/slug"
)

// The one-time migration decomposes the original constitution (the 5+2 maxims and the prose
// sections of axioms/CLAUDE.MD.md) into fine-grained, atomic axioms — the input the classifier then
// files into the tree. Decomposition is deterministic and byte-faithful to the source; only the
// filing is AI-assisted. The source is embedded so the migration is reproducible regardless of
// where devlabd runs.

//go:embed source/constitution.md
var constitution string

// Atom is one decomposed unit: a single top-level bullet (with its sub-bullets), a numbered
// procedure taken whole, or a prose paragraph. Quelle points back at the source block, and
// SuggestedCategory seeds the classifier so the migration produces a coherent top-level structure
// instead of inventing one category per atom.
type Atom struct {
	SeedTitle         string `json:"seedTitle"`
	Body              string `json:"body"`
	Quelle            string `json:"quelle"`
	SuggestedCategory string `json:"suggestedCategory"` // top-level category (from the source section)
	MaximSlug         string `json:"maximSlug"`         // the maxim this atom belongs to, "" for section prose
}

// section maps a top-level source tag to a suggested axiom category. Prose sections that are really
// implementation guidance still become axioms here (the axiom/rule split is the owner's to refine
// afterwards by re-filing); the category keeps them grouped.
var sectionCategory = map[string]string{
	"holistic_architecture_maxims": "architektur",
	"decision_rule":                "architektur",
	"service_handlungsregeln":      "prozess",
	"axiomatische_systeme_maximes": "gesetzbuecher",
	"holistic_umgebung_guidelines": "umgebung",
	"holistic_mobile_maxims":       "mobile",
}

var (
	openNamed = regexp.MustCompile(`^<([a-z_][a-z0-9_]*)\s+name="([^"]*)"\s*>\s*$`)
	openBare  = regexp.MustCompile(`^<([a-z_][a-z0-9_]*)\s*>\s*$`)
	closeTag  = regexp.MustCompile(`^</([a-z_][a-z0-9_]*)\s*>\s*$`)
	numbered  = regexp.MustCompile(`^\d+\.\s`)
)

// Decompose parses the embedded constitution into atoms, in source order (so the classifier sees a
// section's earlier atoms as existing categories when filing its later ones). A section's own prose
// that merely introduces its maxims is framing, not an axiom, and is dropped; a section that holds
// only prose (no maxims) contributes that prose as an atom.
func Decompose() []Atom {
	var atoms []Atom
	lines := strings.Split(constitution, "\n")

	section := ""   // current top-level section tag
	blockTag := ""  // the tag of the open block (e.g. "maxim"), matched against its close tag
	blockName := "" // the block's display name (a maxim's name attr, or the nested tag)
	maximSeen := false
	var buf []string // accumulated body lines of the current block

	for _, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case section == "":
			if m := openBare.FindStringSubmatch(t); m != nil {
				section, blockTag, blockName, maximSeen, buf = m[1], "", "", false, nil
			}
		case blockTag != "" && closeTagName(t) == blockTag:
			// a maxim (or nested block) closes → its body is atoms
			atoms = append(atoms, atomsFromBlock(section, blockName, buf)...)
			blockTag, blockName, buf = "", "", nil
		case closeTagName(t) == section:
			// section closes → its own prose is an atom ONLY for a prose-only section
			if blockTag == "" && !maximSeen {
				atoms = append(atoms, atomsFromBlock(section, "", buf)...)
			}
			section, blockTag, blockName, buf = "", "", "", nil
		case openNamed.MatchString(t):
			buf = nil // drop the section's framing intro before its first maxim
			m := openNamed.FindStringSubmatch(t)
			blockTag, blockName, maximSeen = m[1], m[2], true
		case openBare.MatchString(t) && section != "":
			buf = nil // a bare nested tag (e.g. klaerung_im_nachtlauf) — drop preceding intro
			m := openBare.FindStringSubmatch(t)
			blockTag, blockName, maximSeen = m[1], m[1], true
		default:
			buf = append(buf, line)
		}
	}
	return atoms
}

// atomsFromBlock splits one block's body into atoms: each top-level "- " bullet (with its indented
// sub-bullets) is an atom; a numbered list is one atom (an ordered procedure); a prose paragraph is
// one atom. A leading intro sentence before the first bullet is dropped (it only frames the list).
func atomsFromBlock(section, blockName string, body []string) []Atom {
	cat := sectionCategory[section]
	maximSlug := ""
	if blockName != "" {
		maximSlug = Slug(blockName)
	}
	quelleBase := "axioms/CLAUDE.MD.md#" + section
	if blockName != "" {
		quelleBase += "/" + blockName
	}

	text := strings.TrimRight(strings.Join(body, "\n"), "\n")
	text = strings.TrimLeft(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// Numbered procedure → one atom.
	if numbered.MatchString(strings.TrimSpace(firstNonBlank(body))) {
		return []Atom{{
			SeedTitle:         titleFrom(blockName, section, text),
			Body:              strings.TrimSpace(text),
			Quelle:            quelleBase,
			SuggestedCategory: cat,
			MaximSlug:         maximSlug,
		}}
	}

	// Bullet list → one atom per top-level bullet (sub-bullets kept with their parent).
	if strings.Contains(text, "\n- ") || strings.HasPrefix(strings.TrimSpace(text), "- ") {
		var atoms []Atom
		var cur []string
		emit := func() {
			if len(cur) == 0 {
				return
			}
			b := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.Join(cur, "\n")), "- "))
			// Within a maxim, the maxim name is the SUBcategory (MaximSlug); each bullet's own title
			// must describe the bullet, so derive it from the body (pass no blockName).
			atoms = append(atoms, Atom{
				SeedTitle:         titleFrom("", section, b),
				Body:              b,
				Quelle:            quelleBase,
				SuggestedCategory: cat,
				MaximSlug:         maximSlug,
			})
			cur = nil
		}
		for _, l := range body {
			switch {
			case strings.HasPrefix(l, "- "):
				// a TOP-LEVEL bullet (column 0); an indented "  - " sub-bullet is NOT one and
				// stays with its parent below
				emit()
				cur = []string{l}
			case strings.TrimSpace(l) == "" && len(cur) == 0:
				continue // skip the intro paragraph's blank line
			case len(cur) > 0:
				cur = append(cur, l) // sub-bullet or continuation of the current atom
			}
			// a non-bullet line before the first bullet is the intro — dropped
		}
		emit()
		return atoms
	}

	// Prose paragraph → one atom.
	return []Atom{{
		SeedTitle:         titleFrom(blockName, section, text),
		Body:              strings.TrimSpace(text),
		Quelle:            quelleBase,
		SuggestedCategory: cat,
		MaximSlug:         maximSlug,
	}}
}

// titleFrom derives a short seed title: the maxim name if present, else the first clause of the body
// (up to a sentence break), capped. The classifier may replace it with a better one.
func titleFrom(blockName, section, body string) string {
	if blockName != "" {
		return humanize(blockName)
	}
	first := strings.TrimSpace(body)
	first = strings.SplitN(first, "\n", 2)[0]
	// cut at the first ":" or "." or "—" for a crisp title
	for _, sep := range []string{":", " — ", ". "} {
		if i := strings.Index(first, sep); i > 0 && i < 70 {
			first = first[:i]
			break
		}
	}
	if len(first) > 60 {
		// break at the last word boundary before the cap, not mid-word
		cut := first[:60]
		if i := strings.LastIndexByte(cut, ' '); i > 30 {
			cut = cut[:i]
		}
		first = strings.TrimSpace(cut)
	}
	if first == "" {
		return humanize(section)
	}
	return first
}

func humanize(tag string) string {
	parts := strings.Split(tag, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func firstNonBlank(lines []string) string {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

func closeTagName(t string) string {
	m := closeTag.FindStringSubmatch(t)
	if m == nil {
		return ""
	}
	return m[1]
}

// Slug reduces a title to a lowercase kebab path segment (ascii-only, matching the classifier's
// path rule): umlauts transliterated, everything else collapsed to single dashes.
func Slug(s string) string {
	return slug.Make(s, "-")
}
