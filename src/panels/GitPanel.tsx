import { useWorkspace } from '@/state/workspace';
import { PanelHeader } from './PanelHeader';
import { CommitGraph } from './CommitGraph';
import { IconButton } from '@/ui/Button';
import { useToast } from '@/ui/Toast';
import { CheckIcon, GitBranchIcon, GitGraphIcon, RefreshIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="dl-no-select sticky top-0 z-10 flex items-center gap-2 bg-surface-sidebar px-3 py-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
      {children}
    </div>
  );
}

/** IntelliJ-style Git tool window: worktrees, branches (checkout), and the commit log graph. */
export function GitPanel() {
  const { data, activeBranch, setBranch } = useWorkspace();
  const { toast } = useToast();

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Git"
        actions={
          <IconButton label="Fetch" title="Fetch — wired up with the backend (phase 2)" onClick={() => toast({ title: 'Fetch', description: 'Live git arrives with the backend.' })}>
            <RefreshIcon className="h-4 w-4" />
          </IconButton>
        }
      />

      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto pb-3">
        {/* Worktrees */}
        <SectionLabel>
          <GitGraphIcon className="h-3.5 w-3.5" />
          Worktrees
          <span className="rounded-full bg-fill/10 px-1.5 text-[10px] font-semibold">{data.worktrees.length}</span>
        </SectionLabel>
        <ul className="px-2 pb-1.5">
          {data.worktrees.map((w) => (
            <li key={w.branch}>
              <div className="flex items-start gap-2 rounded-md px-2 py-1.5">
                <span className={cn('mt-1 h-2 w-2 shrink-0 rounded-full', w.current ? 'bg-accent' : 'bg-fill/30')} />
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-1.5">
                    <span className="truncate font-mono text-footnote text-text-primary">{w.branch}</span>
                    {w.current && <span className="shrink-0 rounded-sm bg-accent/15 px-1.5 text-[10px] font-semibold text-accent">current</span>}
                  </span>
                  <span className="block truncate text-caption text-text-tertiary">{w.note}</span>
                  {w.url && (
                    <a href={w.url} target="_blank" rel="noreferrer" className="mt-0.5 flex items-center gap-1 text-[11px] text-text-secondary transition-colors hover:text-accent">
                      <span className="h-1.5 w-1.5 rounded-full bg-success" />
                      <span className="truncate font-mono">{w.url.replace('https://', '')}</span>
                    </a>
                  )}
                </span>
              </div>
            </li>
          ))}
        </ul>

        {/* Branches */}
        <SectionLabel>
          <GitBranchIcon className="h-3.5 w-3.5" />
          Branches
          <span className="rounded-full bg-fill/10 px-1.5 text-[10px] font-semibold">{data.branches.length}</span>
        </SectionLabel>
        <ul className="px-2 pb-1.5">
          {data.branches.map((b) => {
            const current = b.name === activeBranch.name;
            return (
              <li key={b.name}>
                <button
                  type="button"
                  onClick={() => setBranch(b.name)}
                  title={current ? 'Current branch' : `Checkout ${b.name}`}
                  className={cn(
                    'group flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left transition-colors',
                    current ? 'bg-fill/[0.08]' : 'hover:bg-fill/10',
                  )}
                >
                  <GitBranchIcon className={cn('h-4 w-4 shrink-0', current ? 'text-accent' : 'text-text-tertiary')} />
                  <span className={cn('truncate text-footnote', current ? 'text-text-primary' : 'text-text-secondary')}>{b.name}</span>
                  {b.isDefault && <span className="shrink-0 rounded-sm bg-fill/10 px-1.5 text-[10px] text-text-tertiary">default</span>}
                  <span className="ml-auto shrink-0 font-mono text-[11px] text-text-tertiary">
                    {b.ahead > 0 && <span className="text-success">↑{b.ahead}</span>}
                    {b.ahead > 0 && b.behind > 0 && ' '}
                    {b.behind > 0 && <span className="text-warning">↓{b.behind}</span>}
                  </span>
                  {current && <CheckIcon className="h-4 w-4 shrink-0 text-accent" />}
                </button>
              </li>
            );
          })}
        </ul>

        {/* Log */}
        <SectionLabel>
          <GitGraphIcon className="h-3.5 w-3.5" />
          Log
          <span className="rounded-full bg-fill/10 px-1.5 text-[10px] font-semibold">{data.commits.length}</span>
        </SectionLabel>
        <CommitGraph commits={data.commits} />
      </div>
    </div>
  );
}
