// Pure constitution-tree operations (B-31): given the current forest and a user gesture —
// a drop, a keyboard move, a rename — compute WHICH single data-source call realises it.
// The components stay thin (they only render and dispatch), and these rules are unit-tested
// without a DOM. This module is deliberately free of runtime imports so `node --test` loads
// it directly; the two private path helpers below exist for that reason.
import type { MercuryNode } from '../../../types';

/** The scheme-backed namespaces: each is one tree of the constitution store. */
export type SchemeNs = 'axiome' | 'regeln' | 'laeufe' | 'meta';

/** What a drag carries: an axiom leaf or a whole category, by its path + name. */
export interface DragItem {
  kind: 'axiom' | 'category';
  path: string;
  name: string;
}

export type DropPos = 'inside' | 'before' | 'after';

/** The one data-source call a gesture resolves to (or an explained no-op). */
export type TreeAction =
  | { op: 'move'; from: string; to: string }
  | { op: 'moveCategory'; from: string; to: string }
  | { op: 'reorder'; category: string; order: string[] }
  | { op: 'none'; reason?: string };

/** The subset of the DataSource the tree mutates through — the SAME access points the rest of
 *  Mercury uses (no parallel path). */
export interface TreeMutationSource {
  mercuryMoveAxiom(from: string, to: string): Promise<{ path: string }>;
  mercuryMoveCategory(from: string, to: string): Promise<{ moved: number }>;
  mercuryReorder(category: string, order: string[]): Promise<void>;
}

const basename = (p: string): string => p.split('/').pop() ?? p;
const dirname = (p: string): string => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '');

/** Find a node by path within a forest. */
export function findNode(nodes: MercuryNode[], path: string): MercuryNode | null {
  for (const n of nodes) {
    if (n.path === path) return n;
    if (n.children) {
      const hit = findNode(n.children, path);
      if (hit) return hit;
    }
  }
  return null;
}

/** The children (in current display order) of the node at parentPath — or the roots when
 *  parentPath is the namespace itself. */
export function childrenOf(roots: MercuryNode[], parentPath: string, namespace: string): MercuryNode[] {
  if (parentPath === namespace) return roots;
  return findNode(roots, parentPath)?.children ?? [];
}

/** Whether target sits inside item's own subtree (a category must never move into itself). */
function insideOwnSubtree(item: DragItem, targetCat: string): boolean {
  return item.kind === 'category' && (targetCat === item.path || targetCat.startsWith(item.path + '/'));
}

/** Re-nest item under the category targetCat. */
function reNest(item: DragItem, targetCat: string): TreeAction {
  if (insideOwnSubtree(item, targetCat)) {
    return { op: 'none', reason: 'A category cannot be moved into itself.' };
  }
  const to = `${targetCat}/${item.kind === 'axiom' ? basename(item.path) : item.name}`;
  if (to === item.path) return { op: 'none' };
  return item.kind === 'axiom' ? { op: 'move', from: item.path, to } : { op: 'moveCategory', from: item.path, to };
}

/** Resolve a drop: INTO a category re-nests; before/after a sibling of the SAME parent reorders
 *  (the full new child-name order, so a partial list can't silently drop entries); before/after
 *  across branches re-nests into the target's branch. Mirrors the retired monolith's rules. */
export function resolveDrop(
  roots: MercuryNode[],
  namespace: string,
  item: DragItem,
  targetPath: string,
  targetIsAxiom: boolean,
  pos: DropPos,
): TreeAction {
  if (item.path === targetPath) return { op: 'none' };
  if (pos === 'inside' && !targetIsAxiom) return reNest(item, targetPath);

  const targetParent = dirname(targetPath) || namespace;
  const itemParent = dirname(item.path) || namespace;
  if (targetParent !== itemParent) return reNest(item, targetParent);

  const dragKey = basename(item.path);
  const targetKey = basename(targetPath);
  const keys = childrenOf(roots, targetParent, namespace)
    .map((n) => basename(n.path))
    .filter((k) => k !== dragKey);
  const at = keys.indexOf(targetKey);
  if (at < 0) return { op: 'none', reason: 'Drop target is not a sibling.' };
  keys.splice(pos === 'before' ? at : at + 1, 0, dragKey);
  return { op: 'reorder', category: targetParent, order: keys };
}

/** Resolve a keyboard reorder (Cmd/Ctrl+ArrowUp/Down): swap the item with its previous/next
 *  sibling — the keyboard twin of the before/after drop. */
export function resolveKeyboardReorder(
  roots: MercuryNode[],
  namespace: string,
  path: string,
  dir: -1 | 1,
): TreeAction {
  const parent = dirname(path) || namespace;
  const keys = childrenOf(roots, parent, namespace).map((n) => basename(n.path));
  const key = basename(path);
  const at = keys.indexOf(key);
  const to = at + dir;
  if (at < 0 || to < 0 || to >= keys.length) return { op: 'none' };
  keys.splice(at, 1);
  keys.splice(to, 0, key);
  return { op: 'reorder', category: parent, order: keys };
}

/** Apply a resolved action through the data source — the single dispatch every gesture funnels
 *  into. Returns whether anything was written. */
export async function applyTreeAction(source: TreeMutationSource, action: TreeAction): Promise<boolean> {
  switch (action.op) {
    case 'move':
      await source.mercuryMoveAxiom(action.from, action.to);
      return true;
    case 'moveCategory':
      await source.mercuryMoveCategory(action.from, action.to);
      return true;
    case 'reorder':
      await source.mercuryReorder(action.category, action.order);
      return true;
    case 'none':
      return false;
  }
}

/** One visible row of the rendered tree, in display order — the flat list keyboard navigation
 *  walks (ArrowUp/Down select across the whole visible forest). */
export interface VisibleRow {
  node: MercuryNode;
  depth: number;
}

export function flattenVisible(roots: MercuryNode[], isOpen: (path: string) => boolean, depth = 0): VisibleRow[] {
  const rows: VisibleRow[] = [];
  for (const n of roots) {
    rows.push({ node: n, depth });
    if (!n.isAxiom && n.children && isOpen(n.path)) {
      rows.push(...flattenVisible(n.children, isOpen, depth + 1));
    }
  }
  return rows;
}

/** The path selected after ArrowUp/ArrowDown from `current` (null current selects the edge). */
export function neighborPath(rows: VisibleRow[], current: string | null, dir: -1 | 1): string | null {
  if (rows.length === 0) return null;
  const at = current === null ? -1 : rows.findIndex((r) => r.node.path === current);
  if (at < 0) return rows[dir === 1 ? 0 : rows.length - 1].node.path;
  const to = at + dir;
  if (to < 0 || to >= rows.length) return rows[at].node.path;
  return rows[to].node.path;
}

/** Slugify a user-typed name to a path segment (mirrors the backend's one transliteration). */
export function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/ä/g, 'ae')
    .replace(/ö/g, 'oe')
    .replace(/ü/g, 'ue')
    .replace(/ß/g, 'ss')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}
