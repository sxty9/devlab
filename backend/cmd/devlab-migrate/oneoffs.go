// The one-off items M1–M8: the part of the migration that happens ON the instance, by hand, and
// therefore cannot be a code path. The import PREPARES them as a protocol in the notice pool so
// each one is visible with its next step and can be acknowledged once it is done (S15 step 5).
// There is no migration store: the protocol lives in the existing notice pool, and the printed
// section file deploy/migration/10-daten.md carries the same items with their outcome column.
package main

import (
	"time"

	"devlab/backend/internal/runs"
)

// noticeKindMigration marks a protocol record of the one-time migration.
//
// The kind vocabulary lives in the notice pool's own file, where every raising package picks one
// of the listed kinds. This ONE kind is the migration's — reported to the orchestrator so it can
// join that list; the pool itself is passive and stores whatever kind it is given.
const noticeKindMigration = "migration"

// noticeIDPrefix makes the protocol records addressable and, above all, idempotent: a second
// import finds its own records by these stable ids and adds nothing.
const noticeIDPrefix = "ntc_migration_"

// OneOff is one manual migration item: a stable key, the item's label, what has to happen and
// the concrete next step. The outcome is NOT stored here — it is the operator's entry in the
// section file, and acknowledging the notice is what closes the item.
type OneOff struct {
	Key      string
	Label    string
	Text     string
	NextStep string
}

// protocolItems returns everything the import records as protocol: the eight one-off items M1–M8
// of the old data set, plus the activation gate the import itself creates. One list, so the
// notice pool, the printed protocol and the section file cannot disagree about what is open.
func protocolItems() []OneOff {
	return append(oneOffs(), activationGate())
}

// activationGate is the protocol item the IMPORT produces rather than inherits: the recurring runs
// arrive switched off, and this states what has to happen before any of them may run. It is a
// checked cutover step, not a hint — the corresponding section of
// deploy/migration/10-daten.md carries the same wording with its verification.
func activationGate() OneOff {
	return OneOff{
		Key:   "activation",
		Label: "Activation gate — the imported recurring runs",
		Text: "The recurring runs were imported switched OFF although the old system had them on. " +
			"They carry no axioms yet, so they carry no composed prompt; an active run without one " +
			"would send an unattended agent into every target repository with no task named.",
		NextStep: "Assign the axioms (a constitution write triggers the assignment, B-10), confirm in " +
			"the coverage view that every run carries axioms and a prompt, then switch each run on " +
			"and check its next firing in the calendar.",
	}
}

// oneOffs returns M1–M8 in order. The wording mirrors deploy/migration/10-daten.md; both are
// this module's ownership, so the protocol in the surface and the protocol in the section file
// cannot drift apart.
func oneOffs() []OneOff {
	return []OneOff{{
		Key:   "m1",
		Label: "M1 — rescue branches",
		Text: "Four rescue branches hold work that was saved from the old workbench. " +
			"Where the rebuild carries the capability anyway, it is PROVEN, not delivered twice.",
		NextStep: "Record per branch: folded in or superseded (evidence: the slot, budget, " +
			"usage and workbench acceptance lines), then delete all four locally and remotely.",
	}, {
		Key:   "m2",
		Label: "M2 — rollout backlog",
		Text: "The retired constitution rollout left open pull requests, branches and " +
			"follow-up entries behind. The rebuild has no rollout path at all.",
		NextStep: "Close the open rollout pull requests, delete their branches, drop their " +
			"follow-up entries, and revert or counter-book the one-pull-request-per-repository commit.",
	}, {
		Key:   "m3",
		Label: "M3 — stale follow-up entry",
		Text: "A follow-up entry still points at a delivery branch that no longer exists — " +
			"the prune loop that repeated forever.",
		NextStep: "Remove the follow-up entry for the deleted delivery branch.",
	}, {
		Key:   "m4",
		Label: "M4 — tasks whose work already arrived",
		Text: "Tasks whose work is already in the default branch were shown as open. The " +
			"startup reconciliation closes them by itself and records a completed execution.",
		NextStep: "No manual step: verify the reconciliation's notices after the first start.",
	}, {
		Key:   "m5",
		Label: "M5 — deliver the two pending services",
		Text: "Two services were never delivered because the old path needed a per-repository " +
			"script. The generic path replaces it.",
		NextStep: "Create one task per service, run it through the chain, then verify: unit " +
			"active, visible in the dashboard, right declared.",
	}, {
		Key:   "m6",
		Label: "M6 — obsolete process branches",
		Text: "Old per-execution branches whose CONTENT arrived in the default branch (also " +
			"through a collective merge) are still present. Content decides, never the commit id.",
		NextStep: "Probe each branch by content (cherry/diff) and delete the ones already contained.",
	}, {
		Key:   "m7",
		Label: "M7 — port inventory",
		Text: "One service once started on a port already held by another. Ports are observed, " +
			"never assumed.",
		NextStep: "Open the ports view (or read the ports endpoint) and confirm every conflict " +
			"and deviation it reports.",
	}, {
		Key:   "m8",
		Label: "M8 — the one service that is not delivered",
		Text: "One service is deliberately not set up in production. That decision belongs in " +
			"its repository as a declared value, not in this service's code.",
		NextStep: "Declare the exclusion in that repository's service declaration — by the " +
			"owner's own commit or through a fed-in task; the rebuild does not change foreign repositories.",
	}}
}

// notice renders one item as a feed record: the finding as text (the item's label leads it, so
// the feed names which one it is) and the resolving step as the next step. The occurrence time is
// pinned to the import — the pool lets a caller pin it for exactly this case.
func (o OneOff) notice(at time.Time) runs.Notice {
	return runs.Notice{
		ID:       noticeIDPrefix + o.Key,
		At:       at,
		Kind:     noticeKindMigration,
		Text:     o.Label + ": " + o.Text,
		NextStep: o.NextStep,
	}
}
