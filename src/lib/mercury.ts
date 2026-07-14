// Where the Holistic axioms live. They were migrated out of the standalone `axioma` repo and into
// devlab, so Mercury reads them through the repo endpoints that already exist — no axiom store, no
// new backend. Should they ever move again, this file is the whole migration.

/** The repo holding the axioms. */
export const MERCURY_REPO = 'devlab';

/** The two guiding documents ("Leitdokumente"). */
export const MERCURY_DOCS = [
  { id: 'axioms', label: 'Axiome', path: 'axioms/CLAUDE.MD.md' },
  { id: 'nightly', label: 'Nachtlauf', path: 'axioms/Nightly Run.md' },
] as const;

export type MercuryDocId = (typeof MERCURY_DOCS)[number]['id'];

/** The running inbox for new decisions, consumed by the "aggregate" workflow. */
export const MERCURY_INBOX = 'axioms/Konsolidierungen';

/** The aggregate workflow itself — the rules for maintaining the axioms. */
export const MERCURY_WORKFLOW = 'axioms/CLAUDE.MD';
