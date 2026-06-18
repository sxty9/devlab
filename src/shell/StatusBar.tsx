import { useWorkspace } from '@/state/workspace';
import type { StageState } from '@/types';
import { ChevronRightIcon, GitBranchIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

const stageDot: Record<StageState, string> = {
  done: 'bg-success',
  active: 'bg-accent',
  pending: 'bg-fill/30',
};
const stageText: Record<StageState, string> = {
  done: 'text-text-secondary',
  active: 'text-text-primary',
  pending: 'text-text-tertiary',
};

/** Bottom bar: the delivery pipeline (Vision → … → main) on the left, repo status on the right. */
export function StatusBar() {
  const { data, activeRepo, activeBranch } = useWorkspace();
  const { stages, changes } = data;
  const previewStage = stages.find((s) => s.id === 'preview');
  const staged = changes.filter((c) => c.staged).length;

  return (
    <footer className="dl-no-select flex h-7 shrink-0 items-center gap-3 border-t border-separator bg-surface-sidebar px-3 text-caption text-text-secondary [backdrop-filter:var(--material-blur-thin)]">
      {/* Pipeline */}
      <div className="flex items-center gap-1.5">
        {stages.map((s, i) => (
          <div key={s.id} className="flex items-center gap-1.5" title={s.hint}>
            <span className={cn('h-1.5 w-1.5 rounded-full', stageDot[s.state], s.state === 'active' && 'animate-pulse')} />
            <span className={cn('hidden sm:inline', stageText[s.state])}>{s.label}</span>
            {i < stages.length - 1 && <ChevronRightIcon className="h-3 w-3 text-text-tertiary" />}
          </div>
        ))}
      </div>

      <div className="ml-auto flex items-center gap-3">
        {previewStage && previewStage.state !== 'pending' && (
          <a
            href={`https://${activeBranch.name === 'main' ? 'main' : 'branch'}-${activeRepo.name}.henrysoase.org`}
            className="flex items-center gap-1.5 text-text-secondary transition-colors hover:text-accent"
            target="_blank"
            rel="noreferrer"
          >
            <span className="h-1.5 w-1.5 rounded-full bg-success" />
            <span className="font-mono">{`${activeBranch.name === 'main' ? 'main' : '…'}-${activeRepo.name}.henrysoase.org`}</span>
          </a>
        )}
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
        <span className="hidden md:inline">{changes.length} changes · {staged} staged</span>
        <span className="hidden lg:inline text-text-tertiary">{activeRepo.language}</span>
        <span className="hidden lg:inline text-text-tertiary">UTF-8</span>
      </div>
    </footer>
  );
}
