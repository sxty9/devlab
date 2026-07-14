package mercury

import (
	"bufio"
	"strings"
)

// An axiom record is markdown with a small YAML-ish front-matter block: DevLab owns the id (stable
// across a re-file, because scheme carries the content with the path on a move), the model proposes
// the title, and quelle points back at the source it was decomposed from.
//
//	---
//	id: ax_01J8Z9Q4K7T3M2
//	titel: Kein paralleler Datenpfad
//	quelle: axioms/CLAUDE.MD.md#holistic_architecture_maxims/Single Source of Truth
//	---
//	Existiert für die Entität bereits ein Zugangspunkt? Zwingend wiederverwenden.

// Axiom is a parsed record: its front-matter fields plus the body below them.
type Axiom struct {
	ID     string `json:"id"`
	Titel  string `json:"titel"`
	Quelle string `json:"quelle,omitempty"`
	Body   string `json:"body"`
}

// ParseAxiom splits a record into front-matter and body. A record without a leading "---" block is
// treated as all body (older or hand-dropped content), so nothing is ever lost.
func ParseAxiom(content string) Axiom {
	s := bufio.NewScanner(strings.NewReader(content))
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)

	if !s.Scan() || strings.TrimSpace(s.Text()) != "---" {
		return Axiom{Body: strings.TrimRight(content, "\n")}
	}
	var ax Axiom
	for s.Scan() {
		line := s.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "id":
			ax.ID = val
		case "titel":
			ax.Titel = val
		case "quelle":
			ax.Quelle = val
		}
	}
	var body strings.Builder
	for s.Scan() {
		body.WriteString(s.Text())
		body.WriteByte('\n')
	}
	ax.Body = strings.TrimRight(body.String(), "\n")
	return ax
}

// Render serialises an Axiom back to its on-disk form: the front-matter block followed by the body.
// The inverse of ParseAxiom for the fields it carries.
func Render(ax Axiom) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: " + ax.ID + "\n")
	b.WriteString("titel: " + ax.Titel + "\n")
	if ax.Quelle != "" {
		b.WriteString("quelle: " + ax.Quelle + "\n")
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimRight(ax.Body, "\n"))
	b.WriteString("\n")
	return b.String()
}
