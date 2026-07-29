import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { FileNode } from '../types';
import {
  PATH_MIME,
  changeMenu,
  dragHasFiles,
  dropFolder,
  dropPath,
  parentIndex,
  rowIndex,
  tabMenu,
  treeKeyAction,
  treeMenu,
  visibleRows,
} from './treeOps.ts';

const file = (id: string): FileNode => ({ id, name: id.split('/').pop()!, kind: 'file' });
const dir = (id: string, children: FileNode[]): FileNode => ({
  id,
  name: id.split('/').pop()!,
  kind: 'dir',
  children,
});

const tree: FileNode[] = [
  dir('src', [file('src/a.ts'), dir('src/deep', [file('src/deep/b.ts')])]),
  file('README.md'),
];

test('only the rows under expanded folders are visible, in view order', () => {
  const closed = visibleRows(tree, () => false);
  assert.deepEqual(
    closed.map((r) => r.node.id),
    ['src', 'README.md'],
  );

  const open = visibleRows(tree, (id) => id === 'src');
  assert.deepEqual(
    open.map((r) => r.node.id),
    ['src', 'src/a.ts', 'src/deep', 'README.md'],
  );
  assert.deepEqual(
    open.map((r) => r.depth),
    [0, 1, 1, 0],
  );

  const all = visibleRows(tree, () => true);
  assert.deepEqual(
    all.map((r) => r.node.id),
    ['src', 'src/a.ts', 'src/deep', 'src/deep/b.ts', 'README.md'],
  );
});

test('arrow keys move within the list and never off its ends', () => {
  const rows = visibleRows(tree, () => true);
  const openAll = () => true;
  assert.deepEqual(treeKeyAction({ key: 'ArrowDown' }, 0, rows, openAll), { kind: 'move', index: 1 });
  assert.deepEqual(treeKeyAction({ key: 'ArrowDown' }, rows.length - 1, rows, openAll), {
    kind: 'move',
    index: rows.length - 1,
  });
  assert.deepEqual(treeKeyAction({ key: 'ArrowUp' }, 0, rows, openAll), { kind: 'move', index: 0 });
  assert.deepEqual(treeKeyAction({ key: 'Home' }, 3, rows, openAll), { kind: 'move', index: 0 });
  assert.deepEqual(treeKeyAction({ key: 'End' }, 0, rows, openAll), { kind: 'move', index: rows.length - 1 });
  assert.deepEqual(treeKeyAction({ key: 'PageDown' }, 0, rows, openAll), { kind: 'move', index: rows.length - 1 });
  assert.deepEqual(treeKeyAction({ key: 'PageUp' }, 2, rows, openAll), { kind: 'move', index: 0 });
  // Nothing to move in an empty tree.
  assert.deepEqual(treeKeyAction({ key: 'ArrowDown' }, 0, [], openAll), { kind: 'none' });
});

test('left and right expand, collapse and walk out of a folder', () => {
  const rows = visibleRows(tree, (id) => id === 'src');
  const isOpen = (id: string) => id === 'src';
  // A closed folder expands; an open one steps into its first child.
  assert.deepEqual(treeKeyAction({ key: 'ArrowRight' }, 2, rows, isOpen), { kind: 'expand' });
  assert.deepEqual(treeKeyAction({ key: 'ArrowRight' }, 0, rows, isOpen), { kind: 'move', index: 1 });
  // An open folder collapses; a file walks out to its parent.
  assert.deepEqual(treeKeyAction({ key: 'ArrowLeft' }, 0, rows, isOpen), { kind: 'collapse' });
  assert.deepEqual(treeKeyAction({ key: 'ArrowLeft' }, 1, rows, isOpen), { kind: 'parent' });
  // A file's arrow-right does nothing rather than something surprising.
  assert.deepEqual(treeKeyAction({ key: 'ArrowRight' }, 1, rows, isOpen), { kind: 'none' });
});

