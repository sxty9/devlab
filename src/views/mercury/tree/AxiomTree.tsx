// AxiomTree — the constitution surface (S5): the four scheme namespaces as arbitrarily deep
// category trees over the ONE /api/mercury access point. Re-filing, reordering and renaming are
// gestures — drag & drop, keyboard (arrows, Cmd/Ctrl+arrows) and a right-click menu — and every
// gesture resolves through ops.ts into exactly one DataSource call (unit-tested there, B-31).
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { useLiveTopic } from '@/state/live';
import { cn } from '@/lib/cn';
import { dirname } from '@/lib/lang';
import { ChevronRightIcon, DotIcon, MercuryIcon, PlusIcon } from '@/ui/icons';
import { Segmented } from '@/ui/Segmented';
import type { MercuryNode, MercuryTree } from '@/types';
import { AddAxiomForm, AxiomPane, NS_NOUN } from './AxiomDialogs';
import {
  applyTreeAction,
  flattenVisible,
  neighborPath,
  resolveDrop,
  resolveKeyboardReorder,
  type DragItem,
  type DropPos,
  type SchemeNs,
} from './ops';

const msg = (e: unknown) => String((e as Error)?.message ?? e);

const DND_MIME = 'application/x-mercury';

function readDragItem(e: React.DragEvent): DragItem | null {
  const raw = e.dataTransfer.getData(DND_MIME);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as DragItem;
  } catch {
    return null;
  }
}

const NS_TABS: { value: SchemeNs; label: string }[] = [
  { value: 'axiome', label: 'Axioms' },
  { value: 'regeln', label: 'Rules' },
  { value: 'laeufe', label: 'Run rules' },
  { value: 'meta', label: 'Meta' },
];

const NS_EMPTY: Record<SchemeNs, string> = {
  axiome: 'No axioms yet. New axioms are filed into the tree automatically.',
  regeln: 'No implementation rules yet.',
  laeufe: 'No run rules yet.',
  meta: 'No meta-axioms yet. They bindingly define how an axiom must be phrased — every new axiom is checked against them.',
};

// Drag state shared across the recursive rows: what's being dragged (for dimming + the
// descendant guard). The payload also rides dataTransfer so a drop works if state is lost.
interface DnDCtx {
  drag: DragItem | null;
  setDrag: (d: DragItem | null) => void;
  onMove: (item: DragItem, targetPath: string, targetIsAxiom: boolean, pos: DropPos) => void;
}
const TreeDnD = createContext<DnDCtx | null>(null);
const useDnD = () => useContext(TreeDnD)!;

/** A context menu, opened on right-click at the cursor. */
interface MenuState {
  x: number;
  y: number;
  node: MercuryNode;
}

