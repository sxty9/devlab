import { useCallback, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { renderMarkdown } from '@/lib/markdown';
import { Splash } from '@/shell/Splash';
import { cn } from '@/lib/cn';
import { ChevronRightIcon, MercuryIcon, SitemapIcon, RocketIcon, DotIcon } from '@/ui/icons';
import type { Axiom, MercuryNode, MercuryTree } from '@/types';

type SectionId = 'axiome' | 'regeln' | 'laeufe';

const SECTIONS: { id: SectionId; label: string; icon: (p: { className?: string }) => JSX.Element; empty: string }[] = [
  { id: 'axiome', label: 'Axiome', icon: MercuryIcon, empty: 'Noch keine Axiome. Aigentic sortiert neue Axiome automatisch ein.' },
  { id: 'regeln', label: 'Implementierungsregeln', icon: SitemapIcon, empty: 'Noch keine Implementierungsregeln.' },
  { id: 'laeufe', label: 'Automatische Läufe', icon: RocketIcon, empty: 'Automatische Läufe sind noch nicht eingerichtet.' },
];

/** Mercury — the centre for the Holistic axioms, in three parts: the axioms (auto-sorted into an
 *  arbitrarily deep category tree), the implementation rules, and the scheduled runs. Read-only for
 *  now: adding axioms, the rollout and the runs are the next stages. It owns no store — the tree is
 *  a projection of aigentic's scheme-backed graveyard. */
export function MercuryView() {
  const source = useMemo(() => getDataSource(), []);

  const [tree, setTree] = useState<MercuryTree | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [section, setSection] = useState<SectionId>('axiome');
  const [selected, setSelected] = useState<MercuryNode | null>(null);

  useEffect(() => {
    let cancelled = false;
    source
      .mercuryTree()
      .then((t) => !cancelled && setTree(t))
      .catch((e: unknown) => !cancelled && setFailed(String((e as Error)?.message ?? e)));
    return () => {
      cancelled = true;
    };
  }, [source]);

  if (failed) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base px-6">
        <p className="max-w-md text-center text-footnote text-text-secondary">{failed}</p>
      </div>
    );
  }
  if (!tree) return <Splash />;

  const roots = tree[section];

  return (
    <div className="flex min-h-0 flex-1">
      <aside className="dl-scroll flex w-72 shrink-0 flex-col overflow-y-auto border-r border-separator bg-surface-sidebar">
        <nav className="flex flex-col gap-0.5 border-b border-separator p-2">
          {SECTIONS.map((sec) => {
            const active = section === sec.id;
            return (
              <button
                key={sec.id}
                type="button"
                onClick={() => {
                  setSection(sec.id);
                  setSelected(null);
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

        <div className="flex-1 p-1.5">
          {roots.length === 0 ? (
            <p className="px-2.5 py-3 text-caption text-text-tertiary">{SECTIONS.find((s) => s.id === section)!.empty}</p>
          ) : (
            roots.map((n) => <TreeRow key={n.path} node={n} depth={0} selected={selected} onSelect={setSelected} />)
          )}
        </div>
      </aside>

      <main className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
        {selected ? <AxiomPane node={selected} /> : <Placeholder section={section} />}
      </main>
    </div>
  );
}

/** One row of the category tree. Categories are collapsible to any depth; axioms are selectable
 *  leaves. Expansion is local state, so the tree remembers what you opened. */
function TreeRow({
  node,
  depth,
  selected,
  onSelect,
}: {
  node: MercuryNode;
  depth: number;
  selected: MercuryNode | null;
  onSelect: (n: MercuryNode) => void;
}) {
  const [open, setOpen] = useState(depth < 1);
  const pad = { paddingLeft: `${depth * 14 + 8}px` };

  if (node.isAxiom) {
    const active = selected?.path === node.path;
    return (
      <button
        type="button"
        style={pad}
        onClick={() => onSelect(node)}
        className={cn(
          'flex w-full items-center gap-1.5 rounded-md py-1 pr-2 text-left text-footnote transition duration-fast',
          active ? 'bg-accent/15 text-text-primary' : 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
        )}
      >
        <DotIcon className={cn('h-3 w-3 shrink-0', active ? 'text-accent' : 'text-text-tertiary')} />
        <span className="truncate">{node.name}</span>
      </button>
    );
  }

  return (
    <div>
      <button
        type="button"
        style={pad}
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-1 rounded-md py-1 pr-2 text-left text-footnote font-medium text-text-secondary transition duration-fast hover:bg-fill/10 hover:text-text-primary"
      >
        <ChevronRightIcon className={cn('h-3.5 w-3.5 shrink-0 transition-transform duration-fast', open && 'rotate-90')} />
        <span className="truncate">{node.name}</span>
      </button>
      {open && node.children?.map((c) => <TreeRow key={c.path} node={c} depth={depth + 1} selected={selected} onSelect={onSelect} />)}
    </div>
  );
}

/** The reading pane for a selected axiom: its title, source, and body. Content loads on demand. */
function AxiomPane({ node }: { node: MercuryNode }) {
  const source = useMemo(() => getDataSource(), []);
  const [axiom, setAxiom] = useState<Axiom | null>(null);
  const [err, setErr] = useState(false);

  const load = useCallback(() => {
    setAxiom(null);
    setErr(false);
    source
      .mercuryItem(node.path)
      .then(setAxiom)
      .catch(() => setErr(true));
  }, [source, node.path]);

  useEffect(load, [load]);

  if (err) {
    return <p className="px-8 py-7 text-footnote text-text-secondary">Dieses Axiom konnte nicht geladen werden.</p>;
  }
  if (!axiom) {
    return <p className="px-8 py-7 text-footnote text-text-tertiary">Lädt…</p>;
  }

  return (
    <article className="mx-auto max-w-3xl px-8 py-7">
      <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{axiom.titel || node.name}</h1>
      <p className="mt-1 font-mono text-caption text-text-tertiary">{node.path}</p>
      <div className="dl-markdown mt-5" dangerouslySetInnerHTML={{ __html: renderMarkdown(axiom.body) }} />
      {axiom.quelle && (
        <p className="mt-8 border-t border-separator pt-3 text-caption text-text-tertiary">
          Quelle: <span className="font-mono">{axiom.quelle}</span>
        </p>
      )}
    </article>
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
