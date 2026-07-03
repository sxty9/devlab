import { CodeIcon } from '@/ui/icons';
import { Button } from '@/ui/Button';
import type { User } from '@/types';

/** Resolve the Holistic dashboard origin from DevLab's own host: devlab.<zone> → holistic.<zone>.
 *  Falls back to a bare path in dev/unknown hosts. */
function holisticOrigin(): string {
  if (typeof window === 'undefined') return '/';
  const { protocol, hostname, host } = window.location;
  const parts = hostname.split('.');
  if (parts.length >= 2) {
    return `${protocol}//holistic.${parts.slice(1).join('.')}`;
  }
  return `${protocol}//${host}`;
}

/** The Holistic sign-in URL carrying a `return` param, so Holistic sends the user straight back to
 *  where they were in DevLab after signing in (Holistic validates it stays on the same zone). */
function holisticLoginUrl(): string {
  const origin = holisticOrigin();
  if (typeof window === 'undefined') return origin;
  return `${origin}/?return=${encodeURIComponent(window.location.href)}`;
}

/** The centered brand-mark frame shared by the sign-in, access-denied and GitHub-link gates. */
export function GateShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-full flex-col items-center justify-center bg-bg-base px-6 text-center text-text-primary">
      <div className="relative">
        <div className="absolute inset-0 -z-10 scale-150 rounded-full bg-accent/20 blur-3xl" aria-hidden />
        <span className="flex h-14 w-14 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-3 ring-1 ring-separator">
          <CodeIcon className="h-7 w-7 text-accent" />
        </span>
      </div>
      <h1 className="mt-5 text-title2 font-semibold tracking-tight">DevLab</h1>
      {children}
      <p className="mt-6 text-caption text-text-tertiary">Holistic DevLab</p>
    </div>
  );
}

/** Shown when there is no Holistic session on this origin. Sign in via the Holistic dashboard;
 *  the shared session cookie then carries to DevLab. */
export function SignInGate() {
  return (
    <GateShell>
      <p className="mt-1 max-w-xs text-footnote text-text-secondary">
        Bitte über Holistic anmelden. Deine Sitzung gilt dann auch hier.
      </p>
      <div className="mt-6 flex w-full max-w-xs flex-col gap-2">
        <Button
          variant="primary"
          size="md"
          className="w-full"
          onClick={() => {
            window.location.href = holisticLoginUrl();
          }}
        >
          Bei Holistic anmelden
        </Button>
        <Button variant="secondary" size="md" className="w-full" onClick={() => window.location.reload()}>
          Erneut prüfen
        </Button>
      </div>
    </GateShell>
  );
}

/** Shown when the user IS signed in but lacks the hp_devlab_access right. */
export function AccessDenied({ user }: { user: User }) {
  return (
    <GateShell>
      <p className="mt-1 max-w-sm text-footnote text-text-secondary">
        {user.displayName || user.username ? (
          <>
            Angemeldet als <span className="font-medium text-text-primary">{user.displayName || user.username}</span>.{' '}
          </>
        ) : null}
        Für DevLab fehlt dir die Berechtigung. Ein Administrator kann dir das Recht{' '}
        <code className="rounded bg-fill/10 px-1 py-0.5 text-caption text-text-primary">hp_devlab_access</code>{' '}
        über privleg erteilen.
      </p>
      <div className="mt-6 flex w-full max-w-xs flex-col gap-2">
        <Button variant="secondary" size="md" className="w-full" onClick={() => window.location.reload()}>
          Erneut prüfen
        </Button>
      </div>
    </GateShell>
  );
}
