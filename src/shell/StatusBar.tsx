import { useWorkspace } from '@/state/workspace';
import type { PanelId, RepoStage, StageState } from '@/types';
import { ChevronRightIcon, GitBranchIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

const dotCls: Record<StageState, string> = {
  done: 'bg-success',
  active: 'bg-accent',
  pending: 'bg-fill/30',
};
const textCls: Record<StageState, string> = {
  done: 'text-text-secondary',
  active: 'text-text-primary',
  pending: 'text-text-tertiary',
};

/** Bottom bar: the delivery pipeline (Vision → … → main) on the left, repo status on the right. */
export function StatusBar() {
  const { data, activeRepo, activeBranch, setPanel } = useWorkspace();
  const { stages, changes } = data;
  const staged = changes.filter((c) => c.staged).length;

  // Some overview rows jump to a relevant surface; the rest are informational.
  const onStage = (s: RepoStage) => {
    if (s.id === 'vision' || s.id === 'code') setPanel((s.id === 'code' ? 'project' : 'vision') as PanelId);
  };

  return (
    <footer className="dl-no-select flex h-7 shrink-0 items-center gap-2 border-t border-separator bg-surface-sidebar px-2 text-caption text-text-secondary [backdrop-filter:var(--material-blur-thin)]">
      {/* Pipeline */}
      <div className="flex items-center">
        {stages.map((s, i) => (
          <div key={s.id} className="flex items-center">
            <button
              type="button"
              onClick={() => onStage(s)}
              title={s.hint}
              className="flex items-center gap-1.5 rounded-sm px-1.5 py-0.5 transition-colors hover:bg-fill/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            >
              <span
                className={cn(
                  'h-1.5 w-1.5 rounded-full',
                  dotCls[s.state],
                  s.state === 'active' && 'ring-2 ring-accent/30',
                )}
              />
              <span className={cn('hidden sm:inline', textCls[s.state])}>{s.label}</span>
            </button>
            {i < stages.length - 1 && <ChevronRightIcon className="h-3 w-3 text-text-tertiary" />}
          </div>
        ))}
      </div>

      <div className="ml-auto flex items-center gap-2.5">
        <span className="flex items-center gap-1">
          <GitBranchIcon className="h-3.5 w-3.5" />
          {activeBranch.name}
          {(activeBranch.ahead > 0 || activeBranch.behind > 0) && (
            <span className="font-mono text-text-tertiary">
              {activeBranch.ahead > 0 && ` ↑${activeBranch.ahead}`}
              {activeBranch.behind > 0 && ` ↓${activeBranch.behind}`}
            </span>
          )}
        </span>
        {changes.length > 0 && (
          <span className="hidden items-center gap-1 md:flex">
            <span className="rounded-full bg-accent/15 px-1.5 text-[10px] font-semibold text-accent">{changes.length}</span>
            <span className="text-text-tertiary">changes · {staged} staged</span>
          </span>
        )}
        <span className="hidden text-text-tertiary lg:inline">{activeRepo.language}</span>
        <span className="hidden text-text-tertiary lg:inline">UTF-8</span>
      </div>
    </footer>
  );
}