test('the usual command combinations work: open, copy path, context menu', () => {
  const rows = visibleRows(tree, () => true);
  const openAll = () => true;
  assert.deepEqual(treeKeyAction({ key: 'Enter' }, 1, rows, openAll), { kind: 'open' });
  assert.deepEqual(treeKeyAction({ key: ' ' }, 1, rows, openAll), { kind: 'open' });
  assert.deepEqual(treeKeyAction({ key: 'Enter', metaKey: true }, 1, rows, openAll), { kind: 'menu' });
  assert.deepEqual(treeKeyAction({ key: 'ContextMenu' }, 1, rows, openAll), { kind: 'menu' });
  assert.deepEqual(treeKeyAction({ key: 'c', metaKey: true }, 1, rows, openAll), { kind: 'copyPath' });
  assert.deepEqual(treeKeyAction({ key: 'c', ctrlKey: true }, 1, rows, openAll), { kind: 'copyPath' });
  assert.deepEqual(treeKeyAction({ key: 'c' }, 1, rows, openAll), { kind: 'none' }); // plain typing
  assert.deepEqual(treeKeyAction({ key: 'x', metaKey: true }, 1, rows, openAll), { kind: 'none' });
});

test('a row and its parent row are found by id', () => {
  const rows = visibleRows(tree, () => true);
  assert.equal(rowIndex(rows, 'src/deep/b.ts'), 3);
  assert.equal(rowIndex(rows, 'nope'), -1);
  assert.equal(rowIndex(rows, null), -1);
  assert.equal(parentIndex(rows, 3), 2); // b.ts → deep
  assert.equal(parentIndex(rows, 2), 0); // deep → src
  assert.equal(parentIndex(rows, 0), -1); // a root has none
});

test('a dropped file lands in the folder it was dropped on', () => {
  assert.equal(dropFolder(dir('src', [])), 'src');
  assert.equal(dropFolder(file('src/a.ts')), 'src'); // a file means its own folder
  assert.equal(dropFolder(file('README.md')), ''); // repo root
  assert.equal(dropFolder(null), '');
  assert.equal(dropPath('src', 'x.ts'), 'src/x.ts');
  assert.equal(dropPath('', 'x.ts'), 'x.ts');
});

test('an OS file drag is told apart from an in-app path drag', () => {
  const dt = (types: string[]) => ({ types } as unknown as DataTransfer);
  assert.equal(dragHasFiles(dt(['Files'])), true);
  assert.equal(dragHasFiles(dt(['Files', PATH_MIME])), false); // our own row being dragged
  assert.equal(dragHasFiles(dt(['text/plain'])), false);
  assert.equal(dragHasFiles(null), false);
});

test('menus offer what is possible here and nothing that is not', () => {
  const readOnly = treeMenu(file('src/a.ts'), false).map((e) => e.id);
  assert.deepEqual(readOnly, ['open', 'copyPath']);

  const writable = treeMenu(file('src/a.ts'), true).map((e) => e.id);
  assert.deepEqual(writable, ['open', 'copyPath', 'newFile']);

  const changed = treeMenu({ ...file('src/a.ts'), status: 'modified' }, true).map((e) => e.id);
  assert.deepEqual(changed, ['open', 'diff', 'copyPath', 'newFile', 'stage']);

  const folder = treeMenu(dir('src', []), true).map((e) => e.id);
  assert.deepEqual(folder, ['toggle', 'copyPath', 'newFile']);

  assert.deepEqual(changeMenu(false, true).map((e) => e.id), ['diff', 'copyPath', 'stage']);
  assert.deepEqual(changeMenu(true, true).map((e) => e.id), ['diff', 'copyPath', 'unstage']);
  assert.deepEqual(changeMenu(true, false).map((e) => e.id), ['diff', 'copyPath']);

  assert.deepEqual(tabMenu(1).map((e) => e.id), ['close', 'copyPath']);
  assert.deepEqual(tabMenu(3).map((e) => e.id), ['close', 'closeOthers', 'closeAll', 'copyPath']);
});
