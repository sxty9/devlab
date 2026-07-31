// deliveries.ts — PURE presentation logic of the delivery ledger (F12), kept apart from the
// rendering so every rule is unit-tested with node --test (deliveries.test.ts).
//
// A delivery is one addressable unit of work an execution shipped to a repository: a commit range
// on the growing dev branch plus its stacked pull request. The ledger is append-only — a rollback
// adds a REVERSING delivery instead of erasing anything — so this module only groups, orders and
// labels what the server states. The lifecycle stage comes from the server (`stage`); nothing here
// derives it (B-17/B-35).
import type { Delivery } from '@/types';
import type { BadgeTone } from '@/ui/tint';

/** The ledger lifecycle stages the server states, named once for every surface. Distinct from a
 *  run's chain stages: a delivery exists from its intent onward, so its stage tracks its pull
 *  request (open → merged, or closed without merge) plus the `reverted` terminal a counter-booking
 *  sets. */
export const DELIVERY_STAGE: Record<string, { label: string; tone: BadgeTone }> = {
  open: { label: 'PR open', tone: 'accent' },
  merged: { label: 'merged', tone: 'success' },
  closed: { label: 'closed', tone: 'warning' },
  reverted: { label: 'rolled back', tone: 'neutral' },
};

/** The one stage name that means "not settled yet". Named once here so every surface asks the same
 *  question of the same field instead of spelling the stage out again. */
const STAGE_OPEN = 'open';

/** Whether the server states this delivery as still OPEN — not merged, not closed, not counter-
 *  booked. This is the B-8 question ("is every delivery of the work settled?") and it is ANSWERED
 *  by the server: the ledger stamps decide the stage, the client only reads it (B-35). */
export function isOpenDelivery(d: Pick<Delivery, 'stage'>): boolean {
  return (d.stage ?? '').trim() === STAGE_OPEN;
}

/** The badge of one delivery — total: an unstamped or unfamiliar stage still yields a DEFINED
 *  chip, so no ledger row can render a blank (or a black screen). */
export function deliveryBadge(d: Pick<Delivery, 'stage'>): { label: string; tone: BadgeTone } {
  const stage = (d.stage ?? '').trim();
  return DELIVERY_STAGE[stage] ?? { label: stage || 'recorded', tone: 'neutral' };
}

/** The executions that still hold an OPEN delivery, read straight off the ledger — the same rule the
 *  server applies (runs.ExecutionCompleted, B-8): an execution is closed once every delivery that
 *  arose from it is merged, rolled back, or closed with a reason. Nothing is derived from a chain
 *  stage or its name here (B-35); the ledger's own stamps are the answer. A delivery the ledger
 *  cannot attribute to an execution holds nothing open. */
export function openDeliveryExecutionIds(list: readonly Delivery[]): ReadonlySet<string> {
  const out = new Set<string>();
  for (const d of list) {
    if (d.executionId && isOpenDelivery(d)) out.add(d.executionId);
  }
  return out;
}

/** The instant a delivery is ordered by: its creation. Unparseable/absent times yield 0 so the
 *  row sorts last instead of NaN-sorting the whole list away (the old black-screen trap). */
export function deliveryAt(d: Delivery): number {
  const t = d.createdAt ? Date.parse(d.createdAt) : NaN;
  return Number.isNaN(t) ? 0 : t;
}

/** Deliveries newest first — stable, so rows sharing an instant (or carrying no usable one) keep
 *  the server's order instead of shuffling per render. */
export function sortDeliveries(list: Delivery[]): Delivery[] {
  return [...list]
    .map((d, i) => ({ d, i, at: deliveryAt(d) }))
    .sort((a, b) => (b.at !== a.at ? b.at - a.at : a.i - b.i))
    .map((x) => x.d);
}

export interface RepoDeliveries {
  repo: string;
  /** This repo's deliveries, newest first — empty for a repository the ledger has never named. */
  deliveries: Delivery[];
  /** The most recent delivery the server still states as open; null when every one is settled. It
   *  says what the LEDGER holds open — never what the dev branch happens to carry, which only the
   *  repository itself can attest. */
  latestOpen: Delivery | null;
  openCount: number;
  /** The instant this repository was last delivered to; 0 when it never was (it then sorts last). */
  latestAt: number;
}

