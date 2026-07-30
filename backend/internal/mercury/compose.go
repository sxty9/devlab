// Prompt composition (S5, REQ-002.1 + REQ-003): the ONE path that turns the constitution plus one
// run definition into the prompt an execution hands the agent. BOTH kinds go through it — the
// automatic run and the one-time ToDo — because the constitution reaches every implementation
// through the PROMPT and nowhere else: the repositories' CLAUDE.md carries only the reference text
// that points back at the prompt (claudemd.go), and the rollout that once shipped the wording is
// abolished. A prompt that omits the wording therefore leaves the agent with no wording at all.
//
// This file is pure string assembly over records handed in; it reads no store. The corpus comes
// from the caller (runs.Catalog), the one place that talks to the axiom store.
package mercury

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// RunAxiom is one record folded into a prompt: an Axiom, an Implementierungsregel or a Laufregel.
type RunAxiom struct {
	ID    string
	Titel string
	Body  string
}

// Corpus is the constitution as composition receives it: EVERY axiom and EVERY
// Implementierungsregel, in full wording. Read separates "the store was read" from "the store
// could not be read" — an unread store must never look like an empty constitution (REQ-001.3), so
// ComposePrompt NAMES the gap in the prompt instead of carrying a silently empty block.
type Corpus struct {
	Axiome []RunAxiom
	Regeln []RunAxiom
	Read   bool
}

// Empty reports whether the corpus holds no record at all.
func (c Corpus) Empty() bool { return len(c.Axiome) == 0 && len(c.Regeln) == 0 }

// PromptSpec is the ONE input of the ONE composer: what the constitution says (Corpus), which run
// is being composed (Title, and Task/Attachments for a ToDo, Subject/Laufregeln for an automatic
// run), and nothing else. Todo selects the kind — one composer with one discriminator, not two
// similar siblings.
type PromptSpec struct {
	Todo   bool
	Title  string
	Corpus Corpus

	// kind=todo
	Task        string
	Attachments []TodoAttachment

	// kind=auto
	Subject    []RunAxiom // the axioms THIS run works through (its subject)
	Laufregeln []RunAxiom // how the run is to be conducted
}

