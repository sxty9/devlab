// Tests of the delivery ledger's presentation logic (F12, REQ-024/025/040.2): grouping, ordering
// (including records without a usable time), the server-stated lifecycle badge, what may still be
// rolled back, and the three sentences a dangerous action owes its caller.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  DELIVERY_STAGE,
  canRollback,
  commitRange,
  deliveryAt,
  deliveryBadge,
  groupDeliveriesByRepo,
  resetConfirmation,
  rollbackConfirmation,
  shortSha,
  sortDeliveries,
} from './deliveries.ts';
import type { Delivery } from '../../../types';

const dlv = (over: Partial<Delivery> & Pick<Delivery, 'id' | 'repo' | 'createdAt'>): Delivery => ({
  branch: `mercury-delivery/${over.id}`,
  fromCommit: '1111111aaaabbbb',
  toCommit: '2222222ccccdddd',
  stage: 'open',
  ...over,
});

test('every lifecycle stage the server states has a label and a tone', () => {
  for (const s of ['open', 'merged', 'closed', 'reverted']) {
    assert.ok(DELIVERY_STAGE[s].label.length > 0, `label for ${s}`);
    assert.ok(DELIVERY_STAGE[s].tone.length > 0, `tone for ${s}`);
  }
});

test('an unstamped or unfamiliar stage still yields a DEFINED badge (no blank row)', () => {
  assert.deepEqual(deliveryBadge({ stage: 'merged' }), DELIVERY_STAGE.merged);
  assert.equal(deliveryBadge({}).label, 'recorded');
  assert.equal(deliveryBadge({ stage: '' }).label, 'recorded');
  assert.equal(deliveryBadge({ stage: 'from-the-future' }).label, 'from-the-future');
  assert.equal(deliveryBadge({ stage: 'from-the-future' }).tone, 'neutral');
});

test('deliveries read newest first; a record without a usable time sorts last, never vanishes', () => {
  const ordered = sortDeliveries([
    dlv({ id: 'mid', repo: 'o/one', createdAt: '2026-07-02T00:00:00Z' }),
    dlv({ id: 'broken', repo: 'o/one', createdAt: 'not-a-date' }),
    dlv({ id: 'new', repo: 'o/one', createdAt: '2026-07-03T00:00:00Z' }),
    dlv({ id: 'old', repo: 'o/one', createdAt: '2026-07-01T00:00:00Z' }),
  ]);
  assert.deepEqual(ordered.map((d) => d.id), ['new', 'mid', 'old', 'broken']);
  assert.equal(deliveryAt(dlv({ id: 'x', repo: 'r', createdAt: '' })), 0);
});

test('ordering is stable for equal instants (the ledger order survives)', () => {
  const a = dlv({ id: 'a', repo: 'r', createdAt: '2026-07-01T00:00:00Z' });
  const b = dlv({ id: 'b', repo: 'r', createdAt: '2026-07-01T00:00:00Z' });
  assert.deepEqual(sortDeliveries([a, b]).map((d) => d.id), ['a', 'b']);
  assert.deepEqual(sortDeliveries([b, a]).map((d) => d.id), ['b', 'a']);
});

test('the ledger groups per repository, repos led by their most recent delivery', () => {
  const groups = groupDeliveriesByRepo([
    dlv({ id: 'a', repo: 'o/one', stage: 'merged', createdAt: '2026-07-01T00:00:00Z' }),
    dlv({ id: 'b', repo: 'o/two', stage: 'open', createdAt: '2026-07-03T00:00:00Z' }),
    dlv({ id: 'c', repo: 'o/one', stage: 'open', createdAt: '2026-07-02T00:00:00Z' }),
  ]);
  assert.deepEqual(groups.map((g) => g.repo), ['o/two', 'o/one']);
  assert.deepEqual(groups.find((g) => g.repo === 'o/one')!.deliveries.map((d) => d.id), ['c', 'a']);
});

test('devServes names the newest OPEN delivery — what dev carries beyond the default branch', () => {
  const [g] = groupDeliveriesByRepo([
    dlv({ id: 'a', repo: 'o/one', stage: 'open', createdAt: '2026-07-01T00:00:00Z' }),
    dlv({ id: 'b', repo: 'o/one', stage: 'open', createdAt: '2026-07-02T00:00:00Z' }),
    dlv({ id: 'c', repo: 'o/one', stage: 'merged', createdAt: '2026-07-03T00:00:00Z' }),
  ]);
  assert.equal(g.devServes?.id, 'b'); // the newest OPEN one, not the newer merged one
  assert.equal(g.openCount, 2);

  const [settled] = groupDeliveriesByRepo([
    dlv({ id: 'a', repo: 'o/one', stage: 'merged', createdAt: '2026-07-01T00:00:00Z' }),
    dlv({ id: 'b', repo: 'o/one', stage: 'reverted', createdAt: '2026-07-02T00:00:00Z' }),
  ]);
  assert.equal(settled.devServes, null); // dev equals the default branch
  assert.equal(settled.openCount, 0);
});

test('only an open or merged delivery offers a rollback — no button into the void (REQ-040.3)', () => {
  const at = { repo: 'r', createdAt: '2026-07-01T00:00:00Z' };
  assert.equal(canRollback(dlv({ id: 'a', ...at, stage: 'open' })), true);
  assert.equal(canRollback(dlv({ id: 'a', ...at, stage: 'merged' })), true);
  assert.equal(canRollback(dlv({ id: 'a', ...at, stage: 'closed' })), false);
  assert.equal(canRollback(dlv({ id: 'a', ...at, stage: 'reverted' })), false);
  // A reversal is not itself rolled back — the ledger only ever grows forward.
  assert.equal(canRollback(dlv({ id: 'a', ...at, stage: 'open', reversalOf: 'dlv_x' })), false);
});

test('a delivery shows its exact commit span, shortened', () => {
  assert.equal(shortSha('2222222ccccdddd'), '2222222');
  assert.equal(shortSha(''), '');
  assert.equal(commitRange(dlv({ id: 'a', repo: 'r', createdAt: '' })), '1111111..2222222');
  assert.equal(commitRange(dlv({ id: 'a', repo: 'r', createdAt: '', fromCommit: '', toCommit: '' })), '—');
  assert.equal(commitRange(dlv({ id: 'a', repo: 'r', createdAt: '', fromCommit: '' })), '?..2222222');
});

test('a rollback states effect, resulting state and the way back (REQ-040.6)', () => {
  const merged = rollbackConfirmation(dlv({ id: 'a', repo: 'o/one', createdAt: '2026-07-01T00:00:00Z', stage: 'merged' }));
  assert.match(merged.effect, /Counter-books 1111111\.\.2222222 in o\/one/);
  assert.match(merged.effect, /pull request/);
  assert.match(merged.result, /rolled back/);
  assert.match(merged.undo, /Nothing is rewritten/);

  const open = rollbackConfirmation(dlv({ id: 'a', repo: 'o/one', createdAt: '2026-07-01T00:00:00Z', stage: 'open' }));
  assert.match(open.effect, /closed with the justification/);
  assert.match(open.result, /dev branch no longer carries/);
  for (const part of [merged, open]) {
    for (const [key, text] of Object.entries(part)) assert.ok(text.length > 0, `${key} must be stated`);
  }
});

test('the dev reset states what it discards (REQ-022.4/040.6)', () => {
  const c = resetConfirmation('o/one');
  assert.match(c.effect, /dev branch of o\/one back to its default branch/);
  assert.match(c.result, /Delivered work stays/);
  assert.match(c.undo, /never published are lost/);
});
