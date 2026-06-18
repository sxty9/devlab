import { useState } from 'react';
import { CodeIcon } from '@/ui/icons';
import { Button } from '@/ui/Button';

/** Full-screen password gate for the public sxgate preview (read-only, shared password). */
export function LoginGate({ onSubmit }: { onSubmit: (password: string) => Promise<boolean> }) {
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!password.trim() || busy) return;
    setBusy(true);
    setError('');
    const ok = await onSubmit(password).catch(() => false);
    setBusy(false);
    if (!ok) {
      setError('Wrong password.');
      setPassword('');
    }
  };

  return (
    <div className="flex h-full flex-col items-center justify-center bg-bg-base px-6 text-center text-text-primary">
      <div className="relative">
        <div className="absolute inset-0 -z-10 scale-150 rounded-full bg-accent/20 blur-3xl" aria-hidden />
        <span className="flex h-14 w-14 items-center justify-center rounded-2xl bg-surface-raised shadow-elev-3 ring-1 ring-separator">
          <CodeIcon className="h-7 w-7 text-accent" />
        </span>
      </div>
      <h1 className="mt-5 text-title2 font-semibold tracking-tight">DevLab</h1>
      <p className="mt-1 max-w-xs text-footnote text-text-secondary">
        This preview is read-only and password-protected. Enter the shared preview password.
      </p>

      <form onSubmit={submit} className="mt-6 flex w-full max-w-xs flex-col gap-2">
        <input
          type="password"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="Preview password"
          aria-label="Preview password"
          className="w-full rounded-md border border-separator bg-fill/10 px-3 py-2.5 text-center text-footnote text-text-primary placeholder:text-text-tertiary focus:border-accent/50 focus:outline-none focus:ring-2 focus:ring-accent/40"
        />
        {error && <p className="text-caption text-danger">{error}</p>}
        <Button type="submit" variant="primary" size="md" disabled={busy || !password.trim()} className="w-full">
          {busy ? 'Checking…' : 'Enter'}
        </Button>
      </form>

      <p className="mt-6 text-caption text-text-tertiary">Holistic DevLab · preview via sxgate</p>
    </div>
  );
}
