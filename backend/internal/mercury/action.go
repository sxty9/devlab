package mercury

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Beyond answering, the Mercury-wide assistant can propose ONE mutating action for the user to review
// and apply — the same contract as the run-plan proposal, generalized to every operation the user can
// perform in the Mercury UI (create a ToDo or a Lauf, add/edit/delete an axiom or rule, run something
// now, replan the runs). The caller applies an accepted action through the SAME access point the UI
// uses (POST /runs, POST /axiom, …), so the chat opens no parallel data path. As with the classifier
// and the run-plan, the model returns free text with one embedded JSON object; the contract is enforced
// here and the object is stripped from the reply the user reads.

// The action kinds. Each mirrors one mutating operation of the Mercury surface, one to one.
const (
	ActionCreateTodo   = "create_todo"   // → POST /api/mercury/runs (type=todo)
	ActionCreateRun    = "create_run"    // → POST /api/mercury/runs (type=auto)
	ActionAddRecord    = "add_record"    // → POST /api/mercury/axiom (axiome|regeln|laeufe|meta)
	ActionEditRecord   = "edit_record"   // → PUT  /api/mercury/axiom
	ActionDeleteRecord = "delete_record" // → DELETE /api/mercury/axiom
	ActionDeleteRun    = "delete_run"    // → DELETE /api/mercury/runs/{id}
	ActionRunNow       = "run_now"       // → POST /api/mercury/runs/{id}/run
	ActionPlanRuns     = "plan_runs"     // → POST /api/mercury/runs/apply-proposal
)

// ErrInvalidAction wraps every contract violation; its message doubles as a model-retry correction,
// exactly like ErrInvalidPlacement for the classifier.
var ErrInvalidAction = fmt.Errorf("invalid action")

// ChatAction is a single reviewable operation. Only the fields relevant to Kind are populated — a flat
// shape (rather than a per-kind struct) so it rides one JSON contract the model can emit uniformly.
type ChatAction struct {
	Kind string `json:"kind"`

	// create_todo / create_run / delete_* addressing
	Name    string         `json:"name,omitempty"`
	Task    string         `json:"task,omitempty"`    // create_todo
	Targets []ActionTarget `json:"targets,omitempty"` // create_todo
	DueAt   string         `json:"dueAt,omitempty"`   // create_todo (optional ISO date-time)

	AxiomIDs []string     `json:"axiomIds,omitempty"` // create_run
	Schedule PlanSchedule `json:"schedule,omitempty"` // create_run

	// add_record / edit_record / delete_record
	Section string `json:"section,omitempty"` // axiome | regeln | laeufe | meta
	Titel   string `json:"titel,omitempty"`
	Body    string `json:"body,omitempty"`
	Path    string `json:"path,omitempty"` // edit_record / delete_record

	// delete_run / run_now
	RunID string `json:"runId,omitempty"`

	// plan_runs — the former run-plan proposal, folded in as one action kind
	Mode string       `json:"mode,omitempty"` // fill | replace (default replace)
	Runs []PlannedRun `json:"runs,omitempty"`
}

// ActionTarget is one ToDo destination: exactly an existing repo id OR a new repo name (mirrors
// runs.Target). The api layer re-validates on apply; here we only enforce the exactly-one-of shape.
type ActionTarget struct {
	Repo    string `json:"repo,omitempty"`
	NewRepo string `json:"newRepo,omitempty"`
}

// ActionContext is what a proposed action is checked against: the ids, record paths and repos the model
// was actually shown, so it can only address things that exist. An empty set means "not provided" and is
// not enforced (the authoritative gate is the endpoint the action is applied through).
type ActionContext struct {
	AxiomIDs    map[string]bool // ids of axiome/ records (runs bundle only axioms)
	RunIDs      map[string]bool // ids of all runs and ToDos
	RecordPaths map[string]bool // paths of all records (axiome+regeln+laeufe+meta)
	RepoIDs     map[string]bool // ids of existing repos, for ToDo targets
}

