import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { MercuryLiveProvider } from '@/state/mercuryLive';
import { renderMarkdown } from '@/lib/markdown';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Splash } from '@/shell/Splash';
import RunsView from './RunsView';
import TodosView from './TodosView';
import GlobalCalendarView from './GlobalCalendarView';
import MercuryChat from './MercuryChat';
import { fmtDateTime } from './MercuryExecutions';
import { cn } from '@/lib/cn';
import { ChevronRightIcon, MercuryIcon, SitemapIcon, RocketIcon, DotIcon, PlusIcon, CheckIcon } from '@/ui/icons';
import type { Axiom, Conformance, MercuryNode, MercuryTree, MetaViolation, RolloutReport } from '@/types';

type SectionId = 'axiome' | 'regeln' | 'laeufe' | 'todos' | 'kalender';

// The scheme-backed namespaces (each is a tree). `meta` (Meta-Axiome) lives as a sub-tab under the
// Axiome section rather than as its own sidebar entry, so it is a namespace but not a SectionId.
type SchemeNs = 'axiome' | 'regeln' | 'laeufe' | 'meta';

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
  { id: 'laeufe', label: 'Automatische Läufe', icon: RocketIcon, empty: 'Noch keine Laufregeln.' },
  // Konkrete ToDos: one-time, manually planned tasks (ad-hoc fix, new service). Same machinery as the
  // automatic runs, but no Laufregeln and no recurrence — it owns no scheme namespace, so no tree.
  { id: 'todos', label: 'Konkrete ToDos', icon: CheckIcon, empty: 'Noch keine ToDos.' },
  // The global calendar unites the automatic runs and the ToDos (colour-separated); like todos it is
  // not scheme-backed, so it owns its pane and has no tree.
  { id: 'kalender', label: 'Kalender', icon: RocketIcon, empty: '' },
];

// Adding is symmetric across the scheme-backed namespaces — same form, only the namespace differs.
// (`todos`/`kalender` have no scheme namespace; those views own their own flows.)
const ADD_NOUN: Record<SchemeNs, string> = { axiome: 'Axiom', regeln: 'Regel', laeufe: 'Laufregel', meta: 'Meta-Axiom' };
const ADD_PLACEHOLDER: Record<SchemeNs, string> = {
  axiome: 'Das Axiom, atomar und knapp formuliert.',
  regeln: 'Die Implementierungsregel, knapp formuliert.',
  laeufe: 'Der automatisierte Lauf, knapp beschrieben.',
  meta: 'Die verbindliche Anforderung an Axiome, präzise formuliert.',
};
// Empty-state text per scheme namespace (the sidebar SECTIONS.empty covers the section level; meta
// lives under Axiome as a sub-tab, so it needs its own).
const NS_EMPTY: Record<SchemeNs, string> = {
  axiome: 'Noch keine Axiome. Aigentic sortiert neue Axiome automatisch ein.',
  regeln: 'Noch keine Implementierungsregeln.',
  laeufe: 'Noch keine Laufregeln.',
  meta: 'Noch keine Meta-Axiome. Sie legen verbindlich fest, wie ein Axiom formuliert sein muss — jedes neue Axiom wird streng dagegen geprüft.',
};

/** Mercury — the centre for the Holistic axioms, in three parts: the axioms (aigentic auto-sorts a
 *  new one; the user re-files, renames, edits and deletes by hand), the implementation rules, and
 *  the scheduled runs. The tree is a projection of the constitution's own Git repository, served
 *  through DevLab's single /api/mercury access point. */
/** MercuryView wraps the whole surface in the ONE live-update provider so the axiom tree, runs, ToDos and
 *  executions refresh themselves without polling. Mounted here (inside the tab view) → the stream opens on
 *  entering Mercury and closes on leaving, so a closed surface causes no ongoing load. */
export function MercuryView() {
  return (
    <MercuryLiveProvider>
      <MercuryViewInner />
    </MercuryLiveProvider>
  );
}

