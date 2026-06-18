import { Brand } from './Brand';
import { RepoDropdown } from './RepoDropdown';
import { BranchDropdown } from './BranchDropdown';
import { ThemeToggle } from './ThemeToggle';
import { Button, IconButton } from '@/ui/Button';
import { RocketIcon, SettingsIcon } from '@/ui/icons';
import { PREVIEW_URL } from '@/lib/constants';

/** The window chrome: brand · repository · branch  ……  actions. */
export function TopBar() {
  return (
    <header className="dl-no-select flex h-12 shrink-0 items-center gap-2 border-b border-separator bg-material-regular px-2.5 [backdrop-filter:var(--material-blur)]">
      <Brand />
      <div className="mx-1 h-5 w-px bg-separator" />
      <RepoDropdown />
      <span className="text-text-tertiary">/</span>
      <BranchDropdown />

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
        <IconButton label="Settings" title="Settings — coming in a later phase">
          <SettingsIcon className="h-4 w-4" />
        </IconButton>
        <span
          aria-hidden
          className="ml-1 flex h-6 w-6 items-center justify-center rounded-full bg-gpu/20 text-caption font-semibold text-gpu"
        >
          N
        </span>
      </div>
    </header>
  );
}
