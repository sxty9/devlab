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
	b.WriteString("Arbeite inkrementell: betrachte je Axiom nur die Commits, die seit der letzten Prüfung ")
	b.WriteString("dieses Axioms in diesem Repository hinzugekommen sind. Wurde ein Axiom hier noch nie ")
	b.WriteString("geprüft, prüfst du das gesamte Repository. Welcher Stand je Axiom zuletzt geprüft wurde, ")
	b.WriteString("steht im Abschnitt „Zuletzt geprüfter Stand\" am Ende dieses Prompts.\n\n")

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

// LastCheck is one axiom's last examination of the repository about to be worked: the commit the
// repository stood at, and when. Mirrors runs.AxiomCheck without importing it (the runs package
// already depends on this one).
type LastCheck struct {
	Titel  string
	Commit string
	At     string // preformatted for the prompt; "" when unknown
}

// RepoScopeSection renders the per-repo addendum appended to a run's snapshot at execution time: for
// each axiom of the run, the commit this repository was last examined against. It is per REPO, so it
// cannot live in the stored snapshot (which is shared across every repo of a sweep).
//
// Axioms with no entry are named explicitly rather than silently omitted — "not listed" would leave the
// agent guessing, while "never examined here ⇒ full repository" is an instruction it can follow.
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
