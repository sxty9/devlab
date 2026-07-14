package mercury

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Classification places a new axiom in the category tree. aigentic returns free text, so the
// contract is enforced here: one JSON object, a path stricter than scheme's own rule, an existing
// parent unless the model declares a new category, a non-empty description. The strictness is what
// stops near-duplicate categories that differ only by case, umlaut or spacing.

// Placement is the model's proposed home for an axiom.
type Placement struct {
	Pfad          string `json:"pfad"`
	Beschreibung  string `json:"beschreibung"`
	Titel         string `json:"titel"`
	NeueKategorie bool   `json:"neue_kategorie"`
	DuplikatVon   string `json:"duplikat_von"`
}

// pathRe is deliberately stricter than scheme's path rule: lowercase kebab segments only, 1–5
// category levels under axiome/, ending in a slug + ".md". This is what prevents "SSOT" and "ssot"
// or "wiederverwendung" and "wieder-verwendung" becoming distinct categories.
var pathRe = regexp.MustCompile(`^axiome(/[a-z0-9][a-z0-9-]*){1,5}/[a-z0-9][a-z0-9-]*\.md$`)

// ErrNoJSON means the model produced no parseable object; ErrInvalidPlacement means it parsed but
// broke the contract. Both drive a retry with a correction line.
var (
	ErrNoJSON           = errors.New("no JSON object in model output")
	ErrInvalidPlacement = errors.New("placement violates the contract")
)

// Categories lists the existing category paths (a category is any path prefix that holds an axiom).
// Because scheme has no empty folders, this is the complete, exact set — which is what the model is
// shown so it reuses categories instead of inventing near-duplicates.
func Categories(paths []string) []string {
	set := map[string]bool{}
	for _, p := range paths {
		if !strings.HasPrefix(p, NsAxiome+"/") {
			continue
		}
		segs := strings.Split(strings.TrimSuffix(p, ".md"), "/")
		for i := 2; i <= len(segs); i++ { // prefixes from axiome/<a> up to the leaf's parent
			set[strings.Join(segs[:i-1], "/")] = true
		}
	}
	delete(set, NsAxiome)
	out := make([]string, 0, len(set))
	for c := range set {
		if c != "" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// ClassifyPrompt builds the instruction: the existing tree, the axiom, and the exact JSON contract.
// correction is empty on the first attempt, else the specific violation to fix.
func ClassifyPrompt(categories []string, titel, body, correction string) string {
	var b strings.Builder
	b.WriteString("Du bist der Kurator der Holistic-Axiome. Ein neues Axiom soll in einen bestehenden, ")
	b.WriteString("beliebig tiefen Kategoriebaum unter `axiome/` einsortiert werden. Wähle den passendsten ")
	b.WriteString("bestehenden Pfad; nur wenn wirklich keine Kategorie passt, lege eine neue an.\n\n")

	b.WriteString("Bestehende Kategorien:\n")
	if len(categories) == 0 {
		b.WriteString("(noch keine — du legst die erste Struktur an)\n")
	} else {
		for _, c := range categories {
			b.WriteString("  " + c + "\n")
		}
	}

	b.WriteString("\nDas neue Axiom:\n")
	b.WriteString("Titel: " + titel + "\n")
	b.WriteString("Inhalt: " + body + "\n\n")

	b.WriteString("Antworte mit GENAU einem JSON-Objekt, ohne Codefence, ohne weiteren Text:\n")
	b.WriteString(`{"pfad":"axiome/<kategorie>/…/<slug>.md","beschreibung":"<1 Satz, worum es geht>",`)
	b.WriteString(`"titel":"<knapper Titel>","neue_kategorie":<true|false>,"duplikat_von":<null|"axiome/…/x.md">}` + "\n")
	b.WriteString("Regeln: pfad beginnt mit axiome/ und endet auf .md; nur Kleinbuchstaben, Ziffern und ")
	b.WriteString("Bindestriche in jedem Segment (keine Umlaute, keine Leerzeichen, kein CamelCase); 1 bis 5 ")
	b.WriteString("Kategorieebenen; wähle eine bestehende Kategorie wo möglich; setze duplikat_von, wenn ein ")
	b.WriteString("inhaltsgleiches Axiom schon existiert.\n")

	if correction != "" {
		b.WriteString("\nKORREKTUR: " + correction + " Antworte erneut mit einem korrigierten JSON-Objekt.\n")
	}
	return b.String()
}

// ParsePlacement extracts and validates the model's JSON. knownCategories is the existing set;
// a non-new category must be one of them. Returns a typed error whose message doubles as the
// correction line for a retry.
func ParsePlacement(output string, knownCategories []string) (Placement, error) {
	raw, ok := firstJSONObject(output)
	if !ok {
		return Placement{}, ErrNoJSON
	}
	var p Placement
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Placement{}, fmt.Errorf("%w: kein gültiges JSON", ErrInvalidPlacement)
	}
	p.Pfad = strings.TrimSpace(p.Pfad)
	if !pathRe.MatchString(p.Pfad) {
		return p, fmt.Errorf("%w: pfad %q entspricht nicht ^axiome(/kebab){1,5}/slug.md$", ErrInvalidPlacement, p.Pfad)
	}
	if strings.TrimSpace(p.Beschreibung) == "" {
		return p, fmt.Errorf("%w: beschreibung fehlt", ErrInvalidPlacement)
	}
	if !p.NeueKategorie {
		parent := p.Pfad[:strings.LastIndex(p.Pfad, "/")]
		if !contains(knownCategories, parent) {
			return p, fmt.Errorf("%w: kategorie %q existiert nicht; setze neue_kategorie:true oder wähle eine bestehende", ErrInvalidPlacement, parent)
		}
	}
	return p, nil
}

// firstJSONObject returns the first balanced {…} in s (brace scan, not regex — a regex cannot match
// nested braces, and the model may wrap the object in prose or a code fence).
func firstJSONObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// inside a string: ignore braces
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
