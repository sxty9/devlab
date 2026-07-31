package mercury

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The AI-fill / AI-fine-tune buttons ask aigentic (claude-cli) to propose a set of runs. As with
// axiom classification (classify.go), the model returns free text; the contract is enforced here —
// one JSON object, validated, retried with a correction line. The proposal is REVIEWABLE and is never
// applied until the user accepts it.

// RunPlan is the model's proposed set of runs.
type RunPlan struct {
	Runs []PlannedRun `json:"runs"`
}

// PlannedRun is one proposed run. Schedule uses plain ints (0=Sun) so this package stays independent
// of the runs package; the api layer converts to runs.Schedule on apply.
type PlannedRun struct {
	Name      string       `json:"name"`
	AxiomIDs  []string     `json:"axiomIds"`
	Schedule  PlanSchedule `json:"schedule"`
	Rationale string       `json:"rationale"`
}

// PlanSchedule is the wire shape of a proposed schedule.
type PlanSchedule struct {
	Kind      string `json:"kind"`      // daily | weekly
	TimeOfDay string `json:"timeOfDay"` // HH:MM
	Weekdays  []int  `json:"weekdays,omitempty"`
}

var runTimeOfDayRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// RunLacksRequiredAxioms is the ONE axiom rule of a recurring run, stated once: a run that will be
// ACTIVE must carry at least one axiom — it would otherwise execute the constitution against
// nothing — while an INACTIVE one is a definition that never fires and is therefore storable
// without any. Every place that accepts a recurring run asks THIS question: the endpoint that
// stores one, the plan the model proposes and the chat action that creates one (the latter two
// arrive active). The rule exists once, so the sites cannot drift into three slightly different
// rules; only the wording of the refusal belongs to each site, because one addresses a person and
// the others correct a model.
func RunLacksRequiredAxioms(active bool, axiomIDs []string) bool {
	return active && len(axiomIDs) == 0
}

// ParseRunPlan extracts the model's JSON for a run plan and validates it. knownAxiomIDs is the set the
// plan may reference. The returned error message doubles as a retry correction (as in ParsePlacement).
func ParseRunPlan(output string, knownAxiomIDs []string) (RunPlan, error) {
	raw, ok := firstJSONObject(output)
	if !ok {
		return RunPlan{}, ErrNoJSON
	}
	var p RunPlan
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return RunPlan{}, fmt.Errorf("%w: kein gültiges JSON", ErrInvalidPlacement)
	}
	if err := ValidateRunPlan(&p, knownAxiomIDs); err != nil {
		return p, err
	}
	return p, nil
}

// ValidateRunPlan checks a plan (from the model or user-edited) against the known axiom set,
// normalizing run names in place. The error message doubles as a retry correction.
func ValidateRunPlan(p *RunPlan, knownAxiomIDs []string) error {
	if len(p.Runs) == 0 {
		return fmt.Errorf("%w: leerer Plan (runs ist leer)", ErrInvalidPlacement)
	}
	known := make(map[string]bool, len(knownAxiomIDs))
	for _, id := range knownAxiomIDs {
		known[id] = true
	}
	names := map[string]bool{}
	for i := range p.Runs {
		r := &p.Runs[i]
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" {
			return fmt.Errorf("%w: a run has no name", ErrInvalidPlacement)
		}
		low := strings.ToLower(r.Name)
		if names[low] {
			return fmt.Errorf("%w: duplicate run name %q", ErrInvalidPlacement, r.Name)
		}
		names[low] = true
		// A proposed run is applied as an ACTIVE run, so the one rule applies in that state.
		if RunLacksRequiredAxioms(true, r.AxiomIDs) {
			return fmt.Errorf("%w: run %q has no axioms", ErrInvalidPlacement, r.Name)
		}
		for _, id := range r.AxiomIDs {
			if !known[id] {
				return fmt.Errorf("%w: run %q references the unknown axiom %q", ErrInvalidPlacement, r.Name, id)
			}
		}
		if err := validatePlanSchedule(r.Name, r.Schedule); err != nil {
			return err
		}
	}
	return nil
}

