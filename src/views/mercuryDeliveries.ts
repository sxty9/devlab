/*  The Lieferungen ledger's presentation logic, kept apart from the React rendering so it can be reasoned
 *  about and unit-tested. A Delivery is one addressable unit of work a run shipped to a repo — a commit
 *  range on the growing dev branch plus its stacked PR. This module groups the flat ledger per repo, names
 *  what dev currently serves, and turns a delivery's lifecycle status and a rollback's outcome into the
 *  labels the surface shows. One definition here, so every row reads the same way.  */
import type { Delivery, RollbackOutcome } from '@/types';

export type DeliveryTint = 'success' | 'accent' | 'warning' | 'danger' | 'neutral';

/** A delivery's own lifecycle status → label + tint. Distinct from the run STAGE ladder (RUN_STAGE_LABEL):
 *  a delivery exists once its PR is open, so its status tracks that PR (open → merged, or closed/withdrawn)
 *  plus the `reverted` terminal a counter-booking sets. Named once so every surface labels it identically. */
export const DELIVERY_STATUS: Record<Delivery['status'], { label: string; tint: DeliveryTint }> = {
  open: { label: 'PR offen', tint: 'accent' },
  merged: { label: 'main-merged', tint: 'success' },
  closed: { label: 'zurückgezogen', tint: 'warning' },
  reverted: { label: 'zurückgerollt', tint: 'neutral' },
};

export interface RepoDeliveries {
  repo: string; // owner/name
  deliveries: Delivery[]; // newest first
  /** The delivery whose range is the current growing dev tip — the most recent still-OPEN one, i.e. what
   *  dev actually serves beyond the default branch. null when nothing is open (dev == the default branch). */
  devServes: Delivery | null;
  openCount: number;
}

function createdAtMs(d: Delivery | undefined): number {
  const t = d ? Date.parse(d.createdAt) : NaN;
  return Number.isNaN(t) ? 0 : t;
}

/** Group the flat ledger by repo. Repos are ordered by their most recent delivery (newest first); within
 *  each repo deliveries are newest-first, and devServes names what dev currently serves. Pure — the view
 *  renders this, the ordering and dev-tip logic are tested here. */
export function groupDeliveriesByRepo(deliveries: Delivery[]): RepoDeliveries[] {
  const byRepo = new Map<string, Delivery[]>();
  for (const d of deliveries) {
    const list = byRepo.get(d.repo);
    if (list) list.push(d);
    else byRepo.set(d.repo, [d]);
  }
  const groups: RepoDeliveries[] = [];
  for (const [repo, list] of byRepo) {
    const sorted = [...list].sort((a, b) => createdAtMs(b) - createdAtMs(a)); // newest first
    const devServes = sorted.find((d) => d.status === 'open') ?? null;
    groups.push({ repo, deliveries: sorted, devServes, openCount: sorted.filter((d) => d.status === 'open').length });
  }
  groups.sort((a, b) => createdAtMs(b.deliveries[0]) - createdAtMs(a.deliveries[0]));
  return groups;
}

/** Whether a delivery can still be rolled back: only an open or merged one has an effect left to undo. A
 *  closed (withdrawn) or already-reverted one is gone, so its rollback control is not offered. */
export function canRollback(d: Delivery): boolean {
  return d.status === 'open' || d.status === 'merged';
}

/** A completed rollback → a toast summary. The outcome is many-branched — counter-booked cleanly, a
 *  reversing PR for an already-merged delivery, a closed still-open PR, an idempotent no-op, or a conflict
 *  that raised a ToDo instead of guessing — and this states each one honestly. */
export function summarizeRollbackOutcome(o: RollbackOutcome): {
  title: string;
  description: string;
  variant: 'default' | 'success' | 'danger';
} {
  if (o.conflict) {
    return {
      title: 'Rückrollen von Hand nötig',
      description:
        `Spätere Arbeit baut auf dieser Lieferung auf${o.laterOpen ? ` (${o.laterOpen} weitere)` : ''} — ein ` +
        'automatischer Revert würde sie beschädigen. Ein konkretes ToDo wurde angelegt, das die Gegenbuchung von Hand vornimmt.',
      variant: 'default',
    };
  }
  if (o.alreadyReverted) {
    return { title: 'Bereits zurückgerollt', description: 'Diese Lieferung war schon gegengebucht.', variant: 'default' };
  }
  if (!o.reverted) {
    return { title: 'Rückrollen fehlgeschlagen', description: 'Die Lieferung wurde nicht zurückgerollt.', variant: 'danger' };
  }
  const parts: string[] = [];
  if (o.reversalPrUrl) parts.push('umkehrender PR geöffnet');
  else if (o.closedPr) parts.push(`offener PR #${o.closedPr} geschlossen`);
  if (o.noChange) parts.push('war auf dev bereits gegenstandslos');
  if (o.deployed) parts.push('dev neu ausgeliefert');
  const detail = parts.join(', ');
  return {
    title: 'Zurückgerollt',
    description: detail ? detail.charAt(0).toUpperCase() + detail.slice(1) + '.' : 'Die Lieferung wurde auf dem dev-Stand gegengebucht.',
    variant: 'success',
  };
}

/** Short commit sha for display (deliveries store full shas; the ledger shows the first 7). */
export function shortSha(sha: string): string {
  return sha ? sha.slice(0, 7) : '';
}