function MercuryViewInner() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();

  const [tree, setTree] = useState<MercuryTree | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [section, setSection] = useState<SectionId>('axiome');
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  // The "Automatische Läufe" section holds two things: the scheduled run instances (Läufe) and the
  // global rules that govern them (Laufregeln, the laeufe/ records). A sub-tab switches between them.
  const [laeufeTab, setLaeufeTab] = useState<'laeufe' | 'laufregeln'>('laeufe');
  // The "Axiome" section holds two things: the axioms themselves and the Meta-Axiome that bindingly
  // govern how an axiom must be formulated. A sub-tab switches the tree's namespace between them.
  const [axiomeTab, setAxiomeTab] = useState<'axiome' | 'meta'>('axiome');
  // The KI-Chat acts for ALL of Mercury, so it lives here rather than inside a section. Its toggle and
  // its panel share the right edge (MercuryChat renders a collapsed tab when closed); kept mounted so
  // the conversation survives closing.
  const [chatOpen, setChatOpen] = useState(false);
  // A record + text handed to the detail pane so it opens in edit pre-filled — used when the user
  // resolves a duplicate by extending or adjusting the existing record.
  const [pendingDraft, setPendingDraft] = useState<{ path: string; titel: string; body: string } | null>(null);

  const reload = useCallback(() => {
    return source
      .mercuryTree()
      .then((t) => setTree(t))
      .catch((e: unknown) => setFailed(String((e as Error)?.message ?? e)));
  }, [source]);

  const [drag, setDrag] = useState<DragItem | null>(null);

  // The scheme namespace currently in view: within Axiome the sub-tab picks axiome vs meta; the other
  // scheme-backed sections map straight to their namespace. (todos/kalender aren't scheme-backed.)
  const activeNs: SchemeNs = section === 'axiome' ? axiomeTab : (section as SchemeNs);

  // Drag-and-drop, mirroring the mail service: drop INTO a category (re-nest), or before/after a
  // sibling (reorder within the same parent, else re-nest into the target's branch).
  // `todos` is not scheme-backed (it has no namespace/tree) — TodosView owns that section entirely.
  const roots = tree && section !== 'todos' && section !== 'kalender' ? tree[activeNs] ?? [] : [];

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

  // In the Läufe sub-tab the run instances take over the pane; the sidebar tree (Laufregeln) and its
  // add form only apply to the Laufregeln sub-tab (and the other scheme-backed sections). The ToDos
  // section is likewise fully owned by its own view.
  const runsMode = section === 'laeufe' && laeufeTab === 'laeufe';
  const todosMode = section === 'todos';
  const calendarMode = section === 'kalender';
  const treeMode = !runsMode && !todosMode && !calendarMode;

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
                  setAdding(false); // don't carry a half-filled add form across sections
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

        {section === 'laeufe' && (
          <div className="flex gap-1 border-b border-separator p-2">
            {(['laufregeln', 'laeufe'] as const).map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => {
                  setLaeufeTab(t);
                  setSelectedPath(null);
                  setAdding(false);
                }}
                className={cn(
                  'flex-1 rounded-md px-2 py-1 text-center text-caption transition duration-fast',
                  laeufeTab === t ? 'bg-fill/[0.07] font-medium text-text-primary' : 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
                )}
              >
                {t === 'laeufe' ? 'Läufe' : 'Laufregeln'}
              </button>
            ))}
          </div>
        )}

        {section === 'axiome' && (
          <div className="flex gap-1 border-b border-separator p-2">
            {(['axiome', 'meta'] as const).map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => {
                  setAxiomeTab(t);
                  setSelectedPath(null);
                  setAdding(false);
                }}
                className={cn(
                  'flex-1 rounded-md px-2 py-1 text-center text-caption transition duration-fast',
                  axiomeTab === t ? 'bg-fill/[0.07] font-medium text-text-primary' : 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
                )}
              >
                {t === 'axiome' ? 'Axiome' : 'Meta-Axiome'}
              </button>
            ))}
          </div>
        )}

        {treeMode && (
          <>
            <div className="border-b border-separator px-2 py-1.5">
              {adding ? (
                <AddAxiomForm
                  section={activeNs}
                  onClose={() => setAdding(false)}
                  onAdded={async (path) => {
                    setAdding(false);
                    await reload();
                    setSelectedPath(path);
                  }}
                  onResolveDuplicate={(path, titel, body) => {
                    // Open the EXISTING record in edit, pre-filled (extended or as-is) — the user
                    // reviews and saves; nothing is written behind their back.
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
                  {ADD_NOUN[activeNs]} hinzufügen
                </button>
              )}
            </div>

            <NamespaceDropZone namespace={activeNs}>
              {roots.length === 0 ? (
                <p className="px-2.5 py-3 text-caption text-text-tertiary">{NS_EMPTY[activeNs]}</p>
              ) : (
                roots.map((n) => (
                  <TreeRow
                    key={n.path}
                    node={n}
                    depth={0}
                    selectedPath={selectedPath}
                    onSelect={(p) => {
                      setPendingDraft(null);
                      setSelectedPath(p);
                    }}
                    onRenameCategory={async (from, to) => {
                      const { moved } = await source.mercuryMoveCategory(from, to);
                      await reload();
                      return moved;
                    }}
                  />
                ))
              )}
            </NamespaceDropZone>
            {(activeNs === 'axiome' || activeNs === 'regeln') && <RolloutStatus refresh={tree} />}
          </>
        )}
      </aside>

      <main className={cn('min-h-0 flex-1 bg-bg-base', runsMode || todosMode || calendarMode ? 'flex' : 'dl-scroll overflow-y-auto')}>
        {calendarMode ? (
          <GlobalCalendarView />
        ) : todosMode ? (
          <TodosView />
        ) : runsMode ? (
          <RunsView />
        ) : selectedPath ? (
          <AxiomPane
            key={selectedPath}
            path={selectedPath}
            categories={roots}
            namespace={activeNs}
            initialDraft={pendingDraft?.path === selectedPath ? { titel: pendingDraft.titel, body: pendingDraft.body } : undefined}
            onChanged={async (nextPath) => {
              setPendingDraft(null);
              await reload();
              setSelectedPath(nextPath);
            }}
          />
        ) : (
          <Placeholder section={section} label={activeNs === 'meta' ? 'Meta-Axiome' : undefined} />
        )}
      </main>

      <MercuryChat open={chatOpen} onOpenChange={setChatOpen} onApplied={() => void reload()} />
    </div>
    </MercuryDnD.Provider>
  );
}