export function AxiomTree() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();

  const [tree, setTree] = useState<MercuryTree | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [ns, setNs] = useState<SchemeNs>('axiome');
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [drag, setDrag] = useState<DragItem | null>(null);
  const [menu, setMenu] = useState<MenuState | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null); // category path under inline rename
  // A record + text handed to the detail pane so it opens in edit pre-filled — used when the
  // user resolves a duplicate by extending or adjusting the existing record.
  const [pendingDraft, setPendingDraft] = useState<{ path: string; titel: string; body: string } | null>(null);
  // Expansion is lifted (not per-row) so keyboard navigation walks exactly the VISIBLE rows.
  const [openOverride, setOpenOverride] = useState<Record<string, boolean>>({});
  const treeRef = useRef<HTMLDivElement>(null);

  const reload = useCallback(() => {
    return source
      .mercuryTree()
      .then((t) => {
        setTree(t);
        setFailed(null);
      })
      .catch((e: unknown) => setFailed(msg(e)));
  }, [source]);

  useEffect(() => {
    void reload();
  }, [reload]);
  // The tree subscribes to the one live stream: any constitution write (another tab, the chat,
  // the AI) re-reads through the normal path (W5).
  useLiveTopic('axioms', () => void reload());

  const roots = tree ? (tree[ns] ?? []) : [];
  const isOpen = useCallback(
    (path: string) => openOverride[path] ?? path.split('/').length <= 2, // top-level categories start open
    [openOverride],
  );
  const setOpen = useCallback((path: string, open: boolean) => {
    setOpenOverride((cur) => ({ ...cur, [path]: open }));
  }, []);

  // Every gesture funnels here: resolve → apply (ONE DataSource call) → reload.
  const dispatch = useCallback(
    (item: DragItem, targetPath: string, targetIsAxiom: boolean, pos: DropPos) => {
      void (async () => {
        const action = resolveDrop(roots, ns, item, targetPath, targetIsAxiom, pos);
        if (action.op === 'none') {
          if (action.reason) toast({ title: 'Not possible', description: action.reason, variant: 'danger' });
          return;
        }
        try {
          const wrote = await applyTreeAction(source, action);
          if (wrote) {
            await reload();
            if (action.op === 'move') setSelectedPath(action.to);
          }
        } catch (e) {
          toast({ title: 'Move failed', description: msg(e), variant: 'danger' });
        }
      })();
    },
    [roots, ns, source, reload, toast],
  );

  // Keyboard: arrows walk the visible rows; Left/Right collapse/expand; Cmd/Ctrl+arrows reorder
  // within the siblings — the keyboard twin of drag & drop.
  const onTreeKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown' && e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
      e.preventDefault();
      const rows = flattenVisible(roots, isOpen);
      if ((e.metaKey || e.ctrlKey) && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
        if (!selectedPath) return;
        const action = resolveKeyboardReorder(roots, ns, selectedPath, e.key === 'ArrowUp' ? -1 : 1);
        void applyTreeAction(source, action)
          .then((wrote) => {
            if (wrote) void reload();
          })
          .catch((err: unknown) => toast({ title: 'Reorder failed', description: msg(err), variant: 'danger' }));
        return;
      }
      if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        const next = neighborPath(rows, selectedPath, e.key === 'ArrowUp' ? -1 : 1);
        if (next) {
          setPendingDraft(null);
          setSelectedPath(next);
        }
        return;
      }
      // Left/Right on a category: collapse/expand; Right on a collapsed category opens it.
      if (!selectedPath) return;
      const row = rows.find((r) => r.node.path === selectedPath);
      if (row && !row.node.isAxiom) setOpen(selectedPath, e.key === 'ArrowRight');
    },
    [roots, ns, selectedPath, isOpen, setOpen, source, reload, toast],
  );

  // The context menu closes on any click or Escape.
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && close();
    window.addEventListener('click', close);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('click', close);
      window.removeEventListener('keydown', onKey);
    };
  }, [menu]);

  const renameCategory = useCallback(
    async (from: string, to: string) => {
      const { moved } = await source.mercuryMoveCategory(from, to);
      await reload();
      return moved;
    },
    [source, reload],
  );

  if (failed) {
    // An unreachable constitution store is a distinguishable failure, never an empty tree
    // (REQ-001.3) — the server's message says which of the two outages it is.
    return (
      <div className="flex h-full min-h-0 items-center justify-center px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (!tree) {
    return <p className="px-6 py-5 text-footnote text-text-tertiary">Loading…</p>;
  }

  return (
    <TreeDnD.Provider value={{ drag, setDrag, onMove: dispatch }}>
      <div className="flex h-full min-h-0">
        <aside className="dl-scroll flex w-72 shrink-0 flex-col overflow-y-auto border-r border-separator bg-surface-sidebar [scrollbar-gutter:stable]">
          <div className="border-b border-separator p-2">
            <Segmented<SchemeNs>
              variant="tabs"
              value={ns}
              options={NS_TABS}
              onChange={(t) => {
                setNs(t);
                setSelectedPath(null);
                setAdding(false); // don't carry a half-filled add form across namespaces
                setPendingDraft(null);
              }}
            />
          </div>

          <div className="border-b border-separator px-2 py-1.5">
            {adding ? (
              <AddAxiomForm
                section={ns}
                onClose={() => setAdding(false)}
                onAdded={async (path) => {
                  setAdding(false);
                  await reload();
                  setSelectedPath(path);
                }}
                onResolveDuplicate={(path, titel, body) => {
                  // Open the EXISTING record in edit, pre-filled — the user reviews and saves;
                  // nothing is written behind their back.
                  setAdding(false);
                  setPendingDraft({ path, titel, body });
                  setSelectedPath(path);
                }}
              />
            ) : (
              <button
                type="button"
                onClick={() => setAdding(true)}
                className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-footnote text-text-secondary transition duration-fast hover:bg-fill/10 hover:text-text-primary"
              >
                <PlusIcon className="h-4 w-4 text-accent" />
                Add {NS_NOUN[ns].toLowerCase()}
              </button>
            )}
          </div>

          <NamespaceDropZone namespace={ns}>
            <div ref={treeRef} tabIndex={0} onKeyDown={onTreeKeyDown} className="outline-none" role="tree" aria-label={NS_NOUN[ns]}>
              {roots.length === 0 ? (
                <p className="px-2.5 py-3 text-caption text-text-tertiary">{NS_EMPTY[ns]}</p>
              ) : (
                roots.map((n) => (
                  <TreeRow
                    key={n.path}
                    node={n}
                    depth={0}
                    selectedPath={selectedPath}
                    isOpen={isOpen}
                    setOpen={setOpen}
                    renaming={renaming}
                    onRenameClose={() => setRenaming(null)}
                    onRename={renameCategory}
                    onSelect={(p) => {
                      setPendingDraft(null);
                      setSelectedPath(p);
                    }}
                    onContextMenu={(e, node) => {
                      e.preventDefault();
                      setMenu({ x: e.clientX, y: e.clientY, node });
                    }}
                  />
                ))
              )}
            </div>
          </NamespaceDropZone>
        </aside>

        <main className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
          {selectedPath ? (
            <AxiomPane
              key={selectedPath}
              path={selectedPath}
              categories={roots}
              namespace={ns}
              initialDraft={pendingDraft?.path === selectedPath ? { titel: pendingDraft.titel, body: pendingDraft.body } : undefined}
              onChanged={async (nextPath) => {
                setPendingDraft(null);
                await reload();
                setSelectedPath(nextPath);
              }}
            />
          ) : (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
              <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-1 ring-1 ring-separator">
                <MercuryIcon className="h-6 w-6 text-accent" />
              </span>
              <p className="text-footnote text-text-tertiary">{NS_TABS.find((t) => t.value === ns)?.label}</p>
            </div>
          )}
        </main>

        {menu && (
          <div
            className="fixed z-50 min-w-40 rounded-md border border-separator bg-surface-raised py-1 shadow-elev-2"
            style={{ left: menu.x, top: menu.y }}
            role="menu"
          >
            {menu.node.isAxiom ? (
              <>
                <MenuItem
                  label="Open"
                  onClick={() => {
                    setPendingDraft(null);
                    setSelectedPath(menu.node.path);
                  }}
                />
                <MenuItem
                  label="Delete"
                  danger
                  onClick={() => {
                    const path = menu.node.path;
                    if (!window.confirm(`Delete "${menu.node.name}"?`)) return;
                    void source
                      .mercuryDeleteAxiom(path)
                      .then(async () => {
                        if (selectedPath === path) setSelectedPath(null);
                        await reload();
                      })
                      .catch((e: unknown) => toast({ title: 'Delete failed', description: msg(e), variant: 'danger' }));
                  }}
                />
              </>
            ) : (
              <MenuItem label="Rename" onClick={() => setRenaming(menu.node.path)} />
            )}
          </div>
        )}
      </div>
    </TreeDnD.Provider>
  );
}

