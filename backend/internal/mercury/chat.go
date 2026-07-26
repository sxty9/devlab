package mercury

import "strings"

// The Mercury-wide assistant. It is NOT run-specific: it sees the whole model — the axioms, the
// implementation rules, the Laufregeln, the planned runs and the concrete ToDos — answers questions
// about any of it, and can propose any mutating action the user could perform in the UI (see
// action.go), appended as one reviewable JSON object the caller extracts and applies through the same
// access points the UI uses.

// ChatMessage is one turn of the chat.
type ChatMessage struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

// ChatRecord is one scheme record (axiom, rule, Laufregel or meta-axiom) the assistant knows, carrying
// the addressing an action needs: its section, its stable id (referenced by runs) and its path
// (referenced by edit/delete).
type ChatRecord struct {
	Section string // axiome | regeln | laeufe | meta
	Path    string
	ID      string
	Titel   string
	Body    string
}

// ChatRun is one run or ToDo the assistant knows, carrying its id (referenced by run_now/delete_run)
// and enough shape to describe it.
type ChatRun struct {
	ID       string
	Name     string
	Todo     bool // true = konkretes ToDo, false = automatischer Lauf
	Schedule PlanSchedule
	AxiomIDs []string
	Task     string
	Targets  []ActionTarget
}

// ChatRepo is one existing repo, offered as a possible ToDo target (create_todo).
type ChatRepo struct {
	ID   string
	Name string
}

// ChatContext is everything the assistant knows about Mercury.
type ChatContext struct {
	Records []ChatRecord // axioms, rules, Laufregeln and meta-axioms
	Runs    []ChatRun    // automatic runs and concrete ToDos
	Repos   []ChatRepo   // existing repos, for ToDo targets
}

// recordsOf returns the records of one section, preserving order.
func (c ChatContext) recordsOf(section string) []ChatRecord {
	var out []ChatRecord
	for _, r := range c.Records {
		if r.Section == section {
			out = append(out, r)
		}
	}
	return out
}

// axiomIDSet returns the ids of the axiome/ records — the only records a run may bundle.
func (c ChatContext) axiomIDSet() map[string]bool {
	out := map[string]bool{}
	for _, r := range c.Records {
		if r.Section == NsAxiome && r.ID != "" {
			out[r.ID] = true
		}
	}
	return out
}

// ActionContext derives the validation sets (known axiom ids, run ids, record paths, repo ids) from the
// context the model was shown, so a proposed action can only address things that exist.
func (c ChatContext) ActionContext() ActionContext {
	ac := ActionContext{
		AxiomIDs:    c.axiomIDSet(),
		RunIDs:      map[string]bool{},
		RecordPaths: map[string]bool{},
		RepoIDs:     map[string]bool{},
	}
	for _, r := range c.Runs {
		if r.ID != "" {
			ac.RunIDs[r.ID] = true
		}
	}
	for _, r := range c.Records {
		if r.Path != "" {
			ac.RecordPaths[r.Path] = true
		}
	}
	for _, r := range c.Repos {
		if r.ID != "" {
			ac.RepoIDs[r.ID] = true
		}
	}
	return ac
}

// MercuryChatPrompt frames the assistant over the whole of Mercury.
func MercuryChatPrompt(c ChatContext, messages []ChatMessage) string {
	var b strings.Builder
	b.WriteString("Du bist der KI-Assistent von Mercury, dem Zentrum der Holistic-Verfassung. Du kennst den ")
	b.WriteString("gesamten Stand unten: die Axiome, die Implementierungsregeln, die Laufregeln, die ")
	b.WriteString("Meta-Axiome, die automatischen Läufe und die konkreten ToDos. Beantworte Fragen zu allem davon, ")
	b.WriteString("berate bei Struktur und Formulierung, hilf beim Planen — und führe auf Wunsch Aktionen aus ")
	b.WriteString("(siehe unten). Antworte natürlich, knapp und auf Deutsch.\n\n")

	section := func(title, ns string) {
		xs := c.recordsOf(ns)
		b.WriteString("## " + title + " (" + itoa(len(xs)) + ")\n")
		if len(xs) == 0 {
			b.WriteString("  (keine)\n")
		}
		for _, a := range xs {
			b.WriteString("  - " + a.Path + " [id " + a.ID + "] " + titleOr(a.Titel, a.ID) + ": " + Snippet(a.Body, 140) + "\n")
		}
		b.WriteString("\n")
	}
	section("Axiome", NsAxiome)
	section("Implementierungsregeln", NsRegeln)
	section("Laufregeln", NsLaeufe)
	section("Meta-Axiome", NsMeta)

	var runs, todos []ChatRun
	for _, r := range c.Runs {
		if r.Todo {
			todos = append(todos, r)
		} else {
			runs = append(runs, r)
		}
	}

	b.WriteString("## Automatische Läufe (" + itoa(len(runs)) + ")\n")
	if len(runs) == 0 {
		b.WriteString("  (keine)\n")
	}
	for _, r := range runs {
		b.WriteString("  - [id " + r.ID + "] " + r.Name + " [" + r.Schedule.Kind + " " + r.Schedule.TimeOfDay + "]: " +
			itoa(len(r.AxiomIDs)) + " Axiome\n")
	}

	b.WriteString("\n## Konkrete ToDos (" + itoa(len(todos)) + ")\n")
	if len(todos) == 0 {
		b.WriteString("  (keine)\n")
	}
	for _, r := range todos {
		b.WriteString("  - [id " + r.ID + "] " + r.Name + " → " + targetsSummary(r.Targets) + ": " + Snippet(r.Task, 120) + "\n")
	}

	b.WriteString("\n## Verfügbare Repos (für ToDo-Ziele) (" + itoa(len(c.Repos)) + ")\n")
	if len(c.Repos) == 0 {
		b.WriteString("  (keine)\n")
	}
	for _, r := range c.Repos {
		b.WriteString("  - [id " + r.ID + "] " + r.Name + "\n")
	}

	writeChatActionContract(&b)

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

// targetsSummary renders a ToDo's targets as a short human string for the prompt listing.
func targetsSummary(ts []ActionTarget) string {
	if len(ts) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		if t.NewRepo != "" {
			parts = append(parts, "neu:"+t.NewRepo)
			continue
		}
		parts = append(parts, t.Repo)
	}
	return strings.Join(parts, ", ")
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
