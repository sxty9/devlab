package mercury

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Meta-Axiome are the binding requirements an axiom itself must satisfy (e.g. "an axiom names a software
// implementation standard, not an ongoing data operation"; "an axiom is unambiguous to an AI on its
// own"). Conformance checking is the STRICT gate that enforces them on every axiom — same free-text-then-
// validate discipline as classification (classify.go). Meta-Axiome never reach the runs: they check
// axioms, and the (now sharper) axioms drive the runs. The influence is purely transitive.

// Conformance is the model's verdict on whether an axiom satisfies every meta-axiom, with a corrected
// version to offer when it does not.
type Conformance struct {
	Conforms   bool        `json:"conforms"`
	Violations []Violation `json:"violations"`
	Titel      string      `json:"titel"` // a corrected, conforming title (when !Conforms)
	Body       string      `json:"body"`  // a corrected, conforming body (when !Conforms)
}

// Violation names one unmet meta-axiom and what specifically is wrong.
type Violation struct {
	Meta  string `json:"meta"`  // the meta-axiom's title
	Issue string `json:"issue"` // the concrete way the axiom fails it
}

// ConformancePrompt asks the model to check an axiom strictly against ALL meta-axioms and, when it
// fails, to produce a corrected version that satisfies them without changing the axiom's intent.
func ConformancePrompt(titel, body string, metas []RunAxiom, correction string) string {
	var b strings.Builder
	b.WriteString("Du bist der strenge Prüfer der Holistic-Verfassung. Ein Axiom MUSS alle folgenden ")
	b.WriteString("Meta-Axiome (verbindliche Anforderungen an die Formulierung von Axiomen) erfüllen. ")
	b.WriteString("Prüfe das gegebene Axiom streng gegen JEDES Meta-Axiom.\n\n")

	b.WriteString("## Meta-Axiome (verbindliche Anforderungen)\n\n")
	for _, m := range metas {
		b.WriteString("### " + titleOr(m.Titel, m.ID) + "\n")
		b.WriteString(strings.TrimSpace(m.Body) + "\n\n")
	}

	b.WriteString("## Zu prüfendes Axiom\n")
	b.WriteString("Titel: " + titel + "\n")
	b.WriteString("Inhalt: " + body + "\n\n")

	b.WriteString("Erfüllt das Axiom ALLE Meta-Axiome, setze conforms:true und violations:[]. ")
	b.WriteString("Andernfalls setze conforms:false, liste JEDEN Verstoß (welches Meta-Axiom, was konkret ")
	b.WriteString("verletzt ist) und liefere in titel/body eine KORRIGIERTE Fassung, die alle Meta-Axiome ")
	b.WriteString("erfüllt, ohne die inhaltliche Absicht des Axioms zu verändern und ohne etwas zu erfinden.\n\n")

	b.WriteString("Antworte mit GENAU einem JSON-Objekt, ohne Codefence, ohne weiteren Text:\n")
	b.WriteString(`{"conforms":<true|false>,"violations":[{"meta":"<Meta-Axiom-Titel>","issue":"<konkreter Verstoß>"}],`)
	b.WriteString(`"titel":"<korrigierter Titel>","body":"<korrigierter Text>"}` + "\n")

	if correction != "" {
		b.WriteString("\nKORREKTUR: " + correction + " Antworte erneut mit einem korrigierten JSON-Objekt.\n")
	}
	return b.String()
}

// ParseConformance extracts and validates the model's verdict. When the axiom does not conform, a
// concrete corrected title+body and at least one violation are required (so the UI can offer a fix);
// the error message doubles as a retry correction, as in ParsePlacement.
func ParseConformance(output string) (Conformance, error) {
	raw, ok := firstJSONObject(output)
	if !ok {
		return Conformance{}, ErrNoJSON
	}
	var c Conformance
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Conformance{}, fmt.Errorf("%w: kein gültiges JSON", ErrInvalidPlacement)
	}
	c.Titel = strings.TrimSpace(c.Titel)
	c.Body = strings.TrimSpace(c.Body)
	if c.Conforms {
		return c, nil
	}
	// A non-conforming verdict must be actionable.
	viol := c.Violations[:0]
	for _, v := range c.Violations {
		if strings.TrimSpace(v.Issue) != "" {
			viol = append(viol, v)
		}
	}
	c.Violations = viol
	if len(c.Violations) == 0 {
		return c, fmt.Errorf("%w: conforms=false, aber keine violations genannt", ErrInvalidPlacement)
	}
	if c.Body == "" {
		return c, fmt.Errorf("%w: conforms=false, aber kein korrigierter body geliefert", ErrInvalidPlacement)
	}
	return c, nil
}