/** capNames renders a repo-id list portioned: up to `n` names, then a "+K weitere" tail — a digestible
 *  summary, never a raw dump. */
function capNames(names: string[], n: number): string {
  if (names.length <= n) return names.join(', ');
  return names.slice(0, n).join(', ') + ` +${names.length - n} weitere`;
}

/** The last automatic CLAUDE.md rollout, shown under the Axiome/Implementierungsregeln tree (the two
 *  namespaces the rollout carries). A write to either triggers a debounced background rollout; this is
 *  its portioned outcome — when it ran, what changed, what was skipped — not a raw log. `refresh` re-reads
 *  it whenever the tree reloads after an edit. */
function RolloutStatus({ refresh }: { refresh: unknown }) {
  const source = useMemo(() => getDataSource(), []);
  const [last, setLast] = useState<RolloutReport | null>(null);
  const [loaded, setLoaded] = useState(false);
  useEffect(() => {
    let cancelled = false;
    source
      .mercuryRolloutStatus()
      .then((r) => {
        if (cancelled) return;
        setLast(r.last);
        setLoaded(true);
      })
      .catch(() => !cancelled && setLoaded(true));
    return () => {
      cancelled = true;
    };
  }, [source, refresh]);

  if (!loaded) return null;

  return (
    <div className="border-t border-separator px-2.5 py-2 text-caption text-text-tertiary">
      <div className="mb-1 flex items-center gap-1.5 font-medium uppercase tracking-wide">
        <RocketIcon className="h-3.5 w-3.5" /> CLAUDE.md-Rollout
      </div>
      {!last ? (
        <p>Noch nicht ausgerollt.</p>
      ) : last.error ? (
        <p className="text-warning">Fehlgeschlagen: {last.error}</p>
      ) : (
        <>
          <p>
            {fmtDateTime(last.at)} · {last.changed.length} geändert
            {last.unchanged > 0 ? ` · ${last.unchanged} unverändert` : ''}
          </p>
          {last.changed.length > 0 && <p className="text-text-secondary">{capNames(last.changed, 6)}</p>}
          {last.skipped && last.skipped.length > 0 && (
            <p className="text-warning">übersprungen: {last.skipped.map((s) => `${s.repo} (${s.reason})`).join('; ')}</p>
          )}
        </>
      )}
    </div>
  );
}

