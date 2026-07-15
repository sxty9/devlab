import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { renderMarkdown } from '@/lib/markdown';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Splash } from '@/shell/Splash';
import { cn } from '@/lib/cn';
import { ChevronRightIcon, MercuryIcon, SitemapIcon, RocketIcon, DotIcon, PlusIcon } from '@/ui/icons';
import type { Axiom, MercuryNode, MercuryTree } from '@/types';

type SectionId = 'axiome' | 'regeln' | 'laeufe';

/** What a drag carries: an axiom leaf or a whole category, by its path + name. */
interface DragItem {
  kind: 'axiom' | 'category';
  path: string;
  name: string;
}

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

type DropPos = 'inside' | 'before' | 'after';

function parentOf(path: string): string {
  return path.slice(0, path.lastIndexOf('/'));
}

/** Find a node by path within a forest. */
function findNode(nodes: MercuryNode[], path: string): MercuryNode | null {
  for (const n of nodes) {
    if (n.path === path) return n;
    if (n.children) {
      const hit = findNode(n.children, path);
      if (hit) return hit;
    }
  }
  return null;
}

/** The children (in current display order) of the node at parentPath — or the roots when parentPath
 *  is the namespace itself. */
function childrenOf(roots: MercuryNode[], parentPath: string, namespace: string): MercuryNode[] {
  if (parentPath === namespace) return roots;
  return findNode(roots, parentPath)?.children ?? [];
}

// Drag state shared across the recursive rows: what's being dragged (for dimming + the descendant
// guard). The payload also rides dataTransfer so a drop still works if state is lost.
interface DnDCtx {
  drag: DragItem | null;
  setDrag: (d: DragItem | null) => void;
  onMove: (item: DragItem, targetPath: string, targetIsAxiom: boolean, pos: DropPos) => void;
}
const MercuryDnD = createContext<DnDCtx | null>(null);
const useDnD = () => useContext(MercuryDnD)!;

const SECTIONS: { id: SectionId; label: string; icon: (p: { className?: string }) => JSX.Element; empty: string }[] = [
  { id: 'axiome', label: 'Axiome', icon: MercuryIcon, empty: 'Noch keine Axiome. Aigentic sortiert neue Axiome automatisch ein.' },
  { id: 'regeln', label: 'Implementierungsregeln', icon: SitemapIcon, empty: 'Noch keine Implementierungsregeln.' },
  { id: 'laeufe', label: 'Automatische Läufe', icon: RocketIcon, empty: 'Automatische Läufe sind noch nicht eingerichtet.' },
];

/** Mercury — the centre for the Holistic axioms, in three parts: the axioms (aigentic auto-sorts a
 *  new one; the user re-files, renames, edits and deletes by hand), the implementation rules, and
 *  the scheduled runs. It owns no store — the tree is a projection of aigentic's scheme graveyard. */
