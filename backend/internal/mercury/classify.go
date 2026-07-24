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

// ValidPlacementPath reports whether p is an acceptable classifier target in namespace ns: a valid
// record path (lowercase kebab segments, ending in a slug + ".md") that actually sits under ns/. The
// kebab-only rule is what stops "SSOT" and "ssot", or "wieder-verwendung" and "wiederverwendung",
// from becoming distinct categories. Depth is unconstrained and a top-level record (no category) is
// allowed, so regeln/ and laeufe/ — whose trees are flat — file exactly like axiome/.
func ValidPlacementPath(p, ns string) bool {
	return recordRe.MatchString(p) && strings.HasPrefix(p, ns+"/")
}

// nsLabel names a namespace with a human singular noun for the classifier prompt (used as a type
// label, so the wording is right whether it files an Axiom, a Regel or a Lauf).
func nsLabel(ns string) string {
	switch ns {
	case NsRegeln:
		return "Implementierungsregel"
	case NsLaeufe:
		return "automatisierter Lauf"
	case NsMeta:
		return "Meta-Axiom"
	default:
		return "Axiom"
	}
}

// recordRe validates a user-supplied move/rename target: the Mercury namespaces (axiome, regeln,
// laeufe, meta — the tree's sections), lowercase kebab segments, ending in a slug + ".md".
var recordRe = regexp.MustCompile(`^(axiome|regeln|laeufe|meta)(/[a-z0-9][a-z0-9-]*)*/[a-z0-9][a-z0-9-]*\.md$`)

// categoryRe validates a category prefix (a move-category source/target): a namespace and at least
// one kebab segment, no trailing ".md".
var categoryRe = regexp.MustCompile(`^(axiome|regeln|laeufe|meta)(/[a-z0-9][a-z0-9-]*)+$`)

// ValidRecordPath reports whether p is an acceptable axiom/rule record path for a move or rename.
func ValidRecordPath(p string) bool { return recordRe.MatchString(p) }

// ValidCategory reports whether p is an acceptable category prefix for a category rename/move.
func ValidCategory(p string) bool { return categoryRe.MatchString(p) }

// ErrNoJSON means the model produced no parseable object; ErrInvalidPlacement means it parsed but
// broke the contract. Both drive a retry with a correction line.
var (
	ErrNoJSON           = errors.New("no JSON object in model output")
	ErrInvalidPlacement = errors.New("placement violates the contract")
)