// ComposePrompt builds the prompt of one execution. Structure is identical for both kinds — role,
// constitution in full wording, then what is specific to this run — so the two kinds read the same
// and no section carries a heading the other names differently.
func ComposePrompt(spec PromptSpec) string {
	var b strings.Builder
	if spec.Todo {
		writeTodoRole(&b, spec.Title)
	} else {
		writeRunRole(&b, spec.Title)
	}
	writeConstitution(&b, spec.Corpus)
	if spec.Todo {
		writeTask(&b, spec.Task, spec.Attachments)
	} else {
		writeRecords(&b, "Laufregeln (gelten für den gesamten Lauf)", spec.Laufregeln,
			"Für diesen Lauf sind keine Laufregeln hinterlegt.")
		writeSubject(&b, spec.Subject)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeRunRole(b *strings.Builder, title string) {
	b.WriteString("# Automatischer Holistic-Lauf: " + strings.TrimSpace(title) + "\n\n")
	b.WriteString("Du bist der automatische Runner der Holistic-Verfassung. Arbeite dieses Repository ")
	b.WriteString("vollständig, gründlich und eigenständig gegen den Gegenstand dieses Laufs durch: ")
	b.WriteString("analysiere den Ist-Zustand, implementiere die nötigen Änderungen und halte dich dabei ")
	b.WriteString("strikt an die Verfassung und an die Laufregeln.\n\n")
	b.WriteString(promptPrecedence)
	b.WriteString("Arbeite inkrementell: betrachte je Axiom nur die Commits, die seit der letzten Prüfung ")
	b.WriteString("dieses Axioms in diesem Repository hinzugekommen sind. Wurde ein Axiom hier noch nie ")
	b.WriteString("geprüft, prüfst du das gesamte Repository. Liegt der Abschnitt „Zuletzt geprüfter Stand ")
	b.WriteString("(für dieses Repository)\" vor, nennt er je Axiom den zuletzt geprüften Commit; fehlt er, ")
	b.WriteString("prüfe gegen das gesamte Repository.\n\n")
}

func writeTodoRole(b *strings.Builder, title string) {
	b.WriteString("# Konkretes ToDo: " + strings.TrimSpace(title) + "\n\n")
	b.WriteString("Du bist der Holistic-Runner und führst eine einmalige, konkrete Aufgabe aus. Die ")
	b.WriteString("Holistic-Axiome und Implementierungsregeln gelten unverändert und stehen unten in ")
	b.WriteString("diesem Prompt im vollen Wortlaut.\n\n")
	b.WriteString(promptPrecedence)
}

// promptPrecedence is the one sentence that makes the prompt the authority: the repositories'
// CLAUDE.md carries only the reference text, so any fuller wording an agent finds in a checkout is
// an outdated leftover and must not win over what stands here.
const promptPrecedence = "Der verbindliche Wortlaut der Verfassung steht vollständig in diesem " +
	"Prompt. Er hat Vorrang vor jeder anderen Fassung, die dir in einem Repository begegnet.\n\n"

// writeConstitution renders the corpus — the same two sections in the same order for both kinds.
// An unread store is named as such (never presented as an empty constitution), and a read store
// that holds nothing says exactly that.
func writeConstitution(b *strings.Builder, c Corpus) {
	if !c.Read {
		b.WriteString("## Verfassung — nicht gelesen\n\n")
		b.WriteString("Der verbindliche Wortlaut der Verfassung (Axiome und Implementierungsregeln) konnte ")
		b.WriteString("beim Komponieren dieses Prompts NICHT aus dem Bestand gelesen werden. Das ist eine ")
		b.WriteString("benannte Lücke und NICHT die Aussage, dass es keine Axiome gibt. Nenne diese Lücke im ")
		b.WriteString("Abschlussbericht und triff keine Entscheidung, die den fehlenden Wortlaut voraussetzt.\n\n")
		return
	}
	writeRecords(b, "Verfassung — Axiome", c.Axiome, "Der Bestand enthält derzeit keine Axiome.")
	writeRecords(b, "Verfassung — Implementierungsregeln", c.Regeln,
		"Der Bestand enthält derzeit keine Implementierungsregeln.")
}

// writeRecords renders one titled section of records in full wording. An empty section is stated
// in words rather than dropped: a missing heading would leave the agent guessing whether the
// section was empty or lost.
func writeRecords(b *strings.Builder, heading string, recs []RunAxiom, whenEmpty string) {
	b.WriteString("## " + heading + "\n\n")
	if len(recs) == 0 {
		b.WriteString(whenEmpty + "\n\n")
		return
	}
	for _, r := range recs {
		b.WriteString("### " + titleOr(r.Titel, r.ID) + "\n")
		b.WriteString(strings.TrimSpace(r.Body) + "\n\n")
	}
}

// writeSubject names the axioms this run works through. It lists TITLES only: their wording stands
// in the constitution section above, and repeating it here would be the same text twice in one
// prompt.
func writeSubject(b *strings.Builder, subject []RunAxiom) {
	b.WriteString("## Gegenstand dieses Laufs\n\n")
	if len(subject) == 0 {
		b.WriteString("Diesem Lauf ist derzeit kein Axiom zugeordnet.\n\n")
		return
	}
	b.WriteString("Arbeite in diesem Lauf genau die folgenden Axiome durch. Ihr Wortlaut steht oben unter ")
	b.WriteString("„Verfassung — Axiome\"; die übrigen Axiome und alle Implementierungsregeln gelten dabei ")
	b.WriteString("unverändert weiter.\n\n")
	for _, a := range subject {
		b.WriteString("- " + titleOr(a.Titel, a.ID) + "\n")
	}
	b.WriteString("\n")
}

// writeTask renders a ToDo's concrete task plus, when present, the media it must take into account.
func writeTask(b *strings.Builder, task string, atts []TodoAttachment) {
	b.WriteString("## Aufgabe\n\n")
	b.WriteString(strings.TrimSpace(task) + "\n\n")
	if len(atts) == 0 {
		return
	}
	b.WriteString("## Angehängte Medien\n\n")
	b.WriteString("Diesem ToDo sind Medien beigefügt, die bei der Umsetzung zu berücksichtigen sind. Sie liegen ")
	b.WriteString("im Arbeitsverzeichnis unter `" + TodoAttachmentDir + "/`. Sichte sie vor der Implementierung ")
	b.WriteString("und beziehe ihren Inhalt ein. Diese Dateien sind Kontext, NICHT Teil der Aufgabe — verändere ")
	b.WriteString("oder committe sie nicht.\n\n")
	for _, a := range atts {
		line := "- `" + TodoAttachmentRel(a.Filename) + "`"
		if strings.TrimSpace(a.MIME) != "" {
			line += " (" + a.MIME + ")"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
}

// LastCheck is one axiom's last examination of the repository about to be worked: the commit the
// repository stood at, and when. Mirrors runs.AxiomCheck without importing it (the runs package
// already depends on this one).
type LastCheck struct {
	Titel  string
	Commit string
	At     string // preformatted for the prompt; "" when unknown
}

// RepoScopeSection renders the per-repo addendum appended to a composed prompt at execution time:
// for each axiom of the run, the commit this repository was last examined against. It is per REPO,
// so it cannot live in the stored snapshot (which is shared across every repo of a sweep).
//
// Axioms with no entry are named explicitly rather than silently omitted — "not listed" would leave
// the agent guessing, while "never examined here ⇒ full repository" is an instruction it can follow.
func RepoScopeSection(axioms []RunAxiom, checked map[string]LastCheck) string {
	if len(axioms) == 0 {
		return ""
	}
	var known, fresh []string
	for _, a := range axioms {
		if c, ok := checked[a.ID]; ok && c.Commit != "" {
			line := "- " + titleOr(a.Titel, a.ID) + ": zuletzt geprüft bei Commit " + c.Commit
			if c.At != "" {
				line += " (" + c.At + ")"
			}
			known = append(known, line)
			continue
		}
		fresh = append(fresh, "- "+titleOr(a.Titel, a.ID))
	}

	var b strings.Builder
	b.WriteString("\n## Zuletzt geprüfter Stand (für dieses Repository)\n\n")
	if len(known) > 0 {
		b.WriteString("Für die folgenden Axiome ist ein geprüfter Stand vermerkt. Betrachte je Axiom NUR die ")
		b.WriteString("Commits seit dem genannten Commit (`git log <commit>..HEAD`, `git diff <commit>..HEAD`):\n")
		b.WriteString(strings.Join(known, "\n") + "\n")
	}
	if len(fresh) > 0 {
		if len(known) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Für die folgenden Axiome liegt keine frühere Prüfung dieses Repositories vor — prüfe sie ")
		b.WriteString("gegen das GESAMTE Repository:\n")
		b.WriteString(strings.Join(fresh, "\n") + "\n")
	}
	return b.String()
}

// TodoAttachmentDir is the workspace-relative directory into which the executor materializes a ToDo's
// media before the agent runs. Kept here so the prompt reference and the executor's write path stay a
// single source of truth. It is DevLab-internal context, removed again before anything is committed.
const TodoAttachmentDir = ".mercury/attachments"

// TodoAttachmentRel is the workspace-relative path of one attachment inside a workspace.
func TodoAttachmentRel(filename string) string {
	return TodoAttachmentDir + "/" + filename
}

// TodoAttachment is the descriptor of one attached medium referenced in a ToDo's prompt: enough for the
// agent to find and recognize the file (its workspace path + declared type), never its bytes.
type TodoAttachment struct {
	Filename string
	MIME     string
}

// PromptInputsHash is a stable fingerprint of everything ComposePrompt reads, so a write can tell
// whether a stored prompt has drifted from the store WITHOUT composing it again, and recompose
// exactly the affected runs. It covers identity + title + body of every record — including the
// whole corpus, because every prompt now carries the whole corpus — plus the run's own title and
// task: a pure rename genuinely changes the composed prompt and must change the hash (REQ-003).
// Record ORDER inside a section is normalized away (sorted) for the store-wide sections, while the
// run's own subject order is kept: a store re-order changes no prompt, a subject re-order does.
func PromptInputsHash(spec PromptSpec) string {
	lines := []string{
		"kind\t" + kindTag(spec.Todo),
		"title\t" + strings.TrimSpace(spec.Title),
		"task\t" + strings.TrimSpace(spec.Task),
		"corpus-read\t" + boolTag(spec.Corpus.Read),
	}
	for _, a := range spec.Subject {
		lines = append(lines, "subject\t"+recordLine(a)) // in the run's own order, which is part of the prompt
	}
	for _, a := range spec.Attachments {
		lines = append(lines, "attachment\t"+a.Filename+"\t"+a.MIME)
	}
	lines = append(lines, sortedRecordLines("axiom", spec.Corpus.Axiome)...)
	lines = append(lines, sortedRecordLines("regel", spec.Corpus.Regeln)...)
	lines = append(lines, sortedRecordLines("laufregel", spec.Laufregeln)...)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// sortedRecordLines renders one store-wide section order-independently.
func sortedRecordLines(tag string, recs []RunAxiom) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, tag+"\t"+recordLine(r))
	}
	sort.Strings(out)
	return out
}

func recordLine(a RunAxiom) string {
	return a.ID + "\t" + RunAxiomTitle(a) + "\t" + strings.TrimSpace(a.Body)
}

func kindTag(todo bool) string {
	if todo {
		return "todo"
	}
	return "auto"
}

func boolTag(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func titleOr(titel, id string) string {
	if strings.TrimSpace(titel) != "" {
		return titel
	}
	return id
}

// RunAxiomTitle returns a display title for a record (its Titel, or its id as a fallback).
func RunAxiomTitle(a RunAxiom) string { return titleOr(a.Titel, a.ID) }
