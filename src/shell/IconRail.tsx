import { useWorkspace } from '@/state/workspace';
import type { PanelId } from '@/types';
import { Tooltip } from '@/ui/Tooltip';
import { ClaudeIcon, FilesIcon, GitBranchIcon, GitGraphIcon, HelpIcon, LightbulbIcon, SettingsIcon, TerminalIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

interface RailItem {
  id: PanelId;
  label: string;
  Icon: (p: { className?: string }) => JSX.Element;
}

const ITEMS: RailItem[] = [
  { id: 'vision', label: 'Vision', Icon: LightbulbIcon },
  { id: 'project', label: 'Project', Icon: FilesIcon },
  { id: 'vcs', label: 'Version Control', Icon: GitBranchIcon },
  { id: 'git', label: 'Git Log & Worktrees', Icon: GitGraphIcon },
  { id: 'claude', label: 'Claude', Icon: ClaudeIcon },
  { id: 'terminal', label: 'Terminal', Icon: TerminalIcon },
];

/** The 56px left rail of tool toggles. Click an active tool to collapse its panel. */
export function IconRail() {
  const { activePanel, togglePanel, data, setOverlay } = useWorkspace();
  const changeCount = data.changes.length;

  return (
    <nav
      aria-label="Tools"
      className="dl-no-select flex w-14 shrink-0 flex-col items-center gap-1 border-r border-separator bg-surface-sidebar py-2 [backdrop-filter:var(--material-blur-thin)]"
    >
      {ITEMS.map(({ id, label, Icon }) => {
        const active = activePanel === id;
        return (
          <Tooltip key={id} label={label} side="right">
            <button
              type="button"
              aria-label={label}
              aria-pressed={active}
              onClick={() => togglePanel(id)}
              className={cn(
                'relative flex h-10 w-10 items-center justify-center rounded-md transition duration-fast ease-out',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50',
                active ? 'bg-fill/[0.07] text-text-primary' : 'text-text-tertiary hover:bg-fill/10 hover:text-text-secondary',
              )}
            >
              <span
                className={cn(
                  'absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded-r bg-accent transition-opacity duration-fast',
                  active ? 'opacity-100' : 'opacity-0',
                )}
              />
              <Icon className="h-5 w-5" />
              {id === 'vcs' && changeCount > 0 && (
                <span className="absolute -right-0.5 -top-0.5 flex h-4 min-w-[1rem] items-center justify-center rounded-full bg-accent px-1 text-[10px] font-semibold leading-none text-accent-fg">
                  {changeCount}
                </span>
              )}
            </button>
          </Tooltip>
        );
      })}

      <div className="mt-auto flex flex-col items-center gap-1">
        <Tooltip label="Keyboard shortcuts  (?)" side="right">
          <button
            type="button"
            aria-label="Keyboard shortcuts"
            onClick={() => setOverlay('help')}
            className="flex h-10 w-10 items-center justify-center rounded-md text-text-tertiary transition duration-fast ease-out hover:bg-fill/10 hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
          >
            <HelpIcon className="h-5 w-5" />
          </button>
        </Tooltip>
        <Tooltip label="Settings" side="right">
          <button
            type="button"
            aria-label="Settings"
            onClick={() => setOverlay('settings')}
            className="flex h-10 w-10 items-center justify-center rounded-md text-text-tertiary transition duration-fast ease-out hover:bg-fill/10 hover:text-text-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
          >
            <SettingsIcon className="h-5 w-5" />
          </button>
        </Tooltip>
      </div>
    </nav>
  );
}
