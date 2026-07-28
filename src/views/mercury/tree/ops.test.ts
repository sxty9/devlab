// Component-level tests of the tree operations (B-31): every gesture — drag & drop, reorder,
// category move, keyboard reorder — resolves to exactly the right data-source call. The fake
// source below records calls; the assertions pin both the resolution rules and the dispatch.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  applyTreeAction,
  childrenOf,
  findNode,
  flattenVisible,
  neighborPath,
  resolveDrop,
  resolveKeyboardReorder,
  slugify,
  type DragItem,
  type TreeMutationSource,
} from './ops.ts';
import type { MercuryNode } from '../../../types.ts';

// axiome/
//   architecture/          (category)
//     minimalism.md        (axiom)
//     reuse.md             (axiom)
//     nested/              (category)
//       deep.md            (axiom)
//   environment/           (category)
//     drag-drop.md         (axiom)
//   top-level.md           (axiom)
const forest: MercuryNode[] = [
  {
    name: 'architecture',
    path: 'axiome/architecture',
    isAxiom: false,
    children: [
      { name: 'minimalism', path: 'axiome/architecture/minimalism.md', isAxiom: true },
      { name: 'reuse', path: 'axiome/architecture/reuse.md', isAxiom: true },
      {
        name: 'nested',
        path: 'axiome/architecture/nested',
        isAxiom: false,
        children: [{ name: 'deep', path: 'axiome/architecture/nested/deep.md', isAxiom: true }],
      },
    ],
  },
  {
    name: 'environment',
    path: 'axiome/environment',
    isAxiom: false,
    children: [{ name: 'drag-drop', path: 'axiome/environment/drag-drop.md', isAxiom: true }],
  },
  { name: 'top-level', path: 'axiome/top-level.md', isAxiom: true },
];

const axiom = (path: string): DragItem => ({ kind: 'axiom', path, name: path.split('/').pop() ?? path });
const category = (path: string): DragItem => ({ kind: 'category', path, name: path.split('/').pop() ?? path });

/** A recording fake of the mutation slice of the DataSource. */
function fakeSource(): TreeMutationSource & { calls: unknown[][] } {
  const calls: unknown[][] = [];
  return {
    calls,
    async mercuryMoveAxiom(from, to) {
      calls.push(['mercuryMoveAxiom', from, to]);
      return { path: to };
    },
    async mercuryMoveCategory(from, to) {
      calls.push(['mercuryMoveCategory', from, to]);
      return { moved: 1 };
    },
    async mercuryReorder(cat, order) {
      calls.push(['mercuryReorder', cat, order]);
    },
  };
}

test('lookup: findNode and childrenOf walk the forest (namespace root = roots)', () => {
  assert.equal(findNode(forest, 'axiome/architecture/nested/deep.md')?.name, 'deep');
  assert.equal(findNode(forest, 'axiome/absent'), null);
  assert.deepEqual(
    childrenOf(forest, 'axiome', 'axiome').map((n) => n.name),
    ['architecture', 'environment', 'top-level'],
  );
  assert.deepEqual(
    childrenOf(forest, 'axiome/architecture', 'axiome').map((n) => n.name),
    ['minimalism', 'reuse', 'nested'],
  );
});

test('drop INTO a category re-files the axiom there — one mercuryMoveAxiom call', async () => {
  const a = resolveDrop(forest, 'axiome', axiom('axiome/architecture/minimalism.md'), 'axiome/environment', false, 'inside');
  assert.deepEqual(a, { op: 'move', from: 'axiome/architecture/minimalism.md', to: 'axiome/environment/minimalism.md' });

  const src = fakeSource();
  assert.equal(await applyTreeAction(src, a), true);
  assert.deepEqual(src.calls, [['mercuryMoveAxiom', 'axiome/architecture/minimalism.md', 'axiome/environment/minimalism.md']]);
});

test('drop before/after a sibling reorders within the parent — the FULL child order is sent', async () => {
  // Drag reuse before minimalism (same parent) → complete new order of that category.
  const a = resolveDrop(
    forest,
    'axiome',
    axiom('axiome/architecture/reuse.md'),
    'axiome/architecture/minimalism.md',
    true,
    'before',
  );
  assert.deepEqual(a, {
    op: 'reorder',
    category: 'axiome/architecture',
    order: ['reuse.md', 'minimalism.md', 'nested'],
  });

  const src = fakeSource();
  await applyTreeAction(src, a);
  assert.deepEqual(src.calls, [['mercuryReorder', 'axiome/architecture', ['reuse.md', 'minimalism.md', 'nested']]]);
});

test('drop after a sibling in another branch re-nests into that branch', () => {
  const a = resolveDrop(
    forest,
    'axiome',
    axiom('axiome/architecture/minimalism.md'),
    'axiome/environment/drag-drop.md',
    true,
    'after',
  );
  assert.deepEqual(a, { op: 'move', from: 'axiome/architecture/minimalism.md', to: 'axiome/environment/minimalism.md' });
});

