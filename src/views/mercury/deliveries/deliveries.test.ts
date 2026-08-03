// Tests of the delivery ledger's presentation logic (F12, REQ-024/025/040.2): grouping, ordering
// (including records without a usable time), the server-stated lifecycle badge, what may still be
// rolled back, and the three sentences a dangerous action owes its caller. Plus the two rules of the
// view that are decided by its source: which repositories get a section, and what it may claim.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import {
  DELIVERY_STAGE,
  canRollback,
  commitRange,
  deliveryAt,
  deliveryBadge,
  PROD_STAGE,
  groupDeliveriesByRepo,
  hasProdStage,
  holdsExecutionOpen,
  isFailedDelivery,
  isOpenDelivery,
  isProdLive,
  isUnsettledDelivery,
  openDeliveryExecutionIds,
  openDeliveryFactsByExecution,
  prodBadge,
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

test('open-delivery facts fold per execution: blocked wins, the soonest deadline is kept, settled rows are ignored', () => {
  const facts = openDeliveryFactsByExecution([
    // Two open deliveries of one execution: the later window and the earlier one — the earlier wins.
    dlv({ id: 'a1', repo: 'o/one', executionId: 'exec_a', stage: 'open', mergeBy: '2026-08-10T00:00:00Z', createdAt: '2026-07-01T00:00:00Z' }),
    dlv({ id: 'a2', repo: 'o/one', executionId: 'exec_a', stage: 'open', mergeBy: '2026-08-08T00:00:00Z', createdAt: '2026-07-01T00:00:00Z' }),
    // A blocked delivery: the block outranks any deadline for its execution.
    dlv({ id: 'b1', repo: 'o/two', executionId: 'exec_b', stage: 'open', blocked: true, blockedReason: 'rate limit', createdAt: '2026-07-01T00:00:00Z' }),
    // A merged delivery contributes NOTHING — it holds no live wait.
    dlv({ id: 'm1', repo: 'o/three', executionId: 'exec_c', stage: 'merged', mergeBy: '2026-08-01T00:00:00Z', createdAt: '2026-07-01T00:00:00Z' }),
    // A delivery the ledger cannot attribute to an execution is skipped (no key to fold under).
    dlv({ id: 'x1', repo: 'o/four', stage: 'open', mergeBy: '2026-08-01T00:00:00Z', createdAt: '2026-07-01T00:00:00Z' }),
  ]);

  // Every open pull request is a live automatic step, so `awaitingMerge` is set alongside its
  // deadline — and even a blocked-yet-open delivery carries it (its PR still exists).
  assert.deepEqual(facts.get('exec_a'), { blocked: false, awaitingMerge: true, mergeBy: '2026-08-08T00:00:00Z' });
  assert.deepEqual(facts.get('exec_b'), { blocked: true, reason: 'rate limit', awaitingMerge: true });
  assert.equal(facts.get('exec_c'), undefined, 'a merged delivery holds no live wait');
  assert.equal(facts.size, 2, 'only the two executions with an OPEN attributed delivery are folded');
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

test('latestOpen names the newest delivery the LEDGER still holds open', () => {
  const [g] = groupDeliveriesByRepo([
    dlv({ id: 'a', repo: 'o/one', stage: 'open', createdAt: '2026-07-01T00:00:00Z' }),
    dlv({ id: 'b', repo: 'o/one', stage: 'open', createdAt: '2026-07-02T00:00:00Z' }),
    dlv({ id: 'c', repo: 'o/one', stage: 'merged', createdAt: '2026-07-03T00:00:00Z' }),
  ]);
  assert.equal(g.latestOpen?.id, 'b'); // the newest OPEN one, not the newer merged one
  assert.equal(g.openCount, 2);

  const [settled] = groupDeliveriesByRepo([
    dlv({ id: 'a', repo: 'o/one', stage: 'merged', createdAt: '2026-07-01T00:00:00Z' }),
    dlv({ id: 'b', repo: 'o/one', stage: 'reverted', createdAt: '2026-07-02T00:00:00Z' }),
  ]);
  // Nothing is open — which says the LEDGER is settled, never what the dev branch happens to carry.
  assert.equal(settled.latestOpen, null);
  assert.equal(settled.openCount, 0);
});

test('EVERY managed repository gets a section, delivered to or not (its dev reset lives there)', () => {
  const groups = groupDeliveriesByRepo(
    [dlv({ id: 'a', repo: 'o/one', stage: 'open', createdAt: '2026-07-01T00:00:00Z' })],
    ['o/one', 'o/untouched', 'o/also-untouched'],
  );
  // A repository the ledger never named must still be present — committed but undelivered work
  // leaves no row, and that is exactly the repository whose dev state may need the reset.
  assert.deepEqual(groups.map((g) => g.repo), ['o/one', 'o/also-untouched', 'o/untouched']);
  const untouched = groups.find((g) => g.repo === 'o/untouched')!;
  assert.deepEqual(untouched.deliveries, []);
  assert.equal(untouched.latestOpen, null);
  assert.equal(untouched.openCount, 0);
  assert.equal(untouched.latestAt, 0);
  // The ledger's own repositories are never lost, even when they are not in the handed-in set.
  const onlyLedger = groupDeliveriesByRepo([dlv({ id: 'a', repo: 'o/gone', createdAt: '2026-07-01T00:00:00Z' })], ['o/one']);
  assert.deepEqual(onlyLedger.map((g) => g.repo).sort(), ['o/gone', 'o/one']);
  // A nameless entry yields no section — there would be nothing to act on.
  assert.deepEqual(groupDeliveriesByRepo([], ['', 'o/one']).map((g) => g.repo), ['o/one']);
});

test('openness is the SERVER-stated stage, and it names the executions still holding one (B-8)', () => {
  assert.equal(isOpenDelivery({ stage: 'open' }), true);
  for (const stage of ['merged', 'closed', 'reverted', '', undefined]) {
    assert.equal(isOpenDelivery({ stage }), false, `stage ${String(stage)} is settled`);
  }
  const ids = openDeliveryExecutionIds([
    dlv({ id: 'a', repo: 'o/one', stage: 'open', createdAt: '2026-07-01T00:00:00Z', executionId: 'exec_open' }),
    dlv({ id: 'b', repo: 'o/one', stage: 'merged', createdAt: '2026-07-02T00:00:00Z', executionId: 'exec_merged' }),
    dlv({ id: 'c', repo: 'o/one', stage: 'closed', createdAt: '2026-07-03T00:00:00Z', executionId: 'exec_closed' }),
    dlv({ id: 'd', repo: 'o/one', stage: 'reverted', createdAt: '2026-07-04T00:00:00Z', executionId: 'exec_reverted' }),
    // A row the ledger cannot attribute holds nothing open.
    dlv({ id: 'e', repo: 'o/one', stage: 'open', createdAt: '2026-07-05T00:00:00Z' }),
  ]);
  assert.deepEqual([...ids], ['exec_open']);
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

test('the dev reset states what it discards and the way back the server really provides', () => {
  const c = resetConfirmation('o/one');
  assert.match(c.effect, /dev branch of o\/one back to its default branch/);
  assert.match(c.result, /Delivered work stays/);
  // workbench.ResetToDefault shelters the discarded tip under a rescue ref before the branch moves,
  // so the sentence must not claim the work is simply lost.
  assert.match(c.undo, /rescue ref/);
  assert.doesNotMatch(c.undo, /are lost;/);
});

test('the view gives EVERY managed repository a section and claims nothing about its branches', () => {
  const src = readFileSync(new URL('./DeliveriesView.tsx', import.meta.url), 'utf8');
  const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '');
  // The managed set comes through the ONE repo access point and decides the sections, so a
  // repository with committed-but-undelivered work still has its dev reset.
  assert.match(code, /source\.repos\(/);
  assert.match(code, /repos\.map\(\(r\) => r\.fullName\)/);
  assert.match(code, /groupDeliveriesByRepo\(list, repoNames\)/);
  // What is stated is what the LEDGER holds. The dev branch's own content is not on this wire, so
  // the view must not put a sentence about it on screen.
  assert.doesNotMatch(src, /dev equals the default branch/);
  assert.doesNotMatch(src, /dev serves/);
  assert.doesNotMatch(code, /devServes/);
});

test('a failed delivery is the tip, is unsettled, and keeps its execution open (WHAT-1/2/4)', () => {
  const failed = dlv({ id: 'f', repo: 'o/one', stage: 'failed', failedReason: 'deliver-dev stage failed: stale root script', createdAt: '2026-07-05T00:00:00Z', executionId: 'exec_f' });
  // A failed delivery reads as failed, is unsettled (holds work), but is NOT a healthy PR-open one.
  assert.equal(isFailedDelivery(failed), true);
  assert.equal(isUnsettledDelivery(failed), true);
  assert.equal(isOpenDelivery(failed), false);
  // Its badge is its own — distinct from a settled one and never a blank chip.
  assert.equal(deliveryBadge(failed).label, 'delivery failed');
  // It keeps its execution in the open ToDo list (B-8), and the open ToDo says WHY it waits.
  assert.deepEqual([...openDeliveryExecutionIds([failed])], ['exec_f']);
  assert.deepEqual(openDeliveryFactsByExecution([failed]).get('exec_f'), {
    blocked: true,
    reason: 'deliver-dev stage failed: stale root script',
  });
});

test('groupDeliveriesByRepo names the failed tip, and it can be rolled back (the second way back)', () => {
  const [g] = groupDeliveriesByRepo([
    dlv({ id: 'base', repo: 'o/one', stage: 'merged', createdAt: '2026-07-01T00:00:00Z' }),
    dlv({ id: 'f', repo: 'o/one', stage: 'failed', failedReason: 'GitHub rejected the pull request', createdAt: '2026-07-02T00:00:00Z' }),
  ]);
  assert.equal(g.failedTip?.id, 'f');
  assert.equal(g.openCount, 0, 'a failed tip is not a healthy open PR');
  // A failed tip offers its rollback; a sound repo has none.
  assert.equal(canRollback(g.failedTip!), true);
  const [sound] = groupDeliveriesByRepo([dlv({ id: 'm', repo: 'o/two', stage: 'merged', createdAt: '2026-07-01T00:00:00Z' })]);
  assert.equal(sound.failedTip, null);
});

test('rolling back a failed tip has its own consequence — no PR to close, the last sound layer returns', () => {
  const c = rollbackConfirmation(dlv({ id: 'f', repo: 'o/one', stage: 'failed', createdAt: '2026-07-02T00:00:00Z' }));
  assert.match(c.effect, /failed tip/);
  assert.match(c.effect, /no pull request to close/);
  assert.match(c.result, /last sound layer becomes the tip/);
});

// ── The production stand (WHAT-1): DEV and PROD are two different stands of one delivery ──────────

test('a merged delivery still owing production holds its execution open, and its two stands differ', () => {
  // DEV says "merged"; PROD says "awaiting production" — the same delivery, two different stands.
  const d: Delivery = {
    id: 'dlv_1', repo: 'o/svc', branch: 'fix/a', fromCommit: 'c0', toCommit: 'c1',
    createdAt: '2026-07-26T11:30:00Z', mergedAt: '2026-07-26T12:00:00Z',
    stage: 'merged', prodStage: 'pending', executionId: 'exec_1',
  };
  assert.equal(deliveryBadge(d).label, DELIVERY_STAGE.merged.label, 'DEV stand: merged');
  assert.equal(prodBadge(d).label, PROD_STAGE.pending.label, 'PROD stand: awaiting production');
  assert.equal(hasProdStage(d), true, 'a merged delivery has a production stand');

  // It holds its execution OPEN (not history-ready) — a task is done only once it runs in production.
  assert.equal(holdsExecutionOpen(d), true);
  assert.deepEqual([...openDeliveryExecutionIds([d])], ['exec_1']);

  // Once live in production, both settle and the PROD view shows it live.
  const live: Delivery = { ...d, prodStage: 'live', prodDeployedAt: '2026-07-26T12:05:00Z' };
  assert.equal(holdsExecutionOpen(live), false);
  assert.deepEqual([...openDeliveryExecutionIds([live])], []);
  assert.equal(prodBadge(live).label, PROD_STAGE.live.label);
  assert.equal(isProdLive(live), true);

  // An unmerged delivery has NO production stand — the PROD view never shows it.
  const open: Delivery = { ...d, mergedAt: undefined, stage: 'open', prodStage: '' };
  assert.equal(hasProdStage(open), false);
});
