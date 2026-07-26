package mercury

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// A run's Claude prompt is composed from its Axioms + all global Laufregeln. This is pure string
// assembly (mirroring RenderBlock): a fixed operating preamble, the Laufregeln (HOW to run), then the
// run's Axioms (WHAT to enforce). The result is snapshotted into the run so execution needs no store.

// RunAxiom is a record folded into a run's prompt (an Axiom of the run, or a global Laufregel).
type RunAxiom struct {
	ID    string
	Titel string
	Body  string
}

// ComposeRunPrompt builds the final prompt for a run from its axioms + all global Laufregeln.
func ComposeRunPrompt(runName string, axioms, laufregeln []RunAxiom) string {
	var b strings.Builder
	b.WriteString("# Automatischer Holistic-Lauf: " + strings.TrimSpace(runName) + "\n\n")
	b.WriteString("Du bist der automatische Runner der Holistic-Verfassung. Arbeite dieses Repository ")
	b.WriteString("vollständig, gründlich und eigenständig gegen die unten stehenden Axiome durch: ")
	b.WriteString("analysiere den Ist-Zustand, implementiere die nötigen Änderungen und halte dich dabei ")
	b.WriteString("strikt an die Laufregeln.\n\n")
	b.WriteString("Arbeite inkrementell: beachte die in der CLAUDE.md des Repositories hinterlegten Axiome ")
	b.WriteString("und den dort dokumentierten Checkpoint-Stand, und betrachte je Axiom nur die Änderungen ")
	b.WriteString("seit dessen letzter Prüfung (nie geprüft ⇒ das gesamte Repository).\n\n")

	if len(laufregeln) > 0 {
		b.WriteString("## Laufregeln (gelten für den gesamten Lauf)\n\n")
		for _, r := range laufregeln {
			b.WriteString("### " + titleOr(r.Titel, r.ID) + "\n")
			b.WriteString(strings.TrimSpace(r.Body) + "\n\n")
		}
	}

	b.WriteString("## Axiome (der Gegenstand dieses Laufs)\n\n")
	for _, a := range axioms {
		b.WriteString("### " + titleOr(a.Titel, a.ID) + "\n")
		b.WriteString(strings.TrimSpace(a.Body) + "\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
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

// ComposeTodoPrompt builds the prompt for a one-time ToDo: the concrete task itself. No axioms and no
// Laufregeln are folded in — the constitution already reaches the agent through the repo's CLAUDE.md
// (that is exactly what the rollout is for), so a ToDo carries only what is specific to it. Attached
// media, when present, is announced with its workspace location so the agent takes it into account.
func ComposeTodoPrompt(name, task, newRepo string, attachments []TodoAttachment) string {
	var b strings.Builder
	b.WriteString("# Konkretes ToDo: " + strings.TrimSpace(name) + "\n\n")
	b.WriteString("Du bist der Holistic-Runner und führst eine einmalige, konkrete Aufgabe aus. Die ")
	b.WriteString("Holistic-Axiome und Implementierungsregeln gelten unverändert — sie stehen in der ")
	b.WriteString("CLAUDE.md dieses Repositories. Lies sie und halte dich strikt daran.\n\n")
	if strings.TrimSpace(newRepo) != "" {
		b.WriteString("Dieses Repository (`" + strings.TrimSpace(newRepo) + "`) ist NEU und noch leer bzw. frisch ")
		b.WriteString("angelegt: baue den Service von Grund auf nach dem Holistic-Standard auf (Struktur, CLI, ")
		b.WriteString("Schnittstellen, CLAUDE.md).\n\n")
	}
	b.WriteString("## Aufgabe\n\n")
	b.WriteString(strings.TrimSpace(task) + "\n")
	if len(attachments) > 0 {
		b.WriteString("\n## Angehängte Medien\n\n")
		b.WriteString("Diesem ToDo sind Medien beigefügt, die bei der Umsetzung zu berücksichtigen sind. Sie liegen ")
		b.WriteString("im Arbeitsverzeichnis unter `" + TodoAttachmentDir + "/`. Sichte sie vor der Implementierung ")
		b.WriteString("und beziehe ihren Inhalt ein. Diese Dateien sind Kontext, NICHT Teil der Aufgabe — verändere ")
		b.WriteString("oder committe sie nicht.\n\n")
		for _, a := range attachments {
			line := "- `" + TodoAttachmentRel(a.Filename) + "`"
			if strings.TrimSpace(a.MIME) != "" {
				line += " (" + a.MIME + ")"
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// RunInputsHash is a stable fingerprint of the scheme inputs a run's prompt was composed from, so a
// write can tell whether a run's snapshot has drifted from the store and recompose exactly the affected
// runs. It hashes identity + title + body of each record: the title is the heading each axiom/Laufregel
// gets in the composed prompt, so a pure rename genuinely changes the prompt and must change the hash.
// Ordering is normalized away (sorted), because composition order follows the run's own axiom list, not
// the store's — a store re-order does not change any run's prompt.
func RunInputsHash(axioms, laufregeln []RunAxiom) string {
	lines := make([]string, 0, len(axioms)+len(laufregeln))
	for _, a := range axioms {
		lines = append(lines, "a\t"+a.ID+"\t"+RunAxiomTitle(a)+"\t"+strings.TrimSpace(a.Body))
	}
	for _, r := range laufregeln {
		lines = append(lines, "r\t"+r.ID+"\t"+RunAxiomTitle(r)+"\t"+strings.TrimSpace(r.Body))
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

func titleOr(titel, id string) string {
	if strings.TrimSpace(titel) != "" {
		return titel
	}
	return id
}

// RunAxiomTitle returns a display title for a record (its Titel, or its id as a fallback).
func RunAxiomTitle(a RunAxiom) string { return titleOr(a.Titel, a.ID) }
