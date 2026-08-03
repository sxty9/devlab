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
  // A delivery whose work was committed but not shipped: the tip a new order does not branch past
  // until it is resolved (re-run) or rolled back. Its own stage, distinct from a settled one.
  failed: { label: 'delivery failed', tone: 'danger' },
};

/** The production-lifecycle badges the server states, for the PROD view of the deliveries surface
 *  (WHAT-1). Distinct from DELIVERY_STAGE above: those track a delivery's dev/PR life, these track
 *  what runs on the SECOND machine — production — which is reached only after a merge. */
export const PROD_STAGE: Record<string, { label: string; tone: BadgeTone }> = {
  pending: { label: 'awaiting production', tone: 'accent' },
  failed: { label: 'production failed — retrying', tone: 'danger' },
  live: { label: 'live in production', tone: 'success' },
  'not-applicable': { label: 'no service', tone: 'neutral' },
};

/** The production badge of one delivery — total, so an unstamped stage still yields a defined chip. */
export function prodBadge(d: Pick<Delivery, 'prodStage'>): { label: string; tone: BadgeTone } {
  const s = (d.prodStage ?? '').trim();
  return PROD_STAGE[s] ?? { label: s || 'not merged yet', tone: 'neutral' };
}

/** Whether this delivery has reached the production step at all — i.e. it MERGED. The PROD view
 *  shows only these (production happens after the merge), so an open or failed dev delivery that
 *  never merged does not appear there. */
export function hasProdStage(d: Pick<Delivery, 'prodStage'>): boolean {
  return (d.prodStage ?? '').trim() !== '';
}

/** The one stage name that means "committed but not shipped" — the failed tip. Named once so every
 *  surface asks the same question of the same field. */
const STAGE_FAILED = 'failed';

/** Whether the server states this delivery as a FAILED tip — its work is on the branch but did not
 *  ship. Read straight off the server-stamped stage; nothing is derived here (B-35). */
export function isFailedDelivery(d: Pick<Delivery, 'stage'>): boolean {
  return (d.stage ?? '').trim() === STAGE_FAILED;
}

/** Whether the delivery is UNSETTLED — neither merged nor closed nor reverted: a healthy open PR OR
 *  a failed tip. This is the B-8 question ("does this execution still hold work?"): a failed
 *  delivery keeps its execution — and thus its ToDo — open, exactly as an open PR does, until it is
 *  resolved (re-run) or rolled back. */
export function isUnsettledDelivery(d: Pick<Delivery, 'stage'>): boolean {
  return isOpenDelivery(d) || isFailedDelivery(d);
}

/** Whether the server states this delivery as still owing its PRODUCTION step (WHAT-1): merged, but
 *  not yet running in production ('pending') or with its last production send failed and retrying
 *  ('failed'). Read straight off the server-stamped `prodStage`; nothing is derived here (B-35). */
export function isProdPending(d: Pick<Delivery, 'prodStage'>): boolean {
  const s = (d.prodStage ?? '').trim();
  return s === 'pending' || s === 'failed';
}

/** Whether this delivery has been proven RUNNING in production — the last step of the chain done. */
export function isProdLive(d: Pick<Delivery, 'prodStage'>): boolean {
  return (d.prodStage ?? '').trim() === 'live';
}

/** Whether a delivery still holds its execution OPEN (not history-ready) — the client mirror of the
 *  server's completion rule (runs.Delivery.Settled, B-8 + WHAT-1). It is broader than the dev/stack
 *  question `isUnsettledDelivery`: a MERGED delivery that still owes production keeps its execution —
 *  and thus its ToDo — open too, because a task is done only once it runs in production, not on the
 *  merge. */
export function holdsExecutionOpen(d: Pick<Delivery, 'stage' | 'prodStage'>): boolean {
  return isUnsettledDelivery(d) || isProdPending(d);
}

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
    // Holds the execution open: an unsettled dev delivery (PR open or a FAILED tip, WHAT-2) OR a
    // merged delivery still owing its production step (WHAT-1) — a task historizes only once it runs
    // in production, never on the merge alone.
    if (d.executionId && holdsExecutionOpen(d)) out.add(d.executionId);
  }
  return out;
}

/** The open delivery's maintenance facts an open todo needs to say WHY it waits: whether the
 *  delivery is blocked (K-5, waiting for a release) and, if not, the instant its auto-merge is due
 *  (`mergeBy`). Read straight off the ledger the server states — never derived from a chain stage. */