function MenuItem({ label, danger, onClick }: { label: string; danger?: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      className={cn(
        'block w-full px-3 py-1.5 text-left text-footnote transition duration-fast hover:bg-fill/10',
        danger ? 'text-danger' : 'text-text-primary',
      )}
    >
      {label}
    </button>
  );
}

/** One row of the category tree. Categories are collapsible to any depth and renamable (which
 *  moves every record under them); axioms are selectable leaves. */
function TreeRow({
  node,
  depth,
  selectedPath,
  isOpen,
  setOpen,
  renaming,
  onRenameClose,
  onRename,
  onSelect,
  onContextMenu,
}: {
  node: MercuryNode;
  depth: number;
  selectedPath: string | null;
  isOpen: (path: string) => boolean;
  setOpen: (path: string, open: boolean) => void;
  renaming: string | null;
  onRenameClose: () => void;
  onRename: (from: string, to: string) => Promise<number>;
  onSelect: (path: string) => void;
  onContextMenu: (e: React.MouseEvent, node: MercuryNode) => void;
}) {
  const { drag, setDrag, onMove } = useDnD();
  const [over, setOver] = useState<DropPos | null>(null);
  const pad = { paddingLeft: `${depth * 14 + 8}px` };
  const item: DragItem = { kind: node.isAxiom ? 'axiom' : 'category', path: node.path, name: node.name };
  const beingDragged = drag?.path === node.path;
  const open = isOpen(node.path);

  // Compute the drop position from the cursor's vertical place in the row: the middle band of a
  // category = drop INTO (re-nest); top/bottom half = reorder before/after. An axiom has no inside.
  const positionFor = (e: React.DragEvent): DropPos => {
    const r = e.currentTarget.getBoundingClientRect();
    const y = e.clientY - r.top;
    if (!node.isAxiom && y > r.height * 0.3 && y < r.height * 0.7) return 'inside';
    return y < r.height / 2 ? 'before' : 'after';
  };

  const dragProps = {
    draggable: true,
    onDragStart: (e: React.DragEvent) => {
      setDrag(item);
      e.dataTransfer.effectAllowed = 'move' as const;
      try {
        e.dataTransfer.setData(DND_MIME, JSON.stringify(item));
      } catch {
        /* some browsers restrict setData; state holds it too */
      }
    },
    onDragEnd: () => {
      setDrag(null);
      setOver(null);
    },
    onDragOver: (e: React.DragEvent) => {
      if (!drag || drag.path === node.path) return;
      // never drop a category into its own subtree
      if (drag.kind === 'category' && node.path.startsWith(drag.path + '/')) return;
      e.preventDefault();
      e.stopPropagation(); // this row handles the drop; don't let the namespace zone also catch it
      const pos = positionFor(e);
      setOver(pos);
      if (pos === 'inside' && !open) setOpen(node.path, true); // auto-expand during drag
    },
    onDragLeave: () => setOver(null),
    onDrop: (e: React.DragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      // Recompute the position from the drop event itself rather than the `over` state: state is
      // committed a frame late, so a fast drop would otherwise read a stale value.
      const pos = positionFor(e);
      setOver(null);
      const dropped = readDragItem(e) ?? drag;
      setDrag(null);
      if (!dropped || dropped.path === node.path) return;
      if (dropped.kind === 'category' && node.path.startsWith(dropped.path + '/')) return;
      onMove(dropped, node.path, node.isAxiom, pos);
    },
  };

  const indicator = over === 'before' || over === 'after' ? over : null;

  if (node.isAxiom) {
    const active = selectedPath === node.path;
    return (
      <div className="relative select-none" {...dragProps} onContextMenu={(e) => onContextMenu(e, node)}>
        {indicator === 'before' && <DropLine edge="top" />}
        {indicator === 'after' && <DropLine edge="bottom" />}
        <button
          type="button"
          style={pad}
          onClick={() => onSelect(node.path)}
          role="treeitem"
          aria-selected={active}
          className={cn(
            'flex w-full items-center gap-1.5 rounded-md py-1 pr-2 text-left text-footnote transition duration-fast',
            beingDragged && 'opacity-40',
            active ? 'bg-accent/15 text-text-primary' : 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
          )}
        >
          <DotIcon className={cn('h-3 w-3 shrink-0', active ? 'text-accent' : 'text-text-tertiary')} />
          <span className="truncate">{node.name}</span>
        </button>
      </div>
    );
  }

  return (
    <div>
      {renaming === node.path ? (
        <RenameCategoryRow node={node} pad={pad} onClose={onRenameClose} onRename={onRename} />
      ) : (
        <div className="relative select-none" {...dragProps} onContextMenu={(e) => onContextMenu(e, node)}>
          {indicator === 'before' && <DropLine edge="top" />}
          {indicator === 'after' && <DropLine edge="bottom" />}
          <div
            className={cn(
              'group flex items-center rounded-md',
              beingDragged && 'opacity-40',
              over === 'inside' && 'ring-2 ring-inset ring-accent',
              selectedPath === node.path && 'bg-accent/10',
            )}
            style={pad}
          >
            <button
              type="button"
              onClick={() => {
                setOpen(node.path, !open);
                onSelect(node.path); // a category is selectable too (keyboard anchor)
              }}
              role="treeitem"
              aria-selected={selectedPath === node.path}
              aria-expanded={open}
              className="flex min-w-0 flex-1 items-center gap-1 rounded-md py-1 pr-1 text-left text-footnote font-medium text-text-secondary transition duration-fast hover:bg-fill/10 hover:text-text-primary"
            >
              <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 transition-transform duration-fast', open && 'rotate-90')} />
              <span className="truncate">{node.name}</span>
            </button>
          </div>
        </div>
      )}
      {open &&
        node.children?.map((c) => (
          <TreeRow
            key={c.path}
            node={c}
            depth={depth + 1}
            selectedPath={selectedPath}
            isOpen={isOpen}
            setOpen={setOpen}
            renaming={renaming}
            onRenameClose={onRenameClose}
            onRename={onRename}
            onSelect={onSelect}
            onContextMenu={onContextMenu}
          />
        ))}
    </div>
  );
}