// Categories lists the existing category paths in namespace ns (a category is any path prefix that
// holds a record). Because scheme has no empty folders, this is the complete, exact set — which is
// what the model is shown so it reuses categories instead of inventing near-duplicates.
func Categories(paths []string, ns string) []string {
	set := map[string]bool{}
	for _, p := range paths {
		if !strings.HasPrefix(p, ns+"/") {
			continue
		}
		segs := strings.Split(strings.TrimSuffix(p, ".md"), "/")
		for i := 2; i <= len(segs); i++ { // prefixes from ns/<a> up to the leaf's parent
			set[strings.Join(segs[:i-1], "/")] = true
		}
	}
	delete(set, ns)
	out := make([]string, 0, len(set))
	for c := range set {
		if c != "" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// ClassifyPrompt builds the instruction: the existing categories WITH their current entries (so the
// model can read each category's THEME, not just its name — the key to correct filing), the new
// record, and the exact JSON contract. members maps a category path to the titles of the records in
// it. ns is the target namespace (axiome/regeln/laeufe). hint is an optional seed category (migration
// only); correction is empty on the first attempt, else the specific violation to fix.
func ClassifyPrompt(categories []string, members map[string][]string, titel, body, hint, correction, ns string) string {
	noun := nsLabel(ns)
	var b strings.Builder
	b.WriteString("Du bist der Kurator der Holistic-Verfassung. Ein neuer Eintrag vom Typ „" + noun + "\" soll ")
	b.WriteString("in den bestehenden Kategoriebaum unter `" + ns + "/` einsortiert werden.\n\n")

	b.WriteString("Bestehende Kategorien mit ihren Einträgen (Pfad — Titel: Kurzfassung). Erkenne daran das ")
	b.WriteString("THEMA jeder Kategorie UND ob es den Eintrag inhaltlich schon gibt:\n")
	if len(categories) == 0 {
		b.WriteString("(noch keine — du legst die erste Struktur an)\n")
	} else {
		for _, c := range categories {
			b.WriteString("  " + c + "\n")
			for i, m := range members[c] {
				if i >= 8 {
					b.WriteString("      …\n")
					break
				}
				b.WriteString("      - " + m + "\n")
			}
		}
	}

	b.WriteString("\nDer neue Eintrag (" + noun + "):\n")
	b.WriteString("Titel: " + titel + "\n")
	b.WriteString("Inhalt: " + body + "\n")
	if hint != "" {
		b.WriteString("Vorgeschlagene Oberkategorie (Ausgangspunkt, du darfst verfeinern): " + ns + "/" + hint + "\n")
	}
	b.WriteString("\n")

	b.WriteString("Ordne nach INHALT und THEMA ein: Wähle die Kategorie, deren bestehende Einträge dem neuen ")
	b.WriteString("Eintrag inhaltlich am nächsten stehen — orientiere dich an der Bedeutung, nicht an einzelnen ")
	b.WriteString("Stichworten. Weiche NICHT auf eine generische Kategorie (etwa prozess, unsortiert, allgemein) ")
	b.WriteString("aus, wenn eine speziellere thematisch passt. Lege nur dann eine neue Kategorie an, wenn wirklich ")
	b.WriteString("keine bestehende passt.\n\n")

	// English-first (Implementierungsregel): category names are structure, so they are English. Mixing
	// languages is exactly how a tree sprawls into near-duplicate categories.
	b.WriteString("DUBLETTEN: Prüfe ZUERST, ob einer der oben gelisteten Einträge INHALTLICH dasselbe aussagt — ")
	b.WriteString("auch wenn Titel und Wortlaut abweichen. Wenn ja, setze duplikat_von auf dessen EXAKTEN Pfad ")
	b.WriteString("aus der Liste (nicht auf einen erfundenen). Nur wenn der Eintrag inhaltlich wirklich neu ist, ")
	b.WriteString("lass duplikat_von null.\n\n")

	b.WriteString("KATEGORIENAMEN SIND ENGLISCH (Regel „Implementierung auf Englisch\"): Muss doch einmal eine ")
	b.WriteString("neue Kategorie entstehen, benenne sie englisch, kleingeschrieben, mit Bindestrichen ")
	b.WriteString("(z. B. single-source-of-truth, not einzelquelle). Bestehende Kategorien werden NIE umbenannt — ")
	b.WriteString("nutze sie exakt so, wie sie oben stehen. Erfinde keine Kategorie, die eine bestehende nur ")
	b.WriteString("übersetzt oder umschreibt.\n\n")

	b.WriteString("Antworte mit GENAU einem JSON-Objekt, ohne Codefence, ohne weiteren Text:\n")
	b.WriteString(`{"pfad":"` + ns + `/<kategorie>/…/<slug>.md","beschreibung":"<1 Satz, worum es geht>",`)
	b.WriteString(`"titel":"<knapper Titel>","neue_kategorie":<true|false>,"duplikat_von":<null|"` + ns + `/…/x.md">}` + "\n")
	b.WriteString("Regeln: pfad beginnt mit " + ns + "/ und endet auf .md; nur Kleinbuchstaben, Ziffern und ")
	b.WriteString("Bindestriche in jedem Segment (keine Umlaute, keine Leerzeichen, kein CamelCase); flache ")
	b.WriteString("Ablage direkt unter " + ns + "/ ist erlaubt; setze neue_kategorie:true NUR bei einer wirklich ")
	b.WriteString("neuen Kategorie; setze duplikat_von, wenn ein inhaltsgleicher Eintrag schon existiert.\n")

	if correction != "" {
		b.WriteString("\nKORREKTUR: " + correction + " Antworte erneut mit einem korrigierten JSON-Objekt.\n")
	}
	return b.String()
}

// ParsePlacement extracts and validates the model's JSON for a placement in namespace ns.
// knownCategories is the existing set; a non-new category must be one of them (the namespace root
// itself always exists, so a top-level record needs no new category). Returns a typed error whose
// message doubles as the correction line for a retry.
func ParsePlacement(output string, knownCategories []string, ns string) (Placement, error) {
	raw, ok := firstJSONObject(output)
	if !ok {
		return Placement{}, ErrNoJSON
	}
	var p Placement
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Placement{}, fmt.Errorf("%w: kein gültiges JSON", ErrInvalidPlacement)
	}
	p.Pfad = strings.TrimSpace(p.Pfad)
	if !ValidPlacementPath(p.Pfad, ns) {
		return p, fmt.Errorf("%w: pfad %q muss unter %s/ liegen, nur Kleinbuchstaben/Ziffern/Bindestriche je Segment nutzen und auf .md enden", ErrInvalidPlacement, p.Pfad, ns)
	}
	if strings.TrimSpace(p.Beschreibung) == "" {
		return p, fmt.Errorf("%w: beschreibung fehlt", ErrInvalidPlacement)
	}
	if !p.NeueKategorie {
		parent := p.Pfad[:strings.LastIndex(p.Pfad, "/")]
		if parent != ns && !contains(knownCategories, parent) {
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