export function MercuryView() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();

  const [tree, setTree] = useState<MercuryTree | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [section, setSection] = useState<SectionId>('axiome');
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const reload = useCallback(() => {
    return source
      .mercuryTree()
      .then((t) => setTree(t))
      .catch((e: unknown) => setFailed(String((e as Error)?.message ?? e)));
  }, [source]);

  const [drag, setDrag] = useState<DragItem | null>(null);

  // Drag-and-drop, mirroring the mail service: drop INTO a category (re-nest), or before/after a
  // sibling (reorder within the same parent, else re-nest into the target's branch).
  const roots = tree ? tree[section] ?? [] : [];

  const reNest = useCallback(
    async (item: DragItem, targetCat: string) => {
      if (item.kind === 'axiom') {
        const leaf = item.path.slice(item.path.lastIndexOf('/') + 1);
        const to = `${targetCat}/${leaf}`;
        if (to === item.path) return;
        const res = await source.mercuryMoveAxiom(item.path, to);
        await reload();
        setSelectedPath(res.path);
      } else {
        if (targetCat === item.path || targetCat.startsWith(item.path + '/')) {
          toast({ title: 'Nicht möglich', description: 'Eine Kategorie kann nicht in sich selbst verschoben werden.', variant: 'danger' });
          return;
        }
        const to = `${targetCat}/${item.name}`;
        if (to === item.path) return;
        await source.mercuryMoveCategory(item.path, to);
        await reload();
      }
    },
    [source, reload, toast],
  );

  const onMove = useCallback(
    (item: DragItem, targetPath: string, targetIsAxiom: boolean, pos: DropPos) => {
      void (async () => {
        try {
          if (pos === 'inside' && !targetIsAxiom) {
            await reNest(item, targetPath);
            return;
          }
          const targetParent = parentOf(targetPath);
          const itemParent = parentOf(item.path);
          if (targetParent === itemParent) {
            // pure reorder within the shared parent — send the full new child-name order. A child's
            // key is its path's last segment (an axiom's segment carries ".md"), matching the backend.
            const lastSeg = (p: string) => p.slice(p.lastIndexOf('/') + 1);
            const dragKey = lastSeg(item.path);
            const targetKey = lastSeg(targetPath);
            const keys = childrenOf(roots, targetParent, section)
              .map((n) => lastSeg(n.path))
              .filter((k) => k !== dragKey);
            const at = keys.indexOf(targetKey);
            if (at < 0) return;
            keys.splice(pos === 'before' ? at : at + 1, 0, dragKey);
            await source.mercuryReorder(targetParent, keys);
            await reload();
          } else {
            await reNest(item, targetParent); // cross-branch before/after → re-nest into that branch
          }
        } catch (e) {
          toast({ title: 'Verschieben fehlgeschlagen', description: String((e as Error)?.message ?? e), variant: 'danger' });
        }
      })();
    },
    [source, reload, toast, reNest, roots, section],
  );

  useEffect(() => {
    let cancelled = false;
    void reload().finally(() => cancelled);
    return () => {
      cancelled = true;
    };
  }, [reload]);

  if (failed) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (!tree) return <Splash />;

  return (
    <MercuryDnD.Provider value={{ drag, setDrag, onMove }}>
    <div className="flex min-h-0 flex-1">
      <aside className="dl-scroll flex w-72 shrink-0 flex-col overflow-y-auto border-r border-separator bg-surface-sidebar [scrollbar-gutter:stable]">
        <nav className="flex flex-col gap-0.5 border-b border-separator p-2">
          {SECTIONS.map((sec) => {
            const active = section === sec.id;
            return (
              <button
                key={sec.id}
                type="button"
                onClick={() => {
                  setSection(sec.id);
                  setSelectedPath(null);
                }}
                className={cn(
                  'flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-left text-footnote transition duration-fast',
                  active ? 'bg-fill/[0.07] font-medium text-text-primary' : 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
                )}
              >
                <sec.icon className={cn('h-4 w-4', active ? 'text-accent' : 'text-text-tertiary')} />
                {sec.label}
              </button>
            );
          })}
        </nav>

        {section === 'axiome' && (
          <div className="border-b border-separator px-2 py-1.5">
            {adding ? (
              <AddAxiomForm
                onClose={() => setAdding(false)}
                onAdded={async (path) => {
                  setAdding(false);
                  await reload();
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
                Axiom hinzufügen
              </button>
            )}
          </div>
        )}

        <NamespaceDropZone namespace={section}>
          {roots.length === 0 ? (
            <p className="px-2.5 py-3 text-caption text-text-tertiary">{SECTIONS.find((s) => s.id === section)!.empty}</p>
          ) : (
            roots.map((n) => (
              <TreeRow
                key={n.path}
                node={n}
                depth={0}
                selectedPath={selectedPath}
                onSelect={(p) => setSelectedPath(p)}
                onRenameCategory={async (from, to) => {
                  const { moved } = await source.mercuryMoveCategory(from, to);
                  await reload();
                  return moved;
                }}
              />
            ))
          )}
        </NamespaceDropZone>
      </aside>

      <main className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
        {selectedPath ? (
          <AxiomPane
            key={selectedPath}
            path={selectedPath}
            categories={roots}
            namespace={section}
            onChanged={async (nextPath) => {
              await reload();
              setSelectedPath(nextPath);
            }}
          />
        ) : (
          <Placeholder section={section} />
        )}
      </main>
    </div>
    </MercuryDnD.Provider>
  );
}

/** Add an axiom: the user gives a title and text; aigentic classifies it into the tree. Mercury
 *  never asks the user to choose a category for a NEW axiom — that is the whole point. (Re-filing an
 *  existing one by hand is what the edit surface is for.) */
function AddAxiomForm({ onClose, onAdded }: { onClose: () => void; onAdded: (path: string) => void }) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [titel, setTitel] = useState('');
  const [body, setBody] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!titel.trim() || !body.trim() || busy) return;
    setBusy(true);
    try {
      const res = await source.mercuryAddAxiom(titel.trim(), body.trim());
      toast({
        title: res.classified ? 'Axiom einsortiert' : 'Axiom abgelegt (unsortiert)',
        description: res.path,
        variant: res.classified ? 'success' : 'default',
      });
      onAdded(res.path);
    } catch (e) {
      toast({ title: 'Axiom konnte nicht angelegt werden', description: String((e as Error)?.message ?? e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-2 p-1">
      <input
        autoFocus
        value={titel}
        onChange={(e) => setTitel(e.target.value)}
        placeholder="Titel"
        className="rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
      />
      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder="Das Axiom, atomar und knapp formuliert."
        rows={4}
        className="dl-scroll resize-none rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
      />
      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={busy || !titel.trim() || !body.trim()} onClick={submit}>
          {busy ? 'Sortiere ein…' : 'Einsortieren'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={onClose}>
          Abbrechen
        </Button>
      </div>
    </div>
  );
}

/** One row of the category tree. Categories are collapsible to any depth and renamable (which moves
 *  every record under them); axioms are selectable leaves. Expansion is local state. */
function TreeRow({
  node,
  depth,
  selectedPath,
  onSelect,
  onRenameCategory,
}: {
  node: MercuryNode;
  depth: number;
  selectedPath: string | null;
  onSelect: (path: string) => void;
  onRenameCategory: (from: string, to: string) => Promise<number>;
}) {
  const { drag, setDrag, onMove } = useDnD();
  const [open, setOpen] = useState(depth < 1);
  const [renaming, setRenaming] = useState(false);
  const [over, setOver] = useState<DropPos | null>(null);
  const pad = { paddingLeft: `${depth * 14 + 8}px` };
  const item: DragItem = { kind: node.isAxiom ? 'axiom' : 'category', path: node.path, name: node.name };
  const beingDragged = drag?.path === node.path;

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
      e.dataTransfer.effectAllowed = 'move';
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
      if (pos === 'inside' && !open) setOpen(true); // auto-expand a collapsed category during drag
    },
    onDragLeave: () => setOver(null),
    onDrop: (e: React.DragEvent) => {
      e.preventDefault();
      e.stopPropagation(); // handled here — the namespace-root drop zone must not re-handle it
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
      <div className="relative select-none" {...dragProps}>
        {indicator === 'before' && <DropLine edge="top" />}
        {indicator === 'after' && <DropLine edge="bottom" />}
        <button
          type="button"
          style={pad}
          onClick={() => onSelect(node.path)}
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
      {renaming ? (
        <RenameCategoryRow node={node} pad={pad} onClose={() => setRenaming(false)} onRename={onRenameCategory} />
      ) : (
        <div className="relative select-none" {...dragProps}>
          {indicator === 'before' && <DropLine edge="top" />}
          {indicator === 'after' && <DropLine edge="bottom" />}
          <div
            className={cn(
              'group flex items-center rounded-md',
              beingDragged && 'opacity-40',
              over === 'inside' && 'ring-2 ring-inset ring-accent',
            )}
            style={pad}
          >
            <button
              type="button"
              onClick={() => setOpen((o) => !o)}
              className="flex min-w-0 flex-1 items-center gap-1 rounded-md py-1 pr-1 text-left text-footnote font-medium text-text-secondary transition duration-fast hover:bg-fill/10 hover:text-text-primary"
            >
              <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 transition-transform duration-fast', open && 'rotate-90')} />
              <span className="truncate">{node.name}</span>
            </button>
            <button
              type="button"
              onClick={() => setRenaming(true)}
              title="Kategorie umbenennen"
              className="mr-1 shrink-0 rounded px-1 text-caption text-text-tertiary opacity-0 transition group-hover:opacity-100 hover:text-text-primary"
            >
              umbenennen
            </button>
          </div>
        </div>
      )}
      {open && node.children?.map((c) => (
        <TreeRow key={c.path} node={c} depth={depth + 1} selectedPath={selectedPath} onSelect={onSelect} onRenameCategory={onRenameCategory} />
      ))}
    </div>
  );
}

/** A 2px accent line at a row edge, marking a reorder drop position. */
function DropLine({ edge }: { edge: 'top' | 'bottom' }) {
  return <div className={cn('pointer-events-none absolute inset-x-1 z-10 h-0.5 rounded bg-accent', edge === 'top' ? 'top-0' : 'bottom-0')} />;
}

/** The section's scroll area doubles as a drop target for the namespace root: dropping an item on
 *  the empty space moves it to the top level. */
function NamespaceDropZone({ namespace, children }: { namespace: SectionId; children: React.ReactNode }) {
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
        if (dropped && parentOf(dropped.path) !== namespace) onMove(dropped, namespace, false, 'inside');
      }}
    >
      {children}
    </div>
  );
}

