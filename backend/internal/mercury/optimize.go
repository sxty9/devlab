package mercury

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The "Mit KI optimieren" action polishes a record without changing its meaning: fix orthography,
// generalize the wording (instance-neutral, formal), and tighten it. Same free-text-then-validate
// discipline as classification (classify.go).

// Optimized is the AI-polished form of a record.
type Optimized struct {
	Titel string `json:"titel"`
	Body  string `json:"body"`
}

// OptimizePrompt asks the model to polish title + body: orthography, general/instance-neutral wording,
// brevity — WITHOUT altering the meaning or inventing content. For an axiom, any meta-axioms are folded
// in as binding requirements the polished form must satisfy, so optimization already yields a conforming
// axiom (the strict conformance gate is the backstop).
func OptimizePrompt(titel, body, ns string, metas []RunAxiom) string {
	noun := nsLabel(ns)
	var b strings.Builder
	b.WriteString("Du bist Lektor der Holistic-Verfassung. Optimiere den folgenden Eintrag vom Typ „" + noun + "\":\n")
	b.WriteString("- korrigiere Rechtschreibung und Grammatik;\n")
	b.WriteString("- formuliere allgemeingültig und instanz-neutral (keine persönlichen Daten, keine konkreten Pfade oder Domains);\n")
	b.WriteString("- fasse dich kürzer, präziser und formal, ohne die Aussage zu verändern;\n")
	b.WriteString("- gib einen knappen, treffenden Titel.\n")
	b.WriteString("Ändere NICHT die inhaltliche Bedeutung und erfinde nichts hinzu.\n")
	if ns == NsAxiome && len(metas) > 0 {
		b.WriteString("\nDie optimierte Fassung MUSS zusätzlich alle folgenden verbindlichen Anforderungen an Axiome (Meta-Axiome) erfüllen:\n")
		for _, m := range metas {
			b.WriteString("- " + titleOr(m.Titel, m.ID) + ": " + Snippet(m.Body, 200) + "\n")
		}
	}
	b.WriteString("\nTitel: " + titel + "\n")
	b.WriteString("Inhalt: " + body + "\n\n")
	b.WriteString("Antworte mit GENAU einem JSON-Objekt, ohne Codefence, ohne weiteren Text:\n")
	b.WriteString(`{"titel":"<optimierter Titel>","body":"<optimierter Text>"}` + "\n")
	return b.String()
}

// ParseOptimized extracts and validates the polished record.
func ParseOptimized(output string) (Optimized, error) {
	raw, ok := firstJSONObject(output)
	if !ok {
		return Optimized{}, ErrNoJSON
	}
	var o Optimized
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return Optimized{}, fmt.Errorf("%w: kein gültiges JSON", ErrInvalidPlacement)
	}
	o.Titel = strings.TrimSpace(o.Titel)
	o.Body = strings.TrimSpace(o.Body)
	if o.Titel == "" || o.Body == "" {
		return Optimized{}, fmt.Errorf("%w: titel oder body leer", ErrInvalidPlacement)
	}
	return o, nil
}