/** Add a record to the active section: the user gives a title and text; aigentic classifies it into
 *  that namespace's tree. The same form serves Axiome, Implementierungsregeln and Automatische Läufe
 *  — a symmetric design. Mercury never asks the user to choose a category for a NEW record — that is
 *  the whole point. (Re-filing an existing one by hand is what the edit surface is for.) */
function AddAxiomForm({
  section,
  onClose,
  onAdded,
  onResolveDuplicate,
}: {
  section: SchemeNs;
  onClose: () => void;
  onAdded: (path: string) => void;
  /** The user resolved a duplicate: open THIS record in the editor pre-filled with this text. */
  onResolveDuplicate: (path: string, titel: string, body: string) => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [titel, setTitel] = useState('');
  const [body, setBody] = useState('');
  const [busy, setBusy] = useState(false);
  // Set when the classifier reports an existing record with the same content: nothing was written,
  // and the user chooses — extend it, adjust it, or create a new one anyway.
  const [dup, setDup] = useState<{ path: string; axiom: Axiom; proposedTitel: string; proposedBody: string } | null>(null);
  // Set when an axiom violates a meta-axiom: nothing was written, and the user chooses — take the AI
  // correction, adjust it by hand, or create it regardless.
  const [nonconform, setNonconform] = useState<{ violations: MetaViolation[]; proposedTitel: string; proposedBody: string } | null>(null);
  const noun = ADD_NOUN[section];

  const submit = async (force = false) => {
    if (!titel.trim() || !body.trim() || busy) return;
    setBusy(true);
    try {
      // Initiale KI-Optimierung (Orthografie, Generalisierung, Kürzung) VOR dem Einsortieren.
      const opt = await source.mercuryOptimize(titel.trim(), body.trim(), section);
      const res = await source.mercuryAddAxiom(opt.titel, opt.body, section, force);
      if (res.duplicate) {
        setDup({
          path: res.duplicate.path,
          axiom: res.duplicate.axiom,
          proposedTitel: res.proposed?.titel ?? opt.titel,
          proposedBody: res.proposed?.body ?? opt.body,
        });
        return; // nothing written — the choice is the user's
      }
      if (res.nonconform) {
        setNonconform({
          violations: res.nonconform.violations,
          proposedTitel: res.nonconform.proposed.titel,
          proposedBody: res.nonconform.proposed.body,
        });
        return; // nothing written — a meta-axiom is violated; the user decides
      }
      toast({
        title: res.classified ? `${noun} optimiert & einsortiert` : `${noun} optimiert & abgelegt (unsortiert)`,
        description: res.path,
        variant: res.classified ? 'success' : 'default',
      });
      if (res.path) onAdded(res.path);
    } catch (e) {
      toast({ title: `${noun} konnte nicht angelegt werden`, description: String((e as Error)?.message ?? e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  // The duplicate fork: extend the existing record (its text + the new text, to be tidied), adjust it
  // as it stands, or file the new one regardless.
  if (dup) {
    return (
      <div className="flex flex-col gap-2 p-1">
        <p className="text-footnote font-medium text-text-primary">Ähnlicher Eintrag existiert bereits</p>
        <div className="rounded-md border border-separator bg-surface px-2.5 py-2">
          <p className="text-footnote text-text-primary">{dup.axiom.titel}</p>
          <p className="mt-0.5 font-mono text-caption text-text-tertiary">{dup.path}</p>
          <p className="mt-1 line-clamp-3 text-caption text-text-secondary">{dup.axiom.body}</p>
        </div>
        <p className="text-caption text-text-tertiary">Nichts wurde gespeichert. Was soll passieren?</p>
        <div className="flex flex-col gap-1.5">
          <Button
            variant="primary"
            size="sm"
            onClick={() => onResolveDuplicate(dup.path, dup.axiom.titel, `${dup.axiom.body}\n\n${dup.proposedBody}`)}
          >
            Vorhandenes ausweiten
          </Button>
          <Button variant="secondary" size="sm" onClick={() => onResolveDuplicate(dup.path, dup.axiom.titel, dup.axiom.body)}>
            Vorhandenes anpassen
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={() => void submit(true)}>
            {busy ? 'Lege an…' : 'Trotzdem neu anlegen'}
          </Button>
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => setDup(null)}>
            Zurück
          </Button>
        </div>
      </div>
    );
  }

  // The conformance fork: the axiom breaks a binding meta-axiom. Take the AI-corrected version
  // (seeded into the form, re-submitted), adjust it by hand, or write it anyway.
  if (nonconform) {
    return (
      <div className="flex flex-col gap-2 p-1">
        <p className="text-footnote font-medium text-text-primary">Verstößt gegen Meta-Axiome</p>
        <ul className="flex flex-col gap-1.5">
          {nonconform.violations.map((v, i) => (
            <li key={i} className="rounded-md border border-warning/30 bg-warning/[0.06] px-2.5 py-1.5">
              <p className="text-caption font-medium text-warning">{v.meta}</p>
              <p className="mt-0.5 text-caption text-text-secondary">{v.issue}</p>
            </li>
          ))}
        </ul>
        <div className="rounded-md border border-separator bg-surface px-2.5 py-2">
          <p className="text-caption uppercase tracking-wide text-text-tertiary">KI-Korrektur</p>
          <p className="mt-0.5 text-footnote text-text-primary">{nonconform.proposedTitel}</p>
          <p className="mt-1 line-clamp-4 text-caption text-text-secondary">{nonconform.proposedBody}</p>
        </div>
        <p className="text-caption text-text-tertiary">Nichts wurde gespeichert. Was soll passieren?</p>
        <div className="flex flex-col gap-1.5">
          <Button
            variant="primary"
            size="sm"
            disabled={busy}
            onClick={() => {
              setTitel(nonconform.proposedTitel);
              setBody(nonconform.proposedBody);
              setNonconform(null);
              void submit();
            }}
          >
            {busy ? 'Lege an…' : 'KI-Korrektur übernehmen'}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() => {
              setTitel(nonconform.proposedTitel);
              setBody(nonconform.proposedBody);
              setNonconform(null);
            }}
          >
            Manuell anpassen
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={() => void submit(true)}>
            {busy ? 'Lege an…' : 'Trotzdem anlegen'}
          </Button>
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => setNonconform(null)}>
            Zurück
          </Button>
        </div>
      </div>
    );
  }

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
        placeholder={ADD_PLACEHOLDER[section]}
        rows={4}
        className="dl-scroll resize-none rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
      />
      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={busy || !titel.trim() || !body.trim()} onClick={() => void submit()}>
          {busy ? 'Optimiere & sortiere…' : 'Einsortieren'}
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
  initialDraft,
  onChanged,
}: {
  path: string;
  categories: MercuryNode[];
  namespace: SchemeNs;
  /** Pre-fill the editor and open it straight away (duplicate resolution / AI optimization). */
  initialDraft?: { titel: string; body: string };
  onChanged: (nextPath: string | null) => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [axiom, setAxiom] = useState<Axiom | null>(null);
  const [err, setErr] = useState(false);
  const [mode, setMode] = useState<'view' | 'edit' | 'move'>(initialDraft ? 'edit' : 'view');
  const [busy, setBusy] = useState(false);
  const [optimizing, setOptimizing] = useState(false);
  // An AI optimization (or a duplicate resolution) is never applied silently: it pre-fills the edit
  // form so the user reviews and saves — cancelling leaves the stored record untouched.
  const [draft, setDraft] = useState<{ titel: string; body: string } | null>(initialDraft ?? null);
  // Conformance against the meta-axioms — checked on demand (an aigentic call), never automatically.
  const [conf, setConf] = useState<Conformance | null>(null);
  const [checking, setChecking] = useState(false);

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

  // "Mit KI optimieren" on an EXISTING record: polish title+body, then open the editor pre-filled so
  // the user reviews the change before it is stored.
  const optimize = async () => {
    if (optimizing || busy || !axiom) return;
    setOptimizing(true);
    try {
      const opt = await source.mercuryOptimize(axiom.titel, axiom.body, namespace);
      setDraft({ titel: opt.titel, body: opt.body });
      setMode('edit');
      toast({ title: 'Mit KI optimiert — prüfen und speichern', variant: 'success' });
    } catch (e) {
      toast({ title: 'Optimierung fehlgeschlagen', description: String((e as Error)?.message ?? e), variant: 'danger' });
    } finally {
      setOptimizing(false);
    }
  };

  // "Konformität prüfen": check the stored axiom strictly against every meta-axiom. On a violation
  // the corrected proposal can be opened in the editor for review.
  const checkConform = async () => {
    if (checking || busy || !axiom) return;
    setChecking(true);
    try {
      const c = await source.mercuryConform(axiom.titel, axiom.body);
      setConf(c);
    } catch (e) {
      toast({ title: 'Konformitätsprüfung fehlgeschlagen', description: String((e as Error)?.message ?? e), variant: 'danger' });
    } finally {
      setChecking(false);
    }
  };

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
        section={namespace}
        initial={draft ?? undefined}
        onCancel={() => {
          setDraft(null); // discard an un-saved AI optimization
          setMode('view');
        }}
        onSave={async (titel, body) => {
          const res = await source.mercuryEditAxiom(path, titel, body);
          setDraft(null);
          toast({ title: 'Axiom gespeichert', variant: 'success' });
          if (res.path !== path) {
            // The title changed the slug, so the record was renamed to keep heading and path
            // matched; follow the new path (reloads the tree and re-selects it).
            onChanged(res.path);
            return;
          }
          // Same path: this pane won't remount and its item fetch won't re-run, so update local
          // state from the server's response — the reading view reflects the edit without a reload.
          setAxiom(res.axiom);
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
          <Button variant="secondary" size="sm" disabled={optimizing || busy} onClick={optimize}>
            {optimizing ? 'Optimiere…' : 'Mit KI optimieren'}
          </Button>
          {namespace === 'axiome' && (
            <Button variant="secondary" size="sm" disabled={checking || busy} onClick={checkConform}>
              {checking ? 'Prüfe…' : 'Konformität prüfen'}
            </Button>
          )}
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
      {conf && (
        <ConformancePanel
          conf={conf}
          onFix={() => {
            if (conf.proposed) {
              setDraft({ titel: conf.proposed.titel, body: conf.proposed.body });
              setMode('edit');
            }
          }}
        />
      )}
      {axiom.quelle && (
        <p className="mt-8 border-t border-separator pt-3 text-caption text-text-tertiary">
          Quelle: <span className="font-mono">{axiom.quelle}</span>
        </p>
      )}
    </article>
  );
}

/** The result of a "Konformität prüfen" run: conforming (a short confirmation), or a list of violated
 *  meta-axioms with a one-click AI fix that opens the corrected version in the editor. */
function ConformancePanel({ conf, onFix }: { conf: Conformance; onFix: () => void }) {
  if (conf.metaCount === 0) {
    return (
      <div className="mt-6 rounded-md border border-separator bg-surface px-3 py-2.5 text-caption text-text-tertiary">
        Noch keine Meta-Axiome definiert — es gibt keine verbindlichen Anforderungen zu prüfen.
      </div>
    );
  }
  if (conf.unavailable) {
    return (
      <div className="mt-6 rounded-md border border-separator bg-surface px-3 py-2.5 text-caption text-text-tertiary">
        Die Konformitätsprüfung ist derzeit nicht erreichbar.
      </div>
    );
  }
  if (conf.conforms) {
    return (
      <div className="mt-6 rounded-md border border-success/30 bg-success/[0.06] px-3 py-2.5">
        <p className="flex items-center gap-1.5 text-caption font-medium text-success">
          <CheckIcon className="h-3.5 w-3.5" /> Erfüllt alle {conf.metaCount} Meta-Axiome
        </p>
      </div>
    );
  }
  return (
    <div className="mt-6 rounded-md border border-warning/30 bg-warning/[0.06] px-3 py-2.5">
      <p className="text-caption font-medium text-warning">Verstößt gegen Meta-Axiome</p>
      <ul className="mt-1.5 flex flex-col gap-1.5">
        {conf.violations.map((v, i) => (
          <li key={i}>
            <p className="text-caption font-medium text-text-primary">{v.meta}</p>
            <p className="text-caption text-text-secondary">{v.issue}</p>
          </li>
        ))}
      </ul>
      {conf.proposed && (
        <Button variant="primary" size="sm" className="mt-2.5" onClick={onFix}>
          KI-Korrektur im Editor öffnen
        </Button>
      )}
    </div>
  );
}

/** Edit an axiom's title and body. Its id, quelle and path are preserved. */
function EditAxiomForm({ axiom, section, initial, onCancel, onSave }: { axiom: Axiom; section: SchemeNs; initial?: { titel: string; body: string }; onCancel: () => void; onSave: (titel: string, body: string) => Promise<void> }) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  // `initial` carries an AI-optimized draft from the view's "Mit KI optimieren"; otherwise edit the
  // stored record as-is.
  const [titel, setTitel] = useState(initial?.titel ?? axiom.titel);
  const [body, setBody] = useState(initial?.body ?? axiom.body);
  const [busy, setBusy] = useState(false);
  const [optimizing, setOptimizing] = useState(false);

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

  const optimize = async () => {
    if (!titel.trim() || !body.trim() || busy || optimizing) return;
    setOptimizing(true);
    try {
      const opt = await source.mercuryOptimize(titel.trim(), body.trim(), section);
      setTitel(opt.titel);
      setBody(opt.body);
      toast({ title: 'Mit KI optimiert — prüfen und speichern', variant: 'success' });
    } catch (e) {
      toast({ title: 'Optimierung fehlgeschlagen', description: String((e as Error)?.message ?? e), variant: 'danger' });
    } finally {
      setOptimizing(false);
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
        <Button variant="primary" size="sm" disabled={busy || optimizing || !titel.trim() || !body.trim()} onClick={save}>
          {busy ? 'Speichert…' : 'Speichern'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy || optimizing || !titel.trim() || !body.trim()} onClick={optimize}>
          {optimizing ? 'Optimiere…' : 'Mit KI optimieren'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy || optimizing} onClick={onCancel}>
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
function Placeholder({ section, label }: { section: SectionId; label?: string }) {
  const sec = SECTIONS.find((s) => s.id === section)!;
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
      <span className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-1 ring-1 ring-separator">
        <sec.icon className="h-6 w-6 text-accent" />
      </span>
      <p className="text-footnote text-text-tertiary">{label ?? sec.label}</p>
    </div>
  );
}