/** Inline rename of a category: edits the last path segment; on save every record under it moves. */
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
  const parent = node.path.slice(0, node.path.lastIndexOf('/'));
  const [name, setName] = useState(node.name);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    const clean = name.trim();
    if (!clean || clean === node.name || busy) return onClose();
    setBusy(true);
    try {
      const moved = await onRename(node.path, `${parent}/${clean}`);
      toast({ title: 'Kategorie umbenannt', description: `${moved} Axiome verschoben`, variant: 'success' });
      onClose();
    } catch (e) {
      toast({ title: 'Umbenennen fehlgeschlagen', description: String((e as Error)?.message ?? e), variant: 'danger' });
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
        onKeyDown={(e) => (e.key === 'Enter' ? save() : e.key === 'Escape' ? onClose() : undefined)}
        onBlur={save}
        className="min-w-0 flex-1 rounded border border-accent/50 bg-surface px-1.5 py-0.5 text-footnote text-text-primary outline-none"
      />
    </div>
  );
}

/** The reading pane for a selected axiom: view, edit, move/rename or delete. `key={path}` remounts
 *  it on selection change so its local state resets cleanly. */
function AxiomPane({
  path,
  categories,
  namespace,
  onChanged,
}: {
  path: string;
  categories: MercuryNode[];
  namespace: string;
  onChanged: (nextPath: string | null) => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [axiom, setAxiom] = useState<Axiom | null>(null);
  const [err, setErr] = useState(false);
  const [mode, setMode] = useState<'view' | 'edit' | 'move'>('view');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setAxiom(null);
    setErr(false);
    source
      .mercuryItem(path)
      .then((a) => !cancelled && setAxiom(a))
      .catch(() => !cancelled && setErr(true));
    return () => {
      cancelled = true;
    };
  }, [source, path]);

  const del = async () => {
    if (busy || !window.confirm(`Axiom „${axiom?.titel ?? path}“ löschen?`)) return;
    setBusy(true);
    try {
      await source.mercuryDeleteAxiom(path);
      toast({ title: 'Axiom gelöscht', variant: 'default' });
      onChanged(null);
    } catch (e) {
      toast({ title: 'Löschen fehlgeschlagen', description: String((e as Error)?.message ?? e), variant: 'danger' });
      setBusy(false);
    }
  };

  if (err) return <p className="px-8 py-7 text-footnote text-text-secondary">Dieses Axiom konnte nicht geladen werden.</p>;
  if (!axiom) return <p className="px-8 py-7 text-footnote text-text-tertiary">Lädt…</p>;

  if (mode === 'edit') {
    return (
      <EditAxiomForm
        axiom={axiom}
        onCancel={() => setMode('view')}
        onSave={async (titel, body) => {
          const res = await source.mercuryEditAxiom(path, titel, body);
          // An edit keeps the path, so this pane never remounts and its item fetch never re-runs;
          // update the local state from the server's response so the reading view reflects the edit
          // immediately, without a page reload.
          setAxiom(res.axiom);
          toast({ title: 'Axiom gespeichert', variant: 'success' });
          setMode('view');
        }}
      />
    );
  }

  if (mode === 'move') {
    return (
      <MoveAxiomForm
        path={path}
        categories={categories}
        namespace={namespace}
        onCancel={() => setMode('view')}
        onMove={async (to) => {
          const res = await source.mercuryMoveAxiom(path, to);
          toast({ title: 'Axiom verschoben', description: res.path, variant: 'success' });
          onChanged(res.path);
        }}
      />
    );
  }

  return (
    <article className="mx-auto max-w-3xl px-8 py-7">
      <div className="flex items-start justify-between gap-4">
        <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{axiom.titel || path.split('/').pop()}</h1>
        <div className="flex shrink-0 items-center gap-1.5">
          <Button variant="secondary" size="sm" onClick={() => setMode('edit')}>
            Bearbeiten
          </Button>
          <Button variant="secondary" size="sm" onClick={() => setMode('move')}>
            Verschieben
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={del}>
            Löschen
          </Button>
        </div>
      </div>
      <p className="mt-1 font-mono text-caption text-text-tertiary">{path}</p>
      <div className="dl-markdown mt-5" dangerouslySetInnerHTML={{ __html: renderMarkdown(axiom.body) }} />
      {axiom.quelle && (
        <p className="mt-8 border-t border-separator pt-3 text-caption text-text-tertiary">
          Quelle: <span className="font-mono">{axiom.quelle}</span>
        </p>
      )}
    </article>
  );
}

