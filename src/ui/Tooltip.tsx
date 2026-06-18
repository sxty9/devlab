import { type ReactNode } from 'react';
import { cn } from '@/lib/cn';

type Side = 'right' | 'top' | 'bottom' | 'left';

const sideClasses: Record<Side, string> = {
  right: 'left-full top-1/2 ml-2 -translate-y-1/2',
  left: 'right-full top-1/2 mr-2 -translate-y-1/2',
  top: 'bottom-full left-1/2 mb-2 -translate-x-1/2',
  bottom: 'top-full left-1/2 mt-2 -translate-x-1/2',
};

/** Lightweight hover/focus tooltip — pure CSS via group-hover, no portal. */
export function Tooltip({ label, side = 'right', children }: { label: ReactNode; side?: Side; children: ReactNode }) {
  return (
    <span className="group/tt relative inline-flex">
      {children}
      <span
        role="tooltip"
        className={cn(
          'pointer-events-none absolute z-[60] whitespace-nowrap rounded-md border border-separator bg-surface-raised px-2 py-1',
          'text-caption text-text-primary shadow-elev-2',
          'opacity-0 scale-95 transition duration-fast ease-out',
          'group-hover/tt:opacity-100 group-hover/tt:scale-100 group-focus-within/tt:opacity-100 group-focus-within/tt:scale-100',
          sideClasses[side],
        )}
      >
        {label}
      </span>
    </span>
  );
}
