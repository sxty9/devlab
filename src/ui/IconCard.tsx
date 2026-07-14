import type { Repo } from '@/types';
import { tintSoftBg, tintText } from './tint';
import { cn } from '@/lib/cn';

interface IconCardProps {
  icon: (p: { className?: string }) => JSX.Element;
  tint: Repo['tint'];
  title: string;
  /** The single secondary line — the repo's language, or a capability's role. */
  subtitle?: string;
  disabled?: boolean;
  onClick: () => void;
}

/** The dashboard tile. Both dashboard groups — repositories and capabilities — render through this
 *  one component, so "same style" is structural rather than a copy-paste that drifts apart. */
export function IconCard({ icon: Icon, tint, title, subtitle, disabled, onClick }: IconCardProps) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'flex flex-col items-center gap-2 rounded-card border border-separator bg-surface px-3 py-4',
        'shadow-elev-1 transition duration-fast ease-out',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50',
        disabled ? 'opacity-40' : 'hover:-translate-y-0.5 hover:border-accent/40 hover:shadow-elev-2',
      )}
    >
      <span className={cn('flex h-12 w-12 items-center justify-center rounded-card ring-1 ring-separator', tintSoftBg[tint])}>
        <Icon className={cn('h-6 w-6', tintText[tint])} />
      </span>
      <span className="w-full truncate text-footnote font-medium text-text-primary">{title}</span>
      {subtitle && <span className="w-full truncate text-caption text-text-tertiary">{subtitle}</span>}
    </button>
  );
}