/** Edit an axiom's title and body. Its id, quelle and path are preserved. */
function EditAxiomForm({ axiom, onCancel, onSave }: { axiom: Axiom; onCancel: () => void; onSave: (titel: string, body: string) => Promise<void> }) {
  const { toast } = useToast();
  const [titel, setTitel] = useState(axiom.titel);
  const [body, setBody] = useState(axiom.body);
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!titel.trim() || !body.trim() || busy) return;
    setBusy(true);
    try {
      await onSave(titel.trim(), body.trim());
    } catch (e) {
      toast({ title: 'Speichern fehlgeschlagen', description: String((e as Error)?.message ?? e), variant: 'danger' });
      setBusy(false);
    }
  };

  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-3 px-8 py-7">
      <input
        value={titel}
        onChange={(e) => setTitel(e.target.value)}
        placeholder="Titel"
        className="rounded-md border border-separator bg-surface px-3 py-2 text-title3 font-semibold text-text-primary outline-none focus:border-accent/50"
      />
      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        rows={10}
        className="dl-scroll resize-y rounded-md border border-separator bg-surface px-3 py-2 text-body text-text-primary outline-none focus:border-accent/50"
      />
      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={busy || !titel.trim() || !body.trim()} onClick={save}>
          {busy ? 'Speichert…' : 'Speichern'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={onCancel}>
          Abbrechen
        </Button>
      </div>
    </div>
  );
}

