package mercury

import (
	"bufio"
	"errors"
	"fmt"
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

// ErrRecordDefect marks a record that could not be read as front matter plus wording. It exists so a
// caller can refuse a defective record instead of passing an empty axiom on: an axiom whose wording
// is silently lost still travels through every run prompt and into every generated CLAUDE.md, where
// nothing can notice the loss any more.
var ErrRecordDefect = errors.New("axiom record defect")

// The defects a record can carry. Each one names what is wrong with the RECORD, never with the caller.
const (
	defectUnterminatedFrontMatter = `front matter opened with "---" but was never closed`
	defectNoWording               = "record carries no wording"
	defectUnreadable              = "record could not be read to the end"
)

// Axiom is a parsed record: its front-matter fields plus the body below them.
type Axiom struct {
	ID     string `json:"id"`
	Titel  string `json:"titel"`
	Quelle string `json:"quelle,omitempty"`
	Body   string `json:"body"`
	// Defect is empty for every record that read cleanly, and otherwise names why it did not. It is
	// not part of the record on disk (Render never writes it) — it is what ParseAxiom found.
	Defect string `json:"defect,omitempty"`
}

// Err reports a parse defect as an error wrapping ErrRecordDefect, or nil for a clean record. The
// counterpart to Defect for Go callers: errors.Is(ax.Err(), mercury.ErrRecordDefect).
func (a Axiom) Err() error {
	if a.Defect == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRecordDefect, a.Defect)
}

// ParseAxiom splits a record into front-matter and body. A record without a leading "---" block is
// treated as all body (older or hand-dropped content), so nothing is ever lost.
//
// Its invariant: a non-empty record NEVER parses to an empty body, and a body that came out empty is
// always named in Defect. Where the front matter cannot be delimited — the block opens with "---" and
// never closes — the record is kept VERBATIM as the body, because without the closing delimiter there
// is no way to tell the fields from the wording; the fields are still read best-effort, so a defective
// record keeps its stable id (and can be repaired in place) instead of vanishing from every catalog
// that keys by id.
func ParseAxiom(content string) Axiom {
	verbatim := strings.TrimRight(content, "\n")
	s := bufio.NewScanner(strings.NewReader(content))
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)

	// No first line at all: an empty record, or one line longer than the scanner's limit.
	if !s.Scan() {
		return defective(Axiom{Body: verbatim}, scanDefect(s))
	}
	if strings.TrimSpace(s.Text()) != "---" {
		return defective(Axiom{Body: verbatim}, scanDefect(s))
	}

	var ax Axiom
	closed := false
	for s.Scan() {
		line := s.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
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
	if !closed {
		defect := scanDefect(s)
		if defect == "" {
			defect = defectUnterminatedFrontMatter
		}
		ax.Body = verbatim
		return defective(ax, defect)
	}

	var body strings.Builder
	for s.Scan() {
		body.WriteString(s.Text())
		body.WriteByte('\n')
	}
	if defect := scanDefect(s); defect != "" {
		// The wording could not be read to its end — keep the record whole rather than a truncation.
		ax.Body = verbatim
		return defective(ax, defect)
	}
	ax.Body = strings.TrimRight(body.String(), "\n")
	return defective(ax, "")
}

// defective stamps a defect on a parse result and enforces the invariant: an empty body is a defect in
// its own right, so no caller can receive a wordless axiom without being told.
func defective(ax Axiom, defect string) Axiom {
	ax.Defect = defect
	if ax.Defect == "" && strings.TrimSpace(ax.Body) == "" {
		ax.Defect = defectNoWording
	}
	return ax
}

// scanDefect names a scanner failure (a line beyond the buffer limit), or "" when the scanner simply
// reached the end of the record.
func scanDefect(s *bufio.Scanner) string {
	if err := s.Err(); err != nil {
		return defectUnreadable + ": " + err.Error()
	}
	return ""
}

// Render serialises an Axiom back to its on-disk form: the front-matter block followed by the body.
// The inverse of ParseAxiom for the fields it carries; a parse defect is not one of them — it
// describes the record that was read, not the record to write.
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
