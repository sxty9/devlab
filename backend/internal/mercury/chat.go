package mercury

import "strings"

// The Mercury-wide assistant. It is NOT run-specific: it sees the whole model — the axioms, the
// implementation rules, the Laufregeln, the planned runs and the concrete ToDos — answers questions
// about any of it, and only when asked to create or change runs appends a run-plan JSON object the
// caller extracts as a reviewable proposal (same contract as the fill/fine-tune buttons).

// ChatMessage is one turn of the chat.
type ChatMessage struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

// ChatContext is everything the assistant knows about Mercury.
type ChatContext struct {
	Axiome     []RunAxiom   // the constitution
	Regeln     []RunAxiom   // implementation rules
	Laufregeln []RunAxiom   // global rules for automatic runs
	Runs       []PlannedRun // recurring automatic runs
	Todos      []string     // one-time ToDos, pre-rendered "name — target — task"
}

// MercuryChatPrompt frames the assistant over the whole of Mercury.
func MercuryChatPrompt(c ChatContext, messages []ChatMessage) string {
	var b strings.Builder
	b.WriteString("Du bist der KI-Assistent von Mercury, dem Zentrum der Holistic-Verfassung. Du kennst den ")
	b.WriteString("gesamten Stand unten: die Axiome, die Implementierungsregeln, die Laufregeln, die ")
	b.WriteString("automatischen Läufe und die konkreten ToDos. Beantworte Fragen zu allem davon, berate bei ")
	b.WriteString("Struktur und Formulierung, und hilf beim Planen. Antworte natürlich, knapp und auf Deutsch.\n\n")

	section := func(title string, xs []RunAxiom) {
		b.WriteString("## " + title + " (" + itoa(len(xs)) + ")\n")
		if len(xs) == 0 {
			b.WriteString("  (keine)\n")
		}
		for _, a := range xs {
			b.WriteString("  - " + titleOr(a.Titel, a.ID) + ": " + Snippet(a.Body, 140) + "\n")
		}
		b.WriteString("\n")
	}
	section("Axiome", c.Axiome)
	section("Implementierungsregeln", c.Regeln)
	section("Laufregeln", c.Laufregeln)

	b.WriteString("## Automatische Läufe (" + itoa(len(c.Runs)) + ")\n")
	if len(c.Runs) == 0 {
		b.WriteString("  (keine)\n")
	}
	for _, r := range c.Runs {
		b.WriteString("  - " + r.Name + " [" + r.Schedule.Kind + " " + r.Schedule.TimeOfDay + "]: " +
			itoa(len(r.AxiomIDs)) + " Axiome\n")
	}
	b.WriteString("\n## Konkrete ToDos (" + itoa(len(c.Todos)) + ")\n")
	if len(c.Todos) == 0 {
		b.WriteString("  (keine)\n")
	}
	for _, t := range c.Todos {
		b.WriteString("  - " + t + "\n")
	}

	b.WriteString("\nNur wenn der User dich bittet, automatische Läufe anzulegen oder zu ändern, hänge ans ENDE ")
	b.WriteString("deiner Antwort GENAU EIN JSON-Objekt mit dem VOLLSTÄNDIGEN gewünschten Ziel-Set aller Läufe ")
	b.WriteString("an (sonst gar kein JSON). Verwende dabei die echten Axiom-ids:\n")
	b.WriteString("Axiom-ids: ")
	for i, a := range c.Axiome {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(a.ID + "=" + titleOr(a.Titel, a.ID))
	}
	b.WriteString("\n")
	writeRunPlanContract(&b)

	b.WriteString("\nKonversation:\n")
	for _, m := range messages {
		role := "User"
		if m.Role == "assistant" {
			role = "Assistent"
		}
		b.WriteString(role + ": " + strings.TrimSpace(m.Content) + "\n")
	}
	b.WriteString("Assistent: ")
	return b.String()
}

// itoa avoids pulling strconv in for two call sites.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
