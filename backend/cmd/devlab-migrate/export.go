// The raw legacy export is the migration's ONLY input: one JSON document holding every run
// record of the pre-rebuild store. It is read TOLERANTLY — unknown fields are ignored, absent
// fields default — and it is never written back. The shapes here mirror the pre-rebuild record
// (including its two historical target forms) and exist for exactly this one-time read.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"devlab/backend/internal/model"
	"devlab/backend/internal/runs"
)

// exportFile is the document: {"runs":[…]}.
type exportFile struct {
	Runs []exportRun `json:"runs"`
}

// exportRun is one pre-rebuild run record. A record with type "todo" is a concrete task; every
// other value (the export writes "") is a recurring, axiom-driven run — the same tolerance the
// frozen legacy result mapping applies.
type exportRun struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Enabled  bool            `json:"enabled"`
	Schedule *exportSchedule `json:"schedule"`
	// AxiomIDs is read but NOT carried: the automatic runs come in uncovered, and the one
	// planning path assigns axioms against the CURRENT constitution (B-10).
	AxiomIDs []string `json:"axiomIds"`
	Task     string   `json:"task"`
	Prompt   string   `json:"prompt"`
	// Targets is the current form; Repo is the older singular one. Both are read.
	Targets     []exportTarget     `json:"targets"`
	Repo        string             `json:"repo"`
	Attachments []exportAttachment `json:"attachments"`
	Done        bool               `json:"done"`
	DueAt       *time.Time         `json:"dueAt"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
	// LastFiredAt is read but NOT carried: the rebuild's run record holds no execution facts —
	// fire times derive from the execution documents, so an imported run fires on its schedule
	// instead of catching up at once.
	LastFiredAt *time.Time           `json:"lastFiredAt"`
	LastResult  *exportResultSummary `json:"lastResult"`
}

// exportSchedule is the recurrence of an automatic run.
type exportSchedule struct {
	Kind      string         `json:"kind"`
	TimeOfDay string         `json:"timeOfDay"`
	Weekdays  []time.Weekday `json:"weekdays"`
}

// exportTarget is one destination. The older export names a repository to be created "newRepo";
// the rebuild expresses the same thing as one target with Create set (REQ-006.2).
type exportTarget struct {
	Repo    string `json:"repo"`
	NewRepo string `json:"newRepo"`
}

// exportAttachment is one attachment's metadata. The BYTES live in the attachment pool, keyed by
// (run id, attachment id) — carrying the metadata under the original run id keeps them linked.
type exportAttachment struct {
	ID         string    `json:"id"`
	Filename   string    `json:"filename"`
	MIME       string    `json:"mime"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	UploadedAt time.Time `json:"uploadedAt"`
	UploadedBy string    `json:"uploadedBy"`
}

// exportResultSummary is the summary of the record's last execution. It is a SUMMARY: the export
// carries one timestamp, the outcome flag and the token/cost totals — no per-stage detail. The
// import therefore claims no stage states (fabricating them is exactly the false green K-4).
type exportResultSummary struct {
	ResultID     string    `json:"resultId"`
	At           time.Time `json:"at"`
	OK           bool      `json:"ok"`
	RepoCount    int       `json:"repoCount"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	CostUSD      float64   `json:"costUsd"`
}

// readExport reads and parses the export document.
func readExport(path string) (*exportFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f exportFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s is not a readable run export: %w", path, err)
	}
	return &f, nil
}

// kind maps the record onto the rebuild's two run kinds.
func (e exportRun) kind() model.RunKind {
	if e.Type == string(model.KindTodo) {
		return model.KindTodo
	}
	return model.KindAuto
}

// repoNames lists every repository the record aims at, in order and deduplicated: each target
// (existing or to-be-created) plus the older singular field.
func (e exportRun) repoNames() []string {
	out := make([]string, 0, len(e.Targets)+1)
	for _, t := range e.Targets {
		if t.Repo != "" {
			out = append(out, t.Repo)
		}
		if t.NewRepo != "" {
			out = append(out, t.NewRepo)
		}
	}
	if e.Repo != "" {
		out = append(out, e.Repo)
	}
	return runs.DedupStrings(out)
}

// targetsOwnRepo reports whether the record aims at the service's OWN repository. Those records
// were requirements ON the rebuild: their deduplicated substance IS the acceptance matrix, so
// they are deliberately not fed back as tasks (S15 step 2).
func (e exportRun) targetsOwnRepo(own string) bool {
	for _, r := range e.repoNames() {
		if strings.EqualFold(r, own) {
			return true
		}
	}
	return false
}

// targets maps the record's destinations onto the rebuild's target list.
func (e exportRun) targets() []runs.Target {
	out := make([]runs.Target, 0, len(e.Targets)+1)
	seen := map[string]bool{}
	add := func(repo string, create bool) {
		if repo == "" || seen[repo] {
			return
		}
		seen[repo] = true
		out = append(out, runs.Target{Repo: repo, Create: create})
	}
	for _, t := range e.Targets {
		add(t.Repo, false)
		add(t.NewRepo, true)
	}
	add(e.Repo, false)
	return out
}

// attachments maps the record's attachment metadata. The run id is preserved by the import, so
// the pool bytes stay reachable under the same key.
func (e exportRun) attachments() []runs.AttachmentRef {
	out := make([]runs.AttachmentRef, 0, len(e.Attachments))
	for _, a := range e.Attachments {
		out = append(out, runs.AttachmentRef{
			ID: a.ID, Filename: a.Filename, MIME: a.MIME, Size: a.Size,
			SHA256: a.SHA256, UploadedAt: a.UploadedAt, UploadedBy: a.UploadedBy,
		})
	}
	return out
}

// schedule maps the recurrence and REFUSES an unusable one: a run whose schedule cannot be
// expressed would sit in the surface looking active while never firing — the kind of silent
// trap the rebuild exists to remove. The caller turns this into a named refusal.
func (e exportRun) schedule() (*runs.ScheduleSpec, error) {
	if e.Schedule == nil {
		return nil, fmt.Errorf("record %s (%s) carries no schedule", e.ID, e.Name)
	}
	spec := runs.ScheduleSpec{
		Kind:      runs.ScheduleKind(e.Schedule.Kind),
		TimeOfDay: e.Schedule.TimeOfDay,
		Weekdays:  e.Schedule.Weekdays,
	}
	if err := spec.Valid(); err != nil {
		return nil, fmt.Errorf("record %s (%s) has an unusable schedule: %w", e.ID, e.Name, err)
	}
	return &spec, nil
}
