import { useSession } from '@/state/session';
import { CodeIcon } from '@/ui/icons';

/** DevLab brand lockup: a glassy tile with code brackets + the wordmark. Also the way home — it is
 *  the single affordance back to the Dashboard from any capability. */
export function Brand() {
  const { view, goHome } = useSession();
  const atHome = view.kind === 'dashboard';

  return (
    <button
      type="button"
      onClick={goHome}
      disabled={atHome}
      aria-label="DevLab dashboard"
      className="flex select-none items-center gap-2 rounded-md pl-1 pr-1.5 transition duration-fast ease-out hover:bg-fill/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50 disabled:pointer-events-none"
    >
      <span className="flex h-6 w-6 items-center justify-center rounded-md bg-surface-raised shadow-elev-1 ring-1 ring-separator">
        <CodeIcon className="h-3.5 w-3.5 text-accent" />
      </span>
      <span className="text-subhead font-semibold tracking-[-0.02em] text-text-primary">DevLab</span>
    </button>
  );
}