// ValidateChatAction checks and normalizes an action against the context. The error doubles as a retry
// correction.
func ValidateChatAction(a *ChatAction, ac ActionContext) error {
	a.Kind = strings.TrimSpace(a.Kind)
	switch a.Kind {
	case ActionCreateTodo:
		a.Name, a.Task = strings.TrimSpace(a.Name), strings.TrimSpace(a.Task)
		if a.Name == "" || a.Task == "" {
			return fmt.Errorf("%w: create_todo needs name and task", ErrInvalidAction)
		}
		if len(a.Targets) == 0 {
			return fmt.Errorf("%w: create_todo needs at least one target (targets)", ErrInvalidAction)
		}
		for i := range a.Targets {
			t := &a.Targets[i]
			t.Repo, t.NewRepo = strings.TrimSpace(t.Repo), strings.TrimSpace(t.NewRepo)
			if (t.Repo == "") == (t.NewRepo == "") {
				return fmt.Errorf("%w: every target is exactly one existing repository (repo) OR a new one (newRepo)", ErrInvalidAction)
			}
			if t.Repo != "" && len(ac.RepoIDs) > 0 && !ac.RepoIDs[t.Repo] {
				return fmt.Errorf("%w: unknown repository %q — use a repo id from the list or newRepo", ErrInvalidAction, t.Repo)
			}
		}
	case ActionCreateRun:
		a.Name = strings.TrimSpace(a.Name)
		if a.Name == "" {
			return fmt.Errorf("%w: create_run needs name", ErrInvalidAction)
		}
		if len(a.AxiomIDs) == 0 {
			return fmt.Errorf("%w: create_run needs at least one axiom (axiomIds)", ErrInvalidAction)
		}
		for _, id := range a.AxiomIDs {
			if len(ac.AxiomIDs) > 0 && !ac.AxiomIDs[id] {
				return fmt.Errorf("%w: unknown axiom %q", ErrInvalidAction, id)
			}
		}
		if err := validatePlanSchedule(a.Name, a.Schedule); err != nil {
			return err
		}
	case ActionAddRecord:
		if !validSection(a.Section) {
			return fmt.Errorf("%w: section must be axiome, regeln, laeufe or meta", ErrInvalidAction)
		}
		a.Titel, a.Body = strings.TrimSpace(a.Titel), strings.TrimSpace(a.Body)
		if a.Titel == "" || a.Body == "" {
			return fmt.Errorf("%w: add_record needs titel and body", ErrInvalidAction)
		}
	case ActionEditRecord:
		a.Path, a.Titel, a.Body = strings.TrimSpace(a.Path), strings.TrimSpace(a.Titel), strings.TrimSpace(a.Body)
		if err := knownRecordPath(a.Path, ac); err != nil {
			return err
		}
		if a.Titel == "" || a.Body == "" {
			return fmt.Errorf("%w: edit_record needs titel and body", ErrInvalidAction)
		}
	case ActionDeleteRecord:
		a.Path = strings.TrimSpace(a.Path)
		if err := knownRecordPath(a.Path, ac); err != nil {
			return err
		}
	case ActionDeleteRun, ActionRunNow:
		a.RunID = strings.TrimSpace(a.RunID)
		if a.RunID == "" {
			return fmt.Errorf("%w: %s needs runId", ErrInvalidAction, a.Kind)
		}
		if len(ac.RunIDs) > 0 && !ac.RunIDs[a.RunID] {
			return fmt.Errorf("%w: no run or todo with id %q", ErrInvalidAction, a.RunID)
		}
	case ActionPlanRuns:
		if a.Mode == "" {
			a.Mode = "replace"
		}
		if a.Mode != "fill" && a.Mode != "replace" {
			return fmt.Errorf("%w: plan_runs mode must be fill or replace", ErrInvalidAction)
		}
		plan := RunPlan{Runs: a.Runs}
		if err := ValidateRunPlan(&plan, keysOfBool(ac.AxiomIDs)); err != nil {
			return err
		}
		a.Runs = plan.Runs
	case "":
		return fmt.Errorf("%w: missing kind", ErrInvalidAction)
	default:
		return fmt.Errorf("%w: unknown action %q", ErrInvalidAction, a.Kind)
	}
	return nil
}

// ExtractChatAction pulls a valid action JSON object out of free-form chat text (if present) and returns
// the text with that JSON removed, so the reply reads cleanly. found is false when there is no embedded
// action or it does not validate — random JSON in the conversation is never mistaken for a command. A
// bare run-plan object ({"runs":[…]} with no kind) is accepted as a replace plan for backward
// compatibility with the earlier run-plan contract.
func ExtractChatAction(text string, ac ActionContext) (action ChatAction, cleaned string, found bool) {
	raw, ok := firstJSONObject(text)
	if !ok {
		return ChatAction{}, text, false
	}
	if json.Unmarshal([]byte(raw), &action) != nil {
		return ChatAction{}, text, false
	}
	if strings.TrimSpace(action.Kind) == "" {
		if len(action.Runs) == 0 {
			return ChatAction{}, text, false // some other JSON in the reply — not an action
		}
		action.Kind = ActionPlanRuns // legacy bare run-plan
	}
	if ValidateChatAction(&action, ac) != nil {
		return ChatAction{}, text, false
	}
	return action, strings.TrimSpace(strings.Replace(text, raw, "", 1)), true
}

