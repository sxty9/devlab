import { useSession } from '@/state/session';
import { Brand } from './Brand';
import { RepoDropdown } from './RepoDropdown';
import { BranchDropdown } from './BranchDropdown';
import { ThemeToggle } from './ThemeToggle';
import { IconButton } from '@/ui/Button';
import { HelpIcon, SettingsIcon } from '@/ui/icons';
import { Tooltip } from '@/ui/Tooltip';
import { Person } from '@/ui/Person';
import { CAPABILITIES } from '@/views/capabilities';

/** The window chrome: brand · (repository · branch, in the IDE) ……  actions. */
export function TopBar() {
  const { setOverlay, user, view } = useSession();
  const name = user.displayName || user.username || 'Unknown';

  // In a capability, the workspace zone names it instead of selecting a repo/branch.
  const capability = view.kind === 'mercury' || view.kind === 'atlas' ? CAPABILITIES.find((c) => c.id === view.kind) : undefined;

  return (
    <header className="dl-no-select flex h-12 shrink-0 items-center gap-2 border-b border-separator bg-material-regular px-2.5 [backdrop-filter:var(--material-blur)]">
      {/* Identity zone */}
      <Brand />

      {/* Workspace zone */}
      {view.kind === 'ide' && (
        <>
          <div className="mx-1 h-5 w-px bg-separator" />
          <RepoDropdown />
          <span className="text-text-tertiary">/</span>
          <BranchDropdown />
        </>
      )}
      {capability && (
        <>
          <div className="mx-1 h-5 w-px bg-separator" />
          <span className="flex items-center gap-1.5 px-1 text-subhead font-medium text-text-primary">
            <capability.icon className="h-4 w-4 text-accent" />
            {capability.displayName}
          </span>
        </>
      )}

      {/* Actions zone */}
      <div className="ml-auto flex items-center gap-1.5">
        <ThemeToggle />
        <IconButton label="Keyboard shortcuts" title="Keyboard shortcuts  (?)" onClick={() => setOverlay('help')}>
          <HelpIcon className="h-4 w-4" />
        </IconButton>
        <IconButton label="Settings" title="Settings" onClick={() => setOverlay('settings')}>
          <SettingsIcon className="h-4 w-4" />
        </IconButton>
        <Tooltip label={`Signed in as ${name}${user.isAdmin ? ' · admin' : ''}`} side="bottom">
          <button
            type="button"
            aria-label="Account"
            className="ml-1 rounded-full transition hover:ring-2 hover:ring-gpu/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
          >
            <Person avatarOnly username={user.username} displayName={user.displayName} />
          </button>
        </Tooltip>
      </div>
    </header>
  );
}
