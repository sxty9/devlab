// Run planning core (B 1.4) — the logic below the HTTP layer, shared by run CRUD, the
// reconcile-after-write recomposition, the auto-assigner and the AI proposal flow. There is
// exactly ONE composition path (REQ-003): every write path recomposes the prompt snapshot
// through ComposeInto (which itself delegates to mercury's composers).
package runs

import (
	"context"
	"strings"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
)

// PlanAI is the injected aigentic call (it runs on the session of whoever triggered planning —
// no scheduler-style provisioning).
type PlanAI func(ctx context.Context, prompt string) (string, error)

// Catalog is the composition input: every axiom by stable id plus all global run rules.
// (ARCHITEKTUR §3.6 sketched `mercury.Tree` here; composition factually needs the axiom BODIES,
// which the tree of paths does not carry — recorded as a Welle-0 contract note.)
type Catalog struct {
	ByID       map[string]mercury.RunAxiom
	Laufregeln []mercury.RunAxiom
}

// AxiomsFor resolves ids against the catalog, keeping order and dropping unknowns.
func (c Catalog) AxiomsFor(ids []string) []mercury.RunAxiom {
	out := make([]mercury.RunAxiom, 0, len(ids))
	for _, id := range ids {
		if a, ok := c.ByID[id]; ok {
			out = append(out, a)
		}
	}
	return out
}

// TodoAttachmentDescriptors maps stored attachment metadata to the lean descriptors the prompt
// references (filename + type, never bytes) — one definition for composition and the media
// handlers.
func TodoAttachmentDescriptors(atts []AttachmentRef) []mercury.TodoAttachment {
	out := make([]mercury.TodoAttachment, 0, len(atts))
	for _, a := range atts {
		out = append(out, mercury.TodoAttachment{Filename: a.Filename, MIME: a.MIME})
	}
	return out
}

// ComposeInto (re)composes a run's prompt snapshot — the ONE composition path (REQ-003). An
// auto run folds in its axioms + all run rules; a todo's snapshot is the target-agnostic task
// base (per-target notes are composed at execution time). The input hash covers the title too
// (REQ-003) via the composed prompt itself.
func ComposeInto(r *Run, cat Catalog) {
	if r.Kind == model.KindTodo {
		r.PromptSnapshot = mercury.ComposeTodoPrompt(r.Title, r.Task, "", TodoAttachmentDescriptors(r.Attachments))
		r.PromptInputHash = ""
		return
	}
	axs := cat.AxiomsFor(r.AxiomIDs)
	r.PromptSnapshot = mercury.ComposeRunPrompt(r.Title, axs, cat.Laufregeln)
	r.PromptInputHash = mercury.RunInputsHash(axs, cat.Laufregeln)
}

// UpsertPlannedRun folds one planned auto run into out: an existing auto run of the same title
// (case-insensitive) is extended (axioms deduped, snapshot recomposed); otherwise np is
// appended. Only auto runs are matched — a todo sharing a title is never turned into an axiom
// carrier. Shared by the interactive fill-apply and the background auto-assigner (one planning
// path, REQ-004). Returns the list, a copy of the affected run, and whether a new run was
// created.
func UpsertPlannedRun(out []Run, np Run, cat Catalog, now time.Time, by model.Actor) ([]Run, Run, bool) {
	for i := range out {
		if out[i].Kind == model.KindTodo || !strings.EqualFold(out[i].Title, np.Title) {
			continue
		}
		out[i].AxiomIDs = DedupStrings(append(out[i].AxiomIDs, np.AxiomIDs...))
		out[i].Authorship.Updated = by
		out[i].Authorship.UpdatedAt = now
		ComposeInto(&out[i], cat)
		return out, out[i], false
	}
	return append(out, np), np, true
}

// DedupStrings collapses duplicates, preserving first-seen order.
func DedupStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