test('drop a CATEGORY into another category moves the whole category', async () => {
  const a = resolveDrop(forest, 'axiome', category('axiome/architecture/nested'), 'axiome/environment', false, 'inside');
  assert.deepEqual(a, { op: 'moveCategory', from: 'axiome/architecture/nested', to: 'axiome/environment/nested' });

  const src = fakeSource();
  await applyTreeAction(src, a);
  assert.deepEqual(src.calls, [['mercuryMoveCategory', 'axiome/architecture/nested', 'axiome/environment/nested']]);
});

test('drop onto the namespace root lifts the item to the top level', () => {
  const a = resolveDrop(forest, 'axiome', axiom('axiome/architecture/nested/deep.md'), 'axiome', false, 'inside');
  assert.deepEqual(a, { op: 'move', from: 'axiome/architecture/nested/deep.md', to: 'axiome/deep.md' });
});

test('guards: self-drop, category-into-own-subtree and no-op moves write NOTHING', async () => {
  const src = fakeSource();
  // A category can never move into its own subtree.
  const selfNest = resolveDrop(forest, 'axiome', category('axiome/architecture'), 'axiome/architecture/nested', false, 'inside');
  assert.equal(selfNest.op, 'none');
  // Dropping an item onto itself is a no-op.
  const onSelf = resolveDrop(forest, 'axiome', axiom('axiome/top-level.md'), 'axiome/top-level.md', true, 'before');
  assert.equal(onSelf.op, 'none');
  // Re-nesting into the parent it already lives in is a no-op.
  const samePlace = resolveDrop(forest, 'axiome', axiom('axiome/environment/drag-drop.md'), 'axiome/environment', false, 'inside');
  assert.equal(samePlace.op, 'none');

  for (const a of [selfNest, onSelf, samePlace]) assert.equal(await applyTreeAction(src, a), false);
  assert.deepEqual(src.calls, []);
});

test('keyboard reorder (Cmd/Ctrl+Arrow) swaps with the neighbouring sibling; edges are no-ops', async () => {
  const down = resolveKeyboardReorder(forest, 'axiome', 'axiome/architecture/minimalism.md', 1);
  assert.deepEqual(down, {
    op: 'reorder',
    category: 'axiome/architecture',
    order: ['reuse.md', 'minimalism.md', 'nested'],
  });
  // Root level: the namespace itself is the category key.
  const rootUp = resolveKeyboardReorder(forest, 'axiome', 'axiome/environment', -1);
  assert.deepEqual(rootUp, { op: 'reorder', category: 'axiome', order: ['environment', 'architecture', 'top-level.md'] });
  // First child up / last child down: nothing to swap with.
  assert.equal(resolveKeyboardReorder(forest, 'axiome', 'axiome/architecture/minimalism.md', -1).op, 'none');
  assert.equal(resolveKeyboardReorder(forest, 'axiome', 'axiome/top-level.md', 1).op, 'none');

  const src = fakeSource();
  await applyTreeAction(src, down);
  assert.deepEqual(src.calls, [['mercuryReorder', 'axiome/architecture', ['reuse.md', 'minimalism.md', 'nested']]]);
});

test('keyboard selection walks exactly the VISIBLE rows (collapsed branches are skipped)', () => {
  const openAll = flattenVisible(forest, () => true);
  assert.deepEqual(
    openAll.map((r) => r.node.path),
    [
      'axiome/architecture',
      'axiome/architecture/minimalism.md',
      'axiome/architecture/reuse.md',
      'axiome/architecture/nested',
      'axiome/architecture/nested/deep.md',
      'axiome/environment',
      'axiome/environment/drag-drop.md',
      'axiome/top-level.md',
    ],
  );
  const collapsed = flattenVisible(forest, (p) => p !== 'axiome/architecture');
  assert.deepEqual(
    collapsed.map((r) => r.node.path),
    ['axiome/architecture', 'axiome/environment', 'axiome/environment/drag-drop.md', 'axiome/top-level.md'],
  );
  // Arrow navigation over the visible rows.
  assert.equal(neighborPath(collapsed, 'axiome/architecture', 1), 'axiome/environment');
  assert.equal(neighborPath(collapsed, null, 1), 'axiome/architecture');
  assert.equal(neighborPath(collapsed, null, -1), 'axiome/top-level.md');
  assert.equal(neighborPath(collapsed, 'axiome/top-level.md', 1), 'axiome/top-level.md');
});

test('slugify mirrors the backend transliteration', () => {
  assert.equal(slugify('Keine Redundanz'), 'keine-redundanz');
  assert.equal(slugify('Übergreifende Läufe & Größen'), 'uebergreifende-laeufe-groessen');
  assert.equal(slugify('  --weird__input!!  '), 'weird-input');
});
