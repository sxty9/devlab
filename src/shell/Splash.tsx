import { CodeIcon } from '@/ui/icons';

/** The boot placeholder: shown while the session resolves, and while a repo's data loads. */
export function Splash() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 bg-bg-base text-text-primary">
      <span className="flex h-12 w-12 animate-pulse items-center justify-center rounded-2xl bg-surface-raised shadow-elev-2 ring-1 ring-separator">
        <CodeIcon className="h-6 w-6 text-accent" />
      </span>
      <p className="text-footnote text-text-tertiary">Loading workspace…</p>
    </div>
  );
}