// validatePlanSchedule checks one proposed schedule (shared by create_run and the run-plan validator, so
// the daily/weekly rules live in one place). runName only labels the error.
func validatePlanSchedule(runName string, s PlanSchedule) error {
	if !runTimeOfDayRe.MatchString(s.TimeOfDay) {
		return fmt.Errorf("%w: run %q has an invalid timeOfDay %q (HH:MM)", ErrInvalidPlacement, runName, s.TimeOfDay)
	}
	switch s.Kind {
	case "daily":
		if len(s.Weekdays) != 0 {
			return fmt.Errorf("%w: run %q (daily) must carry no weekdays", ErrInvalidPlacement, runName)
		}
	case "weekly":
		if len(s.Weekdays) == 0 {
			return fmt.Errorf("%w: run %q (weekly) needs weekdays", ErrInvalidPlacement, runName)
		}
		for _, d := range s.Weekdays {
			if d < 0 || d > 6 {
				return fmt.Errorf("%w: run %q has an invalid weekday %d (0=Sunday..6=Saturday)", ErrInvalidPlacement, runName, d)
			}
		}
	default:
		return fmt.Errorf("%w: run %q has schedule.kind %q (expected daily|weekly)", ErrInvalidPlacement, runName, s.Kind)
	}
	return nil
}

func validSection(s string) bool {
	switch s {
	case NsAxiome, NsRegeln, NsLaeufe, NsMeta:
		return true
	}
	return false
}

func knownRecordPath(path string, ac ActionContext) error {
	if !ValidRecordPath(path) {
		return fmt.Errorf("%w: %q is not a valid record path (e.g. axiome/category/name.md)", ErrInvalidAction, path)
	}
	if len(ac.RecordPaths) > 0 && !ac.RecordPaths[path] {
		return fmt.Errorf("%w: no record at %q", ErrInvalidAction, path)
	}
	return nil
}

func keysOfBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// writeChatActionContract appends the action contract to the assistant prompt: it tells the model to
// emit exactly one JSON object ONLY when the user asks to perform an action, and lists the kinds.
func writeChatActionContract(b *strings.Builder) {
	b.WriteString("\n## Aktionen\n")
	b.WriteString("Der User kann dich bitten, etwas zu TUN (nicht nur zu beantworten). Willst du eine Aktion ")
	b.WriteString("ausführen, hänge ans ENDE deiner Antwort GENAU EIN JSON-Objekt an (sonst gar kein JSON). Es ")
	b.WriteString("wird dem User als überprüfbarer Vorschlag gezeigt, den er mit einem Klick übernimmt. Genau eine ")
	b.WriteString("Aktion pro Antwort. Verwende nur ids, Pfade und Repos aus den obigen Listen.\n")
	b.WriteString("Mögliche Aktionen (nur die relevanten Felder setzen):\n")
	b.WriteString(`  {"kind":"create_todo","name":"…","task":"…","targets":[{"repo":"<repo-id>"}|{"newRepo":"<name>"}],"dueAt":"<optional ISO>"}` + "\n")
	b.WriteString(`  {"kind":"create_run","name":"…","axiomIds":["<axiom-id>"],"schedule":{"kind":"daily|weekly","timeOfDay":"HH:MM","weekdays":[1,3]}}` + "\n")
	b.WriteString(`  {"kind":"add_record","section":"axiome|regeln|laeufe|meta","titel":"…","body":"…"}` + "\n")
	b.WriteString(`  {"kind":"edit_record","path":"<record-path>","titel":"…","body":"…"}` + "\n")
	b.WriteString(`  {"kind":"delete_record","path":"<record-path>"}` + "\n")
	b.WriteString(`  {"kind":"delete_run","runId":"<run-id>"}` + "\n")
	b.WriteString(`  {"kind":"run_now","runId":"<run-id>"}` + "\n")
	b.WriteString(`  {"kind":"plan_runs","mode":"replace|fill","runs":[{"name":"…","axiomIds":["<id>"],"schedule":{"kind":"daily","timeOfDay":"03:00"}}]}` + "\n")
	b.WriteString("Regeln: weekdays nur bei weekly (0=So..6=Sa); jedes ToDo-Ziel ist genau ein repo ODER ein ")
	b.WriteString("newRepo; ein Lauf/plan_runs bündelt ausschließlich Axiome (axiome-ids). Ohne Handlungswunsch: kein JSON.\n")
}
