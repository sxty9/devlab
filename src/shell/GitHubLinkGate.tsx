import { Button } from '@/ui/Button';
import { GateShell } from './LoginGate';
import { getDataSource } from '@/data';
import type { User } from '@/types';

/** Mandatory gate between DevLab access and the workspace: the user must link their GitHub
 *  account. DevLab's repo visibility and read/write authorization derive entirely from GitHub
 *  (the single source of truth), so there is nothing to show until the account is linked. */
export function GitHubLinkGate({ user }: { user: User }) {
  const source = getDataSource();
  return (
    <GateShell>
      <p className="mt-1 max-w-sm text-footnote text-text-secondary">
        {user.displayName || user.username ? (
          <>
            Signed in as <span className="font-medium text-text-primary">{user.displayName || user.username}</span>.{' '}
          </>
        ) : null}
        Link your GitHub account to see and edit your repositories. Which repositories you see, and whether
        you may read or push, follows from GitHub alone.
      </p>
      <div className="mt-6 flex w-full max-w-xs flex-col gap-2">
        <Button
          variant="primary"
          size="md"
          className="w-full"
          onClick={() => {
            window.location.href = source.githubAuthorizeUrl();
          }}
        >
          Link GitHub
        </Button>
        <Button variant="secondary" size="md" className="w-full" onClick={() => window.location.reload()}>
          Check again
        </Button>
      </div>
    </GateShell>
  );
}
