// Run planning core (B 1.4) — the logic below the HTTP layer, shared by run CRUD, the
// reconcile-after-write recomposition, the auto-assigner and the AI proposal flow. There is
// exactly ONE composition path (REQ-003): every write path recomposes the prompt snapshot through
// ComposeInto, which delegates to the one composer (mercury.ComposePrompt) for BOTH kinds — an
// automatic run and a ToDo differ in what they add to the constitution, never in whether they
// carry it (REQ-002.1).
package runs

import (
	"context"
	"sort"
	"strings"
	"time"

	"devlab/backend/internal/mercury"
	"devlab/backend/internal/model"
)

// PlanAI is the injected aigentic call (it runs on the session of whoever triggered planning —
// no scheduler-style provisioning).
type PlanAI func(ctx context.Context, prompt string) (string, error)

// Catalog is the composition input — the constitution as read from the axiom store: every axiom by
// stable id, every Implementierungsregel, and every global Laufregel.
// (ARCHITEKTUR §3.6 sketched `mercury.Tree` here; composition factually needs the axiom BODIES,
// which the tree of paths does not carry — recorded as a Welle-0 contract note.)
type Catalog struct {
	ByID       map[string]mercury.RunAxiom
	Regeln     []mercury.RunAxiom
	Laufregeln []mercury.RunAxiom
}

// Scanned reports whether this catalog came out of a store scan at all. Every scanner initializes
// ByID — an instance whose constitution is genuinely empty still yields a non-nil map — so the ZERO
// Catalog, and only it, means "the store was NOT read". Composition must never present an unread
// store as an empty constitution (REQ-001.3 carried into the prompt): the composer names the gap.
func (c Catalog) Scanned() bool { return c.ByID != nil }

// Corpus is the constitution handed to the composer: every axiom in a deterministic order (the map
// has none) plus every Implementierungsregel in store order. Ordering is fixed here so a store scan
// that happens to iterate differently never produces a different prompt.
func (c Catalog) Corpus() mercury.Corpus {
	if !c.Scanned() {
		return mercury.Corpus{}
	}
	axs := make([]mercury.RunAxiom, 0, len(c.ByID))
	for _, a := range c.ByID {
		axs = append(axs, a)
	}
	sort.Slice(axs, func(i, j int) bool {
		ti, tj := mercury.RunAxiomTitle(axs[i]), mercury.RunAxiomTitle(axs[j])
		if ti != tj {
			return ti < tj
		}
		return axs[i].ID < axs[j].ID
	})
	return mercury.Corpus{Axiome: axs, Regeln: c.Regeln, Read: true}
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

// promptSpec assembles what the composer reads for one run. BOTH kinds carry the whole constitution
// (REQ-002.1): a ToDo is the way hand-started work runs, so its prompt needs the wording just as
// much as a sweep's does — the repositories' CLAUDE.md no longer holds it. The corpus is passed in
// so a sweep over many runs derives the store-wide part once.
func promptSpec(r Run, cat Catalog, corpus mercury.Corpus) mercury.PromptSpec {
	spec := mercury.PromptSpec{
		Todo:   r.Kind == model.KindTodo,
		Title:  r.Title,
		Corpus: corpus,
	}
	if spec.Todo {
		spec.Task = r.Task
		spec.Attachments = TodoAttachmentDescriptors(r.Attachments)
		return spec
	}
	spec.Subject = cat.AxiomsFor(r.AxiomIDs)
	spec.Laufregeln = cat.Laufregeln
	return spec
}

// ComposeInto (re)composes a run's prompt snapshot — the ONE composition path (REQ-003), for both
// kinds. The input hash covers every record, the title and the task, so a change anywhere in the
// constitution marks the snapshot as drifted (REQ-003).
//
// The snapshot is TARGET-AGNOSTIC: one run addresses many repositories, so anything that is true of
// only one of them — the per-repository examined stand (mercury.RepoScopeSection) above all — must
// be appended where the prompt is finalized per repository, and may never be folded in here.
func ComposeInto(r *Run, cat Catalog) {
	composeWith(r, cat, cat.Corpus())
}

func composeWith(r *Run, cat Catalog, corpus mercury.Corpus) {
	spec := promptSpec(*r, cat, corpus)
	r.PromptSnapshot = mercury.ComposePrompt(spec)
	r.PromptInputHash = mercury.PromptInputsHash(spec)
}

// RecomposeDrifted recomposes every run whose composed inputs no longer match the catalog — BOTH
// kinds, because both carry the constitution. Runs whose inputs are unchanged keep their snapshot
// byte for byte, so a write that touches nothing composed churns nothing.
//
// A catalog that was never scanned recomposes NOTHING: overwriting a good prompt with one that has
// to name a missing corpus would turn a failed read into a damaged run definition.
func RecomposeDrifted(cur []Run, cat Catalog) []Run {
	if !cat.Scanned() {
		return cur
	}
	corpus := cat.Corpus() // derived once for the whole sweep
	for i := range cur {
		if cur[i].PromptInputHash == mercury.PromptInputsHash(promptSpec(cur[i], cat, corpus)) {
			continue
		}
		composeWith(&cur[i], cat, corpus)
	}
	return cur
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
