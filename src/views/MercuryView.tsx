import { useEffect, useMemo, useState } from 'react';
import { useSession } from '@/state/session';
import { getDataSource } from '@/data';
import { parseAxioms, axiomBodyToMarkdown, type AxiomSection } from '@/lib/axioms';
import { MERCURY_DOCS, MERCURY_INBOX, MERCURY_REPO, type MercuryDocId } from '@/lib/mercury';
import { renderMarkdown } from '@/lib/markdown';
import { Splash } from '@/shell/Splash';
import { cn } from '@/lib/cn';
import type { FileNode } from '@/types';

/** Find a directory node in the tree by its path and return its children. */
function childrenOf(tree: FileNode[], path: string): FileNode[] {
  for (const n of tree) {
    if (n.id === path) return n.children ?? [];
    if (n.children) {
      const hit = childrenOf(n.children, path);
      if (hit.length) return hit;
    }
  }
  return [];
}

type Selection = { doc: MercuryDocId; id: string | null };

/** Mercury — the centre for the Holistic axioms.
 *
 *  Read-only for now: it browses the guiding documents and the decision inbox. Editing and the
 *  "aggregate" run (inbox -> guiding documents -> push) are the next stage. It owns no store: the
 *  markdown in git is the single source of truth and this is a projection of it, served by the repo
 *  endpoints that already exist. */
export function MercuryView() {
  const { repos } = useSession();
  const source = useMemo(() => getDataSource(), []);

  const [docs, setDocs] = useState<Record<string, string> | null>(null);
  const [inbox, setInbox] = useState<FileNode[]>([]);
  const [failed, setFailed] = useState(false);
  const [sel, setSel] = useState<Selection>({ doc: 'axioms', id: null });

  const available = repos.some((r) => r.id === MERCURY_REPO);

  useEffect(() => {
    if (!available) return;
    let cancelled = false;
    Promise.all([
      Promise.all(MERCURY_DOCS.map((d) => source.fileContent(MERCURY_REPO, d.path))),
      source.repoData(MERCURY_REPO),
    ])
      .then(([contents, data]) => {
        if (cancelled) return;
        setDocs(Object.fromEntries(MERCURY_DOCS.map((d, i) => [d.id, contents[i].code])));
        setInbox(childrenOf(data.tree, MERCURY_INBOX).filter((n) => n.kind === 'file' && !n.name.startsWith('.')));
      })
      .catch(() => !cancelled && setFailed(true));
    return () => {
      cancelled = true;
    };
  }, [source, available]);

  const sections: AxiomSection[] = useMemo(() => (docs ? parseAxioms(docs[sel.doc] ?? '') : []), [docs, sel.doc]);

  // The reading pane shows the narrowest thing selected: one block, else a whole section (its prose
  // plus its blocks — several sections carry all their weight in the prose and have no blocks at
  // all), else the whole document.
  const html = useMemo(() => {
    if (!docs) return '';

    const entry = sections.flatMap((s) => s.entries).find((e) => e.id === sel.id);
    if (entry) return renderMarkdown(axiomBodyToMarkdown(entry.body));

    const section = sections.find((s) => s.tag === sel.id);
    if (section) {
      const parts = [section.intro, ...section.entries.map((e) => `**${e.name}**\n\n${e.body}`)];
      return renderMarkdown(axiomBodyToMarkdown(parts.filter(Boolean).join('\n\n')));
    }

    return renderMarkdown(axiomBodyToMarkdown(docs[sel.doc] ?? ''));
  }, [docs, sections, sel]);

  if (!available) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base">
        <p className="max-w-sm text-center text-footnote text-text-secondary">
          Mercury benötigt Zugriff auf das Repository <span className="text-text-primary">{MERCURY_REPO}</span>.
        </p>
      </div>
    );
  }

  if (failed) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center bg-bg-base">
        <p className="max-w-sm text-center text-footnote text-text-secondary">
          Die Axiome konnten nicht geladen werden.
        </p>
      </div>
    );
  }

  if (!docs) return <Splash />;

  return (
    <div className="flex min-h-0 flex-1">
      <aside className="dl-scroll flex w-72 shrink-0 flex-col overflow-y-auto border-r border-separator bg-surface-sidebar py-3">
        {MERCURY_DOCS.map((d) => {
          const active = sel.doc === d.id;
          return (
            <div key={d.id}>
              <button
                type="button"
                onClick={() => setSel({ doc: d.id, id: null })}
                className={cn(
                  'flex w-full items-center px-3 py-1.5 text-left text-footnote font-medium transition duration-fast',
                  active && !sel.id ? 'text-text-primary' : 'text-text-secondary hover:text-text-primary',
                )}
              >
                {d.label}
              </button>
              {active && (
                <div className="mb-2">
                  {sections.map((s) => (
                    <div key={s.tag}>
                      <button
                        type="button"
                        onClick={() => setSel({ doc: d.id, id: s.tag })}
                        className={cn(
                          'block w-full truncate px-3 py-1 text-left text-caption uppercase tracking-wide transition duration-fast',
                          sel.id === s.tag ? 'text-text-primary' : 'text-text-tertiary hover:text-text-secondary',
                        )}
                      >
                        {s.tag}
                      </button>
                      {s.entries.map((e) => (
                        <button
                          key={e.id}
                          type="button"
                          onClick={() => setSel({ doc: d.id, id: e.id })}
                          className={cn(
                            'flex w-full items-center border-l-2 py-1 pl-5 pr-3 text-left text-footnote transition duration-fast',
                            sel.id === e.id
                              ? 'border-accent bg-fill/[0.07] text-text-primary'
                              : 'border-transparent text-text-secondary hover:bg-fill/10 hover:text-text-primary',
                          )}
                        >
                          <span className="truncate">{e.name}</span>
                        </button>
                      ))}
                    </div>
                  ))}
                </div>
              )}
            </div>
          );
        })}

        <p className="mt-2 flex items-center justify-between px-3 py-1 text-caption uppercase tracking-wide text-text-tertiary">
          Konsolidierungen
          <span className="text-text-tertiary">{inbox.length}</span>
        </p>
        {inbox.map((n) => (
          <span key={n.id} className="truncate px-3 py-1 pl-5 text-footnote text-text-secondary">
            {n.name}
          </span>
        ))}
      </aside>

      <main className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base px-8 py-7">
        <article className="dl-markdown mx-auto max-w-3xl" dangerouslySetInnerHTML={{ __html: html }} />
      </main>
    </div>
  );
}
