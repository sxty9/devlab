// The file tree's decisions — flattening, keyboard motion, drop targets and menu entries —
// separated from its rendering so the list behaviour (cross-cutting 11: context menus, keyboard
// navigation, drag & drop) is testable without a DOM. Deliberately free of runtime imports, like
// the other decision modules of this codebase, so `node --test` loads it directly. Extracting the
// FILES from a drop is not repeated here: a drop carries the same DataTransfer a paste does, so the
// call site hands it to the one shared extractor (lib/file).

import type { FileNode } from '@/types';

/** One visible row of the tree: the node plus how deep it sits. */
export interface VisibleRow {
  node: FileNode;
  depth: number;
}

/** The rows currently on screen: every node whose ancestors are all expanded, in view order. */
export function visibleRows(nodes: readonly FileNode[], isOpen: (id: string) => boolean, depth = 0): VisibleRow[] {
  const out: VisibleRow[] = [];
  for (const node of nodes) {
    out.push({ node, depth });
    if (node.kind === 'dir' && node.children && isOpen(node.id)) {
      out.push(...visibleRows(node.children, isOpen, depth + 1));
    }
  }
  return out;
}

/** What a key press does to the tree. `move` carries the row to focus; the rest are actions on the
 *  focused row. */
export type TreeAction =
  | { kind: 'none' }
  | { kind: 'move'; index: number }
  | { kind: 'open' }
  | { kind: 'expand' }
  | { kind: 'collapse' }
  | { kind: 'parent' }
  | { kind: 'menu' }
  | { kind: 'copyPath' };

/** The key press as it arrives from the DOM (the fields this module needs). */
export interface KeyPress {
  key: string;
  metaKey?: boolean;
  ctrlKey?: boolean;
  shiftKey?: boolean;
}

/** Translate a key press over the tree into an action. Arrow keys move and expand/collapse, Enter
 *  and Space open, Home/End jump, Cmd/Ctrl+C copies the path and Cmd/Ctrl+Enter (or the menu key)
 *  opens the context menu — the same shortcuts a list is expected to answer (D 17). */
export function treeKeyAction(e: KeyPress, index: number, rows: readonly VisibleRow[], isOpen: (id: string) => boolean): TreeAction {
  const last = rows.length - 1;
  if (last < 0) return { kind: 'none' };
  const cmd = !!(e.metaKey || e.ctrlKey);
  const row = rows[index];

  switch (e.key) {
    case 'ArrowDown':
      return { kind: 'move', index: Math.min(index < 0 ? 0 : index + 1, last) };
    case 'ArrowUp':
      return { kind: 'move', index: index <= 0 ? 0 : index - 1 };
    case 'Home':
      return { kind: 'move', index: 0 };
    case 'End':
      return { kind: 'move', index: last };
    case 'PageDown':
      return { kind: 'move', index: Math.min(index + 10, last) };
    case 'PageUp':
      return { kind: 'move', index: Math.max(index - 10, 0) };
    case 'ArrowRight':
      if (row && row.node.kind === 'dir' && !isOpen(row.node.id)) return { kind: 'expand' };
      if (row && row.node.kind === 'dir') return { kind: 'move', index: Math.min(index + 1, last) };
      return { kind: 'none' };
    case 'ArrowLeft':
      if (row && row.node.kind === 'dir' && isOpen(row.node.id)) return { kind: 'collapse' };
      return { kind: 'parent' };
    case 'Enter':
      return cmd ? { kind: 'menu' } : { kind: 'open' };
    case ' ':
      return { kind: 'open' };
    case 'ContextMenu':
      return { kind: 'menu' };
    case 'c':
    case 'C':
      return cmd ? { kind: 'copyPath' } : { kind: 'none' };
    default:
      return { kind: 'none' };
  }
}

/** The index of the row holding `id`, or -1. */
export function rowIndex(rows: readonly VisibleRow[], id: string | null): number {
  if (!id) return -1;
  return rows.findIndex((r) => r.node.id === id);
}

/** The visible row of the nearest expanded ancestor of `index`, or -1 when it is a root. */
export function parentIndex(rows: readonly VisibleRow[], index: number): number {
  const row = rows[index];
  if (!row || row.depth === 0) return -1;
  for (let i = index - 1; i >= 0; i--) {
    if (rows[i].depth < row.depth) return i;
  }
  return -1;
}

/** The MIME type a dragged repo path is carried in, so a drop inside the app can tell an internal
 *  drag from an OS file drop. */
export const PATH_MIME = 'application/x-devlab-path';

/** True when a drag carries OS files rather than an in-app path. */
export function dragHasFiles(dt: DataTransfer | null): boolean {
  if (!dt) return false;
  if (dt.types && Array.from(dt.types).includes(PATH_MIME)) return false;
  return !!dt.types && Array.from(dt.types).includes('Files');
}

/** The folder a dropped file lands in: the directory of the row it was dropped on (a file row
 *  means its own folder), '' for the repo root. */
export function dropFolder(node: FileNode | null): string {
  if (!node) return '';
  if (node.kind === 'dir') return node.id;
  const at = node.id.lastIndexOf('/');
  return at > 0 ? node.id.slice(0, at) : '';
}

/** The repo-relative path a dropped file is written to. */
export function dropPath(folder: string, name: string): string {
  return folder ? `${folder}/${name}` : name;
}

/** One context-menu entry. `danger` marks a destructive action. */
export interface MenuEntry {
  id: string;
  label: string;
  danger?: boolean;
}

/** The context menu of a tree row: what can be done here, and nothing that cannot. Writing entries
 *  appear only where the caller may write (the authorization sits with the caller, not the menu). */
export function treeMenu(node: FileNode, canWrite: boolean): MenuEntry[] {
  const entries: MenuEntry[] = [];
  if (node.kind === 'file') {
    entries.push({ id: 'open', label: 'Open' });
    if (node.status) entries.push({ id: 'diff', label: 'Open diff' });
  } else {
    entries.push({ id: 'toggle', label: 'Expand / collapse' });
  }
  entries.push({ id: 'copyPath', label: 'Copy path' });
  if (canWrite) {
    entries.push({ id: 'newFile', label: node.kind === 'dir' ? 'New file here…' : 'New file…' });
    if (node.kind === 'file' && node.status) entries.push({ id: 'stage', label: 'Stage' });
  }
  return entries;
}

/** The context menu of a change row in the Version Control panel. */
export function changeMenu(staged: boolean, canWrite: boolean): MenuEntry[] {
  const entries: MenuEntry[] = [{ id: 'diff', label: 'Open diff' }, { id: 'copyPath', label: 'Copy path' }];
  if (canWrite) entries.push(staged ? { id: 'unstage', label: 'Unstage' } : { id: 'stage', label: 'Stage' });
  return entries;
}

/** The context menu of an editor tab. */
export function tabMenu(count: number): MenuEntry[] {
  const entries: MenuEntry[] = [{ id: 'close', label: 'Close' }];
  if (count > 1) {
    entries.push({ id: 'closeOthers', label: 'Close others' });
    entries.push({ id: 'closeAll', label: 'Close all' });
  }
  entries.push({ id: 'copyPath', label: 'Copy path' });
  return entries;
}