/** Group the flat ledger per repository, and include EVERY repository handed in even when the
 *  ledger has never named it: the dev reset is a per-repository decision, and a repository whose
 *  work was committed but never delivered is exactly the one that may need it — it must not be
 *  missing from the surface just because nothing was ever shipped from it. Repos are ordered by
 *  their most recent delivery (newest first), never-delivered ones last by name; deliveries newest
 *  first within each. */
export function groupDeliveriesByRepo(list: Delivery[], repos: readonly string[] = []): RepoDeliveries[] {
  const byRepo = new Map<string, Delivery[]>();
  // A repository without a name is no repository: it would render a section nobody could act on.
  for (const repo of repos) if (repo && !byRepo.has(repo)) byRepo.set(repo, []);
  for (const d of list) {
    const cur = byRepo.get(d.repo);
    if (cur) cur.push(d);
    else byRepo.set(d.repo, [d]);
  }
  const groups: RepoDeliveries[] = [];
  for (const [repo, entries] of byRepo) {
    const sorted = sortDeliveries(entries);
    const open = sorted.filter(isOpenDelivery);
    groups.push({
      repo,
      deliveries: sorted,
      latestOpen: open[0] ?? null,
      openCount: open.length,
      latestAt: sorted.length > 0 ? deliveryAt(sorted[0]) : 0,
    });
  }
  return groups.sort((a, b) => (b.latestAt !== a.latestAt ? b.latestAt - a.latestAt : a.repo.localeCompare(b.repo)));
}

/** Whether a delivery still has an effect left to undo: only an open or a merged one. A closed or
 *  already rolled-back delivery is settled, so its rollback control is not offered at all — no
 *  button into the void (REQ-040.3). A reversal itself is never rolled back again. */
export function canRollback(d: Delivery): boolean {
  if (d.reversalOf) return false;
  return isOpenDelivery(d) || (d.stage ?? '').trim() === 'merged';
}

/** Short commit sha for display (the ledger stores full shas). */
export const shortSha = (sha: string): string => (sha ? sha.slice(0, 7) : '');

/** The commit span this delivery contains — the exact work it shipped, `from..to`. */
export function commitRange(d: Delivery): string {
  const from = shortSha(d.fromCommit);
  const to = shortSha(d.toCommit);
  if (!from && !to) return '—';
  return `${from || '?'}..${to || '?'}`;
}

/** The three sentences a dangerous action owes its caller before it runs — what it DOES, what
 *  state it leaves behind, and the way back (REQ-040.6). A merged delivery is undone by a
 *  reversing pull request, a still-open one by closing its PR with the justification; in both
 *  cases the ledger only grows, history is never rewritten (REQ-025). */
export interface Consequence {
  effect: string;
  result: string;
  undo: string;
}

export function rollbackConfirmation(d: Delivery): Consequence {
  const merged = d.stage === 'merged';
  return {
    effect: merged
      ? `Counter-books ${commitRange(d)} in ${d.repo}: the reversing commit is opened as its own pull request.`
      : `Withdraws ${commitRange(d)} in ${d.repo}: the open pull request is closed with the justification and the dev state is counter-booked.`,
    result: merged
      ? 'The delivery reads as rolled back; the original commits stay in the history and the reversing pull request awaits its merge.'
      : 'The delivery reads as rolled back; the dev branch no longer carries this work.',
    undo: 'Nothing is rewritten — running the work again delivers it anew.',
  };
}

/** What the deliberate dev reset does to one repository (REQ-022.4). It is the ONE reset, never on
 *  the automated path, and it discards the dev state — so it says so, including the way back the
 *  server actually provides: the discarded tip is sheltered under a rescue ref before the branch
 *  moves (workbench.ResetToDefault), so "discarded" never means "gone". */
export function resetConfirmation(repo: string): Consequence {
  return {
    effect: `Sets the dev branch of ${repo} back to its default branch.`,
    result: 'Delivered work stays in the default branch; dev carries nothing beyond it any more.',
    undo: 'The discarded tip is secured under a rescue ref in the repository first, so nothing is lost silently.',
  };
}
