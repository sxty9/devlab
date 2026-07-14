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
            Angemeldet als <span className="font-medium text-text-primary">{user.displayName || user.username}</span>.{' '}
          </>
        ) : null}
        Verknüpfe dein GitHub-Konto, um deine Repositories zu sehen und zu bearbeiten. Welche Repos du
        siehst und was du darfst (lesen / pushen) ergibt sich ausschließlich aus GitHub.
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
          GitHub verknüpfen
        </Button>
        <Button variant="secondary" size="md" className="w-full" onClick={() => window.location.reload()}>
          Erneut prüfen
        </Button>
      </div>
    </GateShell>
  );
}
