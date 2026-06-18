import { useWorkspace } from '@/state/workspace';
import { Brand } from './Brand';
import { RepoDropdown } from './RepoDropdown';
import { BranchDropdown } from './BranchDropdown';
import { ThemeToggle } from './ThemeToggle';
import { Button, IconButton } from '@/ui/Button';
import { HelpIcon, RocketIcon, SettingsIcon } from '@/ui/icons';
import { Tooltip } from '@/ui/Tooltip';
import { PREVIEW_URL } from '@/lib/constants';

/** The window chrome: brand · repository · branch  ……  actions. */
export function TopBar() {
  const { setOverlay } = useWorkspace();

  return (
    <header className="dl-no-select flex h-12 shrink-0 items-center gap-2 border-b border-separator bg-material-regular px-2.5 [backdrop-filter:var(--material-blur)]">
      {/* Identity zone */}
      <Brand />
      <div className="mx-1 h-5 w-px bg-separator" />

      {/* Workspace zone */}
      <RepoDropdown />
      <span className="text-text-tertiary">/</span>
      <BranchDropdown />

      {/* Actions zone */}
      <div className="ml-auto flex items-center gap-1.5">
        <Button
          variant="secondary"
          size="sm"
          className="gap-1.5"
          onClick={() => window.open(PREVIEW_URL, '_blank', 'noopener')}
          title="Open the live sxgate preview"
        >
          <RocketIcon className="h-3.5 w-3.5 text-accent" />
          Preview
        </Button>
        <div className="mx-0.5 h-5 w-px bg-separator" />
        <ThemeToggle />
        <IconButton label="Keyboard shortcuts" title="Keyboard shortcuts  (?)" onClick={() => setOverlay('help')}>
          <HelpIcon className="h-4 w-4" />
        </IconButton>
        <IconButton label="Settings" title="Settings" onClick={() => setOverlay('settings')}>
          <SettingsIcon className="h-4 w-4" />
        </IconButton>
        <Tooltip label="Signed in as nanu" side="bottom">
          <button
            type="button"
            aria-label="Account"
            className="ml-1 flex h-6 w-6 items-center justify-center rounded-full bg-gpu/20 text-caption font-semibold text-gpu transition hover:ring-2 hover:ring-gpu/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
          >
            N
          </button>
        </Tooltip>
      </div>
    </header>
  );
}