export interface OpenDeliveryFacts {
  blocked: boolean;
  reason?: string;
  mergeBy?: string;
  /** There is a healthy OPEN delivery — a pull request still working its way to merge, i.e. a live
   *  automatic step. Set whenever a delivery is open, WITH or WITHOUT a stamped `mergeBy` deadline:
   *  a freshly opened pull request is a genuine wait even before the maintenance stamps its window.
   *  It lets the open-list derivation tell "waiting on an open PR" apart from "in the awaiting set
   *  for a reason that is NOT a live step" (a failed tip), so the latter is never dressed as a wait. */
  awaitingMerge?: boolean;
  /** The delivery is merged and awaiting its PRODUCTION step (WHAT-1): the work reached the default
   *  branch, but it is not with the user until it runs in production. */
  prodPending?: boolean;
  /** The last production send FAILED and is being retried by itself (never given up on). The task
   *  stays open and reports itself, but the stack is untouched — this is not a dev-delivery failure. */
  prodFailed?: boolean;
  /** Why the production send failed (only meaningful when `prodFailed`). */
  prodReason?: string;
}

/** Join the OPEN deliveries by the execution they arose from, folding each execution's open rows
 *  into one fact: blocked if ANY of them is (with the first reason), and the SOONEST merge deadline
 *  among them. Only open deliveries carry a live wait, so merged/closed rows are skipped — the same
 *  `isOpenDelivery` rule the open-delivery set uses, so the two never disagree. Keyed by execution
 *  id, which is how the open-list derivation looks a waiting execution up (B-8). */
export function openDeliveryFactsByExecution(list: readonly Delivery[]): Map<string, OpenDeliveryFacts> {
  const out = new Map<string, OpenDeliveryFacts>();
  for (const d of list) {
    if (!d.executionId || !holdsExecutionOpen(d)) continue;
    const cur = out.get(d.executionId) ?? { blocked: false };
    // A FAILED delivery is a wait that needs no clock: it holds the execution open until it is
    // resolved or rolled back. It reads as blocked, carrying its failure as the reason, so the open
    // ToDo says WHY it waits — the tip failed — not just that it does.
    if (isFailedDelivery(d)) {
      cur.blocked = true;
      cur.reason ??= d.failedReason ?? 'delivery failed';
    }
    if (d.blocked) {
      cur.blocked = true;
      cur.reason ??= d.blockedReason;
    }
    // A healthy open pull request is a live automatic step, deadline stamped or not — so the open
    // list can wait on it instead of mistaking a deadline-less open PR for "nothing is happening".
    if (isOpenDelivery(d)) cur.awaitingMerge = true;
    // Merged and awaiting production (WHAT-1): the work reached the default branch but is not running
    // in production yet. A failed production send is retrying — the task stays open and reports it,
    // but this is NOT a dev-delivery failure, so it never reads as blocked (the stack is untouched).
    if (isProdPending(d)) {
      cur.prodPending = true;
      if ((d.prodStage ?? '').trim() === 'failed') {
        cur.prodFailed = true;
        cur.prodReason ??= d.prodFailedReason;
      }
    }
    if (d.mergeBy && (!cur.mergeBy || d.mergeBy < cur.mergeBy)) cur.mergeBy = d.mergeBy;
    out.set(d.executionId, cur);
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
  /** The failed delivery sitting at the TIP of this repository's stack, or null when the tip is
   *  sound. It is what lets the surface say — without anyone asking — whether the tip is fine, and if
   *  not, which layer is broken and on what (WHAT-4). "At the tip" means the newest unsettled
   *  delivery is a failed one; a failed layer with healthy work stacked on top is not possible,
   *  because the chain holds a new order before it branches past a failed tip. */
  failedTip: Delivery | null;
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
    const sorted = sortDeliveries(entries); // newest first
    const open = sorted.filter(isOpenDelivery);
    // The tip is the newest unsettled delivery; the tip is broken when that delivery is a failed one.
    const tip = sorted.find(isUnsettledDelivery) ?? null;
    groups.push({
      repo,
      deliveries: sorted,
      latestOpen: open[0] ?? null,
      openCount: open.length,
      failedTip: tip && isFailedDelivery(tip) ? tip : null,
      latestAt: sorted.length > 0 ? deliveryAt(sorted[0]) : 0,
    });
  }
  return groups.sort((a, b) => (b.latestAt !== a.latestAt ? b.latestAt - a.latestAt : a.repo.localeCompare(b.repo)));
}

/** Whether a delivery still has an effect left to undo: an open, a FAILED, or a merged one. A closed
 *  or already rolled-back delivery is settled, so its rollback control is not offered at all — no
 *  button into the void (REQ-040.3). A reversal itself is never rolled back again. Rolling back a
 *  FAILED tip is the second of the two ways back: it dismantles the broken layer so the last sound
 *  layer becomes the tip again. */
export function canRollback(d: Delivery): boolean {
  if (d.reversalOf) return false;
  return isUnsettledDelivery(d) || (d.stage ?? '').trim() === 'merged';
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
  const failed = isFailedDelivery(d);
  if (failed) {
    return {
      effect: `Dismantles the failed tip in ${d.repo}: ${commitRange(d)} is counter-booked off the dev branch. This work never shipped, so there is no pull request to close.`,
      result: 'The last sound layer becomes the tip again; the failed delivery reads as rolled back and no longer holds new orders.',
      undo: 'Nothing is rewritten — running the order again delivers it anew.',
    };
  }
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
