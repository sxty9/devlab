import { CodeIcon } from '@/ui/icons';

/** DevLab brand lockup: a glassy tile with code brackets + the wordmark. */
export function Brand() {
  return (
    <div className="flex select-none items-center gap-2 pl-1 pr-1">
      <span className="flex h-6 w-6 items-center justify-center rounded-md bg-surface-raised shadow-elev-1 ring-1 ring-separator">
        <CodeIcon className="h-3.5 w-3.5 text-accent" />
      </span>
      <span className="text-subhead font-semibold tracking-[-0.02em] text-text-primary">DevLab</span>
    </div>
  );
}
