import { useSession } from '@/state/session';
import { useWorkspace } from '@/state/workspace';
import { Brand } from './Brand';
import { RepoDropdown } from './RepoDropdown';
import { BranchDropdown } from './BranchDropdown';
import { ThemeToggle } from './ThemeToggle';
import { Button, IconButton } from '@/ui/Button';
import { HelpIcon, RocketIcon, SettingsIcon } from '@/ui/icons';
import { Tooltip } from '@/ui/Tooltip';
import { Person } from '@/ui/Person';
import { devServiceUrl } from '@/lib/constants';
import { CAPABILITIES } from '@/views/capabilities';

/** The static way from the repository to its running service (B-18). It states WHERE the service is
 *  operated — this instance's dashboard tab — and nothing about whether it is up; a deployment is
 *  attested by the delivery ledger, never by a link. Offered only for a repository the SERVER states
 *  to be a service: a library has no service to open, and a control into the void is not offered at
 *  all (REQ-040.3). Reads the opened repository, so it renders inside the IDE zone only. */
function RunningServiceLink() {
  const { activeRepo } = useWorkspace();
  if (activeRepo.kind !== 'service') return null;
  return (
    <Button
      variant="secondary"
      size="sm"
      className="gap-1.5"
      onClick={() => window.open(devServiceUrl(activeRepo.name), '_blank', 'noopener')}
    >
      <RocketIcon className="h-3.5 w-3.5 text-accent" />
      Running service ↗
    </Button>
  );
}

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
        {view.kind === 'ide' && (
          <>
            <RunningServiceLink />
            <div className="mx-0.5 h-5 w-px bg-separator" />
          </>
        )}
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