// RunPlanFillPrompt asks the model to plan the given UNCOVERED axioms into runs with recurring
// schedules. existingRuns names the runs already present (so it can extend rather than duplicate).
func RunPlanFillPrompt(uncovered []RunAxiom, existingRuns []string, correction string) string {
	var b strings.Builder
	b.WriteString("Du bist der Planer der automatischen Holistic-Läufe. Ein Lauf bündelt Axiome, die ")
	b.WriteString("regelmäßig autonom durchgesetzt werden. Plane die folgenden, noch KEINEM Lauf ")
	b.WriteString("zugeordneten Axiome thematisch sinnvoll in Läufe und weise jedem Lauf einen ")
	b.WriteString("wiederkehrenden Zeitplan zu.\n\n")
	if len(existingRuns) > 0 {
		b.WriteString("Bereits bestehende Läufe: Ordne ein Axiom BEVORZUGT einem thematisch passenden ")
		b.WriteString("bestehenden Lauf zu — nenne dann exakt dessen Namen, damit er erweitert wird. Lege nur ")
		b.WriteString("dann einen neuen Lauf an, wenn kein bestehender thematisch passt.\n")
		for _, n := range existingRuns {
			b.WriteString("  " + n + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Noch nicht abgedeckte Axiome:\n")
	for _, a := range uncovered {
		b.WriteString("- id=" + a.ID + " | " + titleOr(a.Titel, a.ID) + ": " + bodySnippet(a.Body) + "\n")
	}
	b.WriteString("\n")
	writeRunPlanContract(&b)
	if correction != "" {
		b.WriteString("\nKORREKTUR: " + correction + " Antworte erneut mit einem korrigierten JSON-Objekt.\n")
	}
	return b.String()
}

// RunPlanFinetunePrompt asks the model to propose a cleaner regrouping of ALL current runs as a
// complete desired target set. catalog gives the meaning (Titel) of each referenced axiom id.
func RunPlanFinetunePrompt(catalog []RunAxiom, current []PlannedRun, correction string) string {
	var b strings.Builder
	b.WriteString("Du bist der Planer der automatischen Holistic-Läufe. Überarbeite die bestehende ")
	b.WriteString("Lauf-Struktur: gruppiere die Axiome nach sinnvollen Kategorien, führe zusammen was ")
	b.WriteString("zusammengehört, trenne was nicht zusammenpasst und weise passende wiederkehrende ")
	b.WriteString("Zeitpläne zu. Antworte mit dem VOLLSTÄNDIGEN gewünschten Ziel-Set aller Läufe.\n\n")
	b.WriteString("Axiom-Katalog (id → Titel):\n")
	for _, a := range catalog {
		b.WriteString("  " + a.ID + " → " + titleOr(a.Titel, a.ID) + "\n")
	}
	b.WriteString("\nAktuelle Läufe:\n")
	for _, r := range current {
		b.WriteString("- " + r.Name + " [" + r.Schedule.Kind + " " + r.Schedule.TimeOfDay + "]: " + strings.Join(r.AxiomIDs, ", ") + "\n")
	}
	b.WriteString("\n")
	writeRunPlanContract(&b)
	if correction != "" {
		b.WriteString("\nKORREKTUR: " + correction + " Antworte erneut mit einem korrigierten JSON-Objekt.\n")
	}
	return b.String()
}

func writeRunPlanContract(b *strings.Builder) {
	b.WriteString("Antworte mit GENAU einem JSON-Objekt, ohne Codefence, ohne weiteren Text:\n")
	b.WriteString(`{"runs":[{"name":"<knapper Lauf-Name>","axiomIds":["<id>"],`)
	b.WriteString(`"schedule":{"kind":"daily|weekly","timeOfDay":"HH:MM","weekdays":[1,3]},"rationale":"<1 Satz>"}]}` + "\n")
	b.WriteString("Regeln: jede axiomId MUSS aus der obigen Liste stammen; kind ist daily oder weekly; ")
	b.WriteString("weekdays nur bei weekly (0=So..6=Sa); Lauf-Namen eindeutig.\n")
}

func bodySnippet(s string) string { return Snippet(s, 200) }

// Snippet flattens text to one line and truncates it to maxRunes (rune-safe) — used wherever record
// bodies are folded into a prompt.
func Snippet(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}
