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

// ComposeTodoPrompt builds the prompt for a one-time ToDo: the concrete task itself. No axioms and no
// Laufregeln are folded in — the constitution already reaches the agent through the repo's CLAUDE.md
// (that is exactly what the rollout is for), so a ToDo carries only what is specific to it.
func ComposeTodoPrompt(name, task, newRepo string) string {
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
	return b.String()
}

// RunInputsHash is a stable fingerprint of the scheme inputs a run's prompt was composed from, so the
// UI can flag a stored snapshot as stale once the underlying axioms/Laufregeln have changed. It hashes
// only identity + body (not titles or ordering), so a pure re-title doesn't spuriously mark stale.
func RunInputsHash(axioms, laufregeln []RunAxiom) string {
	lines := make([]string, 0, len(axioms)+len(laufregeln))
	for _, a := range axioms {
		lines = append(lines, "a\t"+a.ID+"\t"+strings.TrimSpace(a.Body))
	}
	for _, r := range laufregeln {
		lines = append(lines, "r\t"+r.ID+"\t"+strings.TrimSpace(r.Body))
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