/** Slugify a user-typed name to a path segment (mirrors the backend's rule). */
function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/ä/g, 'ae').replace(/ö/g, 'oe').replace(/ü/g, 'ue').replace(/ß/g, 'ss')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

/** Move or rename an axiom by BROWSING to a target category and naming the leaf — no path typing.
 *  New categories are created on the fly (scheme has no empty folders, so the category springs into
 *  existence when the axiom lands under it). */
function MoveAxiomForm({
  path,
  categories,
  namespace,
  onCancel,
  onMove,
}: {
  path: string;
  categories: MercuryNode[];
  namespace: string;
  onCancel: () => void;
  onMove: (to: string) => Promise<void>;
}) {
  const { toast } = useToast();
  const currentCat = path.slice(0, path.lastIndexOf('/'));
  const currentSlug = path.slice(path.lastIndexOf('/') + 1).replace(/\.md$/, '');
  const [target, setTarget] = useState(currentCat); // selected category path
  const [name, setName] = useState(currentSlug);
  const [busy, setBusy] = useState(false);

  const slug = slugify(name) || currentSlug;
  const to = `${target}/${slug}.md`;
  const unchanged = to === path;

  const move = async () => {
    if (busy || unchanged) return onCancel();
    setBusy(true);
    try {
      await onMove(to);
    } catch (e) {
      toast({ title: 'Verschieben fehlgeschlagen', description: String((e as Error)?.message ?? e), variant: 'danger' });
      setBusy(false);
    }
  };

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-4 px-8 py-7">
      <h1 className="text-title3 font-semibold tracking-tight text-text-primary">Verschieben / Umbenennen</h1>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Zielkategorie</p>
        <div className="dl-scroll max-h-72 overflow-y-auto rounded-card border border-separator bg-surface p-1.5">
          <CategoryPicker nodes={categories} namespace={namespace} selected={target} onSelect={setTarget} />
        </div>
      </div>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Name</p>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && move()}
          spellCheck={false}
          className="w-full rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50"
        />
      </div>

      <p className="font-mono text-caption text-text-tertiary">
        Ziel: <span className="text-text-secondary">{to}</span>
      </p>

      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={busy || unchanged} onClick={move}>
          {busy ? 'Verschiebt…' : 'Verschieben'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={onCancel}>
          Abbrechen
        </Button>
      </div>
    </div>
  );
}

