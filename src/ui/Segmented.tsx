import { cn } from '@/lib/cn';

export interface SegmentedOption<T extends string | number> {
  label: string;
  value: T;
}

const VARIANTS = {
  // Compact raised-active pill — the header/settings segmented picker.
  pill: {
    root: 'inline-flex items-center gap-0.5 rounded-md bg-fill/10 p-0.5',
    button: 'rounded px-3 py-1 text-caption font-medium transition duration-fast',
    active: 'bg-surface-raised text-text-primary shadow-elev-1',
    inactive: 'text-text-secondary hover:text-text-primary',
  },
  // Full-width, bottom-bordered tabs — the sidebar sub-tab strip.
  tabs: {
    root: 'flex gap-1 border-b border-separator p-2',
    button: 'flex-1 rounded-md px-2 py-1 text-center text-caption transition duration-fast',
    active: 'bg-fill/[0.07] font-medium text-text-primary',
    inactive: 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
  },
} as const;

/** A single-choice segmented control, shared across the Mercury surfaces and Settings so the same
 *  picker is never hand-rolled per view. `pill` is the compact raised-active style; `tabs` is the
 *  full-width, bottom-bordered strip. `className` tweaks the wrapper (e.g. `w-fit`). */
export function Segmented<T extends string | number>({
  value,
  options,
  onChange,
  variant = 'pill',
  className,
}: {
  value: T;
  options: SegmentedOption<T>[];
  onChange: (v: T) => void;
  variant?: keyof typeof VARIANTS;
  className?: string;
}) {
  const v = VARIANTS[variant];
  return (
    <div className={cn(v.root, className)}>
      {options.map((o) => (
        <button
          key={String(o.value)}
          type="button"
          onClick={() => onChange(o.value)}
          className={cn(v.button, value === o.value ? v.active : v.inactive)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
