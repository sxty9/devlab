import { useWorkspace } from '@/state/workspace';
import { ChevronRightIcon, FileTextIcon, FolderIcon, GitBranchIcon } from '@/ui/icons';
import { tintSoftBg, tintText } from '@/ui/tint';
import { basename, guessLang } from '@/lib/lang';
import { cn } from '@/lib/cn';
import type { StructureSection } from '@/types';

/** The repo "skeleton" overview — a navigable landing surface for what the repo IS. Every row
 *  comes from the server's derivation of real git state; nothing is asserted that could not be
 *  observed (B 1.4). */
export function StructureView() {
  const { data, activeRepo, activeBranch, openFile, setPanel } = useWorkspace();

  const openPath = (path: string) => openFile({ id: path, name: basename(path), lang: guessLang(path) });

  const onEntry = (e: StructureSection['entries'][number]) => {
    const looksLikePath = !/[ ()]/.test(e.name);
    if (e.kind === 'file' && looksLikePath) openPath(e.name);
    else setPanel('project');
  };

  return (
    <div className="dl-scroll min-h-0 flex-1 overflow-y-auto bg-bg-base">
      <div className="mx-auto max-w-3xl px-8 py-10">
        {/* Header */}
        <div className="flex items-start gap-4">
          <span
            className={cn(
              'mt-1 flex h-12 w-12 items-center justify-center rounded-card shadow-elev-1 ring-1 ring-separator',
              tintSoftBg[activeRepo.tint],
            )}
          >
            <FolderIcon className={cn('h-6 w-6', tintText[activeRepo.tint])} />
          </span>
          <div className="min-w-0">
            <h1 className="text-title1 font-semibold tracking-tight text-text-primary">{activeRepo.name}</h1>
            <p className="mt-1 text-subhead text-text-secondary">{activeRepo.description}</p>
            <div className="mt-3 flex flex-wrap items-center gap-2 text-caption">
              <span className="rounded-sm bg-fill/10 px-2 py-0.5 capitalize text-text-secondary">{activeRepo.kind}</span>
              <span className={cn('rounded-sm bg-fill/10 px-2 py-0.5', tintText[activeRepo.tint])}>{activeRepo.language}</span>
              <span className="flex items-center gap-1 rounded-sm bg-fill/10 px-2 py-0.5 text-text-secondary">
                <GitBranchIcon className="h-3.5 w-3.5" />
                {activeBranch.name}
              </span>
            </div>
          </div>
        </div>

        {/* Repo state — ONLY the rows the server could attest from real git state (B 1.4). No row
            is rendered for anything unknown, and each row shows its ground alongside its state, so
            nothing has to be hovered to be understood. */}
        {data.stages.length > 0 && (
          <div className="mt-8 flex flex-wrap items-center gap-2">
            {data.stages.map((s) => (
              <div
                key={s.id}
                className={cn(
                  'flex items-center gap-2 rounded-md px-2.5 py-1.5',
                  s.state === 'active' && 'bg-accent/15 ring-1 ring-accent/30',
                  s.state === 'done' && 'bg-success/10',
                  s.state === 'pending' && 'bg-fill/[0.06]',
                )}
              >
                <span
                  className={cn(
                    'h-2 w-2 rounded-full',
                    s.state === 'done' && 'bg-success',
                    s.state === 'active' && 'bg-accent',
                    s.state === 'pending' && 'bg-fill/30',
                  )}
                />
                <span className={cn('text-footnote font-medium', s.state === 'pending' ? 'text-text-tertiary' : 'text-text-primary')}>
                  {s.label}
                </span>
                {s.hint && <span className="text-caption text-text-secondary">{s.hint}</span>}
              </div>
            ))}
          </div>
        )}

        {/* Structure sections — the server lists the modules and key files that really exist; the
            view invents no shortcuts to files that may not be there. */}
        <div className="mt-6 space-y-4">
          {data.structure.map((section) => (
            <section key={section.title} className="overflow-hidden rounded-card border border-separator bg-surface shadow-elev-1">
              <div className="border-b border-separator px-4 py-3">
                <h3 className="text-subhead font-semibold text-text-primary">{section.title}</h3>
                <p className="mt-0.5 text-caption text-text-tertiary">{section.hint}</p>
              </div>
              <ul className="divide-y divide-separator">
                {section.entries.map((e) => (
                  <li key={e.name}>
                    <button
                      type="button"
                      onClick={() => onEntry(e)}
                      className="group flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-fill/[0.06] focus-visible:outline-none focus-visible:bg-fill/[0.06]"
                    >
                      {e.kind === 'dir' ? (
                        <FolderIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
                      ) : (
                        <FileTextIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
                      )}
                      <span className="shrink-0 font-mono text-footnote text-text-primary">{e.name}</span>
                      <span className="truncate text-caption text-text-secondary">{e.note}</span>
                      <ChevronRightIcon className="ml-auto h-4 w-4 shrink-0 text-text-tertiary opacity-0 transition group-hover:opacity-100" />
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
