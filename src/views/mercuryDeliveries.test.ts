import { test } from 'node:test';
import assert from 'node:assert/strict';
import { groupDeliveriesByRepo, canRollback, summarizeRollbackOutcome, DELIVERY_STATUS, shortSha } from './mercuryDeliveries.ts';
import type { Delivery, RollbackOutcome } from '@/types';

const dlv = (over: Partial<Delivery> & Pick<Delivery, 'id' | 'repo' | 'status' | 'createdAt'>): Delivery => ({
  runId: 'run_x',
  resultId: 'res_x',
  branch: `mercury-run/run_x/${over.id}`,
  devBranch: 'mercury-dev',
  baseBranch: 'main',
  fromCommit: '1111111aaaa',
  toCommit: '2222222bbbb',
  ...over,
});

test('groupDeliveriesByRepo groups per repo, newest first, repos ordered by newest delivery', () => {
  const groups = groupDeliveriesByRepo([
    dlv({ id: 'a', repo: 'o/one', status: 'merged', createdAt: '2026-01-01T00:00:00Z' }),
    dlv({ id: 'b', repo: 'o/two', status: 'open', createdAt: '2026-01-03T00:00:00Z' }),
    dlv({ id: 'c', repo: 'o/one', status: 'open', createdAt: '2026-01-02T00:00:00Z' }),
  ]);
  // o/two has the single newest delivery (Jan 3) → it leads.
  assert.deepEqual(groups.map((g) => g.repo), ['o/two', 'o/one']);
  // Within o/one, newest first: c (Jan 2) before a (Jan 1).
  const one = groups.find((g) => g.repo === 'o/one')!;
  assert.deepEqual(one.deliveries.map((d) => d.id), ['c', 'a']);
});

test('devServes is the most recent OPEN delivery; null when nothing is open', () => {
  const [g] = groupDeliveriesByRepo([
    dlv({ id: 'a', repo: 'o/one', status: 'open', createdAt: '2026-01-01T00:00:00Z' }),
    dlv({ id: 'b', repo: 'o/one', status: 'open', createdAt: '2026-01-02T00:00:00Z' }),
    dlv({ id: 'c', repo: 'o/one', status: 'merged', createdAt: '2026-01-03T00:00:00Z' }),
  ]);
  assert.equal(g.devServes?.id, 'b'); // newest OPEN, not the newer merged one
  assert.equal(g.openCount, 2);

  const [allMerged] = groupDeliveriesByRepo([
    dlv({ id: 'a', repo: 'o/one', status: 'merged', createdAt: '2026-01-01T00:00:00Z' }),
    dlv({ id: 'b', repo: 'o/one', status: 'reverted', createdAt: '2026-01-02T00:00:00Z' }),
  ]);
  assert.equal(allMerged.devServes, null); // dev == default branch
  assert.equal(allMerged.openCount, 0);
});

test('canRollback only for open or merged deliveries', () => {
  assert.equal(canRollback(dlv({ id: 'a', repo: 'r', status: 'open', createdAt: '' })), true);
  assert.equal(canRollback(dlv({ id: 'a', repo: 'r', status: 'merged', createdAt: '' })), true);
  assert.equal(canRollback(dlv({ id: 'a', repo: 'r', status: 'closed', createdAt: '' })), false);
  assert.equal(canRollback(dlv({ id: 'a', repo: 'r', status: 'reverted', createdAt: '' })), false);
});

test('DELIVERY_STATUS covers every status', () => {
  for (const s of ['open', 'merged', 'closed', 'reverted'] as const) {
    assert.ok(DELIVERY_STATUS[s].label.length > 0, `label for ${s}`);
  }
});

test('summarizeRollbackOutcome names each branch honestly', () => {
  const base: RollbackOutcome = { deliveryId: 'd', reverted: false };

  const conflict = summarizeRollbackOutcome({ ...base, conflict: true, todoId: 't', laterOpen: 2 });
  assert.match(conflict.title, /von Hand/);
  assert.match(conflict.description, /2 weitere/);
  assert.equal(conflict.variant, 'default');

  const already = summarizeRollbackOutcome({ ...base, alreadyReverted: true });
  assert.match(already.title, /Bereits/);

  const failed = summarizeRollbackOutcome(base);
  assert.equal(failed.variant, 'danger');

  const reversalPr = summarizeRollbackOutcome({ ...base, reverted: true, reversalPrUrl: 'x', deployed: true });
  assert.equal(reversalPr.variant, 'success');
  assert.match(reversalPr.description, /[Uu]mkehrender PR/);
  assert.match(reversalPr.description, /neu ausgeliefert/);

  const closed = summarizeRollbackOutcome({ ...base, reverted: true, closedPr: 42 });
  assert.match(closed.description, /#42/);
});

test('shortSha trims to 7 and tolerates empty', () => {
  assert.equal(shortSha('2222222bbbb'), '2222222');
  assert.equal(shortSha(''), '');
});