/** A 2px accent line at a row edge, marking a reorder drop position. */
function DropLine({ edge }: { edge: 'top' | 'bottom' }) {
  return (
    <div className={cn('pointer-events-none absolute inset-x-1 z-10 h-0.5 rounded bg-accent', edge === 'top' ? 'top-0' : 'bottom-0')} />
  );
}

/** The namespace's scroll area doubles as a drop target for the root: dropping an item on the
 *  empty space moves it to the top level. */
function NamespaceDropZone({ namespace, children }: { namespace: SchemeNs; children: React.ReactNode }) {
  const { drag, setDrag, onMove } = useDnD();
  const [over, setOver] = useState(false);
  return (
    <div
      className={cn('flex-1 p-1.5', over && 'bg-accent/5')}
      onDragOver={(e) => {
        if (!drag) return;
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        const dropped = readDragItem(e) ?? drag;
        setDrag(null);
        if (dropped && dirname(dropped.path) !== namespace) onMove(dropped, namespace, false, 'inside');
      }}
    >
      {children}
    </div>
  );
}

/** Inline rename of a category: edits the last path segment; on save every record under it
 *  moves (git tracks no empty folders — moving the leaves IS renaming the category). */
function RenameCategoryRow({
  node,
  pad,
  onClose,
  onRename,
}: {
  node: MercuryNode;
  pad: { paddingLeft: string };
  onClose: () => void;
  onRename: (from: string, to: string) => Promise<number>;
}) {
  const { toast } = useToast();
  const parent = dirname(node.path);
  const [name, setName] = useState(node.name);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    const clean = name.trim();
    if (!clean || clean === node.name || busy) return onClose();
    setBusy(true);
    try {
      const moved = await onRename(node.path, `${parent}/${clean}`);
      toast({ title: 'Category renamed', description: `${moved} records moved`, variant: 'success' });
      onClose();
    } catch (e) {
      toast({ title: 'Rename failed', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  return (
    <div className="flex items-center gap-1 py-0.5" style={pad}>
      <input
        autoFocus
        value={name}
        disabled={busy}
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => (e.key === 'Enter' ? void save() : e.key === 'Escape' ? onClose() : undefined)}
        onBlur={() => void save()}
        className="min-w-0 flex-1 rounded border border-accent/50 bg-surface px-1.5 py-0.5 text-footnote text-text-primary outline-none"
      />
    </div>
  );
}
