import { useWorkspace } from '@/state/workspace';
import { FileTextIcon, FolderIcon, GitBranchIcon, RocketIcon } from '@/ui/icons';
import { Button } from '@/ui/Button';
import { tintSoftBg, tintText } from '@/ui/tint';
import { PREVIEW_URL } from '@/lib/constants';
import { cn } from '@/lib/cn';

/** The repo "skeleton" overview — a landing surface summarising structure & delivery state. */
export function StructureView() {
  const { data, activeRepo, activeBranch } = useWorkspace();

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
          <Button
            variant="primary"
            size="sm"
            className="ml-auto mt-1 shrink-0"
            onClick={() => window.open(PREVIEW_URL, '_blank', 'noopener')}
            title="Open the live sxgate preview"
          >
            <RocketIcon className="h-3.5 w-3.5" />
            Open preview
          </Button>
        </div>

        {/* Pipeline */}
        <div className="mt-8 rounded-card border border-separator bg-surface p-4 shadow-elev-1">
          <h2 className="mb-3 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Delivery pipeline</h2>
          <div className="flex flex-wrap items-center gap-2">
            {data.stages.map((s, i) => (
              <div key={s.id} className="flex items-center gap-2">
                <div
                  className={cn(
                    'flex items-center gap-2 rounded-md px-2.5 py-1.5',
                    s.state === 'active' && 'bg-accent/15',
                    s.state === 'done' && 'bg-success/10',
                    s.state === 'pending' && 'bg-fill/[0.06]',
                  )}
                  title={s.hint}
                >
                  <span
                    className={cn(
                      'h-2 w-2 rounded-full',
                      s.state === 'done' && 'bg-success',
                      s.state === 'active' && 'bg-accent',
                      s.state === 'pending' && 'bg-fill/30',
                    )}
                  />
                  <span
                    className={cn(
                      'text-footnote font-medium',
                      s.state === 'pending' ? 'text-text-tertiary' : 'text-text-primary',
                    )}
                  >
                    {s.label}
                  </span>
                </div>
                {i < data.stages.length - 1 && <span className="text-text-tertiary">→</span>}
              </div>
            ))}
          </div>
        </div>

        {/* Structure sections */}
        <div className="mt-6 space-y-4">
          {data.structure.map((section) => (
            <section key={section.title} className="rounded-card border border-separator bg-surface shadow-elev-1">
              <div className="border-b border-separator px-4 py-3">
                <h3 className="text-subhead font-semibold text-text-primary">{section.title}</h3>
                <p className="mt-0.5 text-caption text-text-tertiary">{section.hint}</p>
              </div>
              <ul className="divide-y divide-separator">
                {section.entries.map((e) => (
                  <li key={e.name} className="flex items-center gap-3 px-4 py-2.5">
                    {e.kind === 'dir' ? (
                      <FolderIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
                    ) : (
                      <FileTextIcon className="h-4 w-4 shrink-0 text-text-tertiary" />
                    )}
                    <span className="shrink-0 font-mono text-footnote text-text-primary">{e.name}</span>
                    <span className="truncate text-caption text-text-secondary">{e.note}</span>
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