/** A browsable category tree for the move dialog: select an existing category, or create a new
 *  subcategory anywhere. Leaves (axioms) are not shown — only the folder structure. */
function CategoryPicker({
  nodes,
  namespace,
  selected,
  onSelect,
  depth = 0,
}: {
  nodes: MercuryNode[];
  namespace: string;
  selected: string;
  onSelect: (path: string) => void;
  depth?: number;
}) {
  const cats = nodes.filter((n) => !n.isAxiom);

  return (
    <>
      {depth === 0 && (
        <CategoryRow
          label={namespace}
          path={namespace}
          depth={0}
          selected={selected}
          onSelect={onSelect}
          onCreate={(name) => onSelect(`${namespace}/${slugify(name)}`)}
        />
      )}
      {cats.map((c) => (
        <div key={c.path}>
          <CategoryRow
            label={c.name}
            path={c.path}
            depth={depth + 1}
            selected={selected}
            onSelect={onSelect}
            onCreate={(name) => onSelect(`${c.path}/${slugify(name)}`)}
          />
          {c.children && <CategoryPicker nodes={c.children} namespace={namespace} selected={selected} onSelect={onSelect} depth={depth + 1} />}
        </div>
      ))}
    </>
  );
}

/** One selectable category row in the picker, with a "+ Unterkategorie" affordance. */
function CategoryRow({
  label,
  path,
  depth,
  selected,
  onSelect,
  onCreate,
}: {
  label: string;
  path: string;
  depth: number;
  selected: string;
  onSelect: (path: string) => void;
  onCreate: (name: string) => void;
}) {
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const active = selected === path || selected.startsWith(path + '/');
  const exact = selected === path;

  return (
    <div>
      <div className="group flex items-center gap-1" style={{ paddingLeft: `${depth * 14}px` }}>
        <button
          type="button"
          onClick={() => onSelect(path)}
          className={cn(
            'flex min-w-0 flex-1 items-center gap-1.5 rounded-md px-2 py-1 text-left text-footnote transition duration-fast',
            exact ? 'bg-accent/15 font-medium text-text-primary' : active ? 'text-text-primary' : 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
          )}
        >
          <SitemapIcon className={cn('h-3.5 w-3.5 shrink-0', exact ? 'text-accent' : 'text-text-tertiary')} />
          <span className="truncate">{label}</span>
        </button>
        <button
          type="button"
          title="Unterkategorie anlegen"
          onClick={() => setCreating(true)}
          className="mr-1 shrink-0 rounded px-1 text-text-tertiary opacity-0 transition group-hover:opacity-100 hover:text-accent"
        >
          <PlusIcon className="h-3.5 w-3.5" />
        </button>
      </div>
      {creating && (
        <div className="flex items-center py-0.5" style={{ paddingLeft: `${(depth + 1) * 14 + 8}px` }}>
          <input
            autoFocus
            value={name}
            placeholder="neue Unterkategorie"
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && slugify(name)) {
                onCreate(name);
                setCreating(false);
                setName('');
              } else if (e.key === 'Escape') {
                setCreating(false);
                setName('');
              }
            }}
            onBlur={() => {
              if (slugify(name)) onCreate(name);
              setCreating(false);
              setName('');
            }}
            className="min-w-0 flex-1 rounded border border-accent/50 bg-surface px-1.5 py-0.5 text-caption text-text-primary outline-none"
          />
        </div>
      )}
    </div>
  );
}

/** Shown when nothing is selected in a section. */
function Placeholder({ section }: { section: SectionId }) {
  const sec = SECTIONS.find((s) => s.id === section)!;
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
      <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-1 ring-1 ring-separator">
        <sec.icon className="h-6 w-6 text-accent" />
      </span>
      <p className="text-footnote text-text-tertiary">{sec.label}</p>
    </div>
  );
}
