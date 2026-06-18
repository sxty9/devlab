import { useEffect, useRef, useState, type ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { CheckIcon, ChevronDownIcon } from './icons';

interface DropdownProps {
  /** Inline content of the trigger button (left of the caret). */
  trigger: ReactNode;
  ariaLabel: string;
  align?: 'start' | 'end';
  /** Render the menu body; call `close` after an item is chosen. */
  children: (close: () => void) => ReactNode;
  triggerClassName?: string;
  menuClassName?: string;
}

/** A click-to-open popover menu with outside-click + Escape dismissal. */
export function Dropdown({ trigger, ariaLabel, align = 'start', children, triggerClassName, menuClassName }: DropdownProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={ariaLabel}
        onClick={() => setOpen((o) => !o)}
        className={cn(
          'inline-flex h-7 max-w-[16rem] items-center gap-1.5 rounded-md px-2 text-subhead text-text-primary',
          'transition duration-fast ease-out hover:bg-fill/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50',
          open && 'bg-fill/10',
          triggerClassName,
        )}
      >
        {trigger}
        <ChevronDownIcon className={cn('h-3.5 w-3.5 shrink-0 text-text-tertiary transition-transform', open && 'rotate-180')} />
      </button>
      {open && (
        <div
          role="menu"
          className={cn(
            'absolute z-50 mt-1.5 min-w-[15rem] origin-top overflow-hidden rounded-md border border-separator',
            'bg-material-thick p-1 shadow-elev-3 [backdrop-filter:var(--material-blur)] animate-pop-in',
            align === 'end' ? 'right-0' : 'left-0',
            menuClassName,
          )}
        >
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  );
}

/** A standard menu row. Pass `selected` to show a trailing check. */
export function DropdownItem({
  onClick,
  selected,
  leading,
  title,
  hint,
  trailing,
}: {
  onClick: () => void;
  selected?: boolean;
  leading?: ReactNode;
  title: ReactNode;
  hint?: ReactNode;
  trailing?: ReactNode;
}) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      className="flex w-full items-center gap-2.5 rounded-sm px-2 py-1.5 text-left transition-colors duration-fast hover:bg-fill/10"
    >
      {leading != null && <span className="flex h-5 w-5 shrink-0 items-center justify-center">{leading}</span>}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-subhead text-text-primary">{title}</span>
        {hint != null && <span className="block truncate text-caption text-text-tertiary">{hint}</span>}
      </span>
      {trailing}
      {selected && <CheckIcon className="h-4 w-4 shrink-0 text-accent" />}
    </button>
  );
}

export function DropdownLabel({ children }: { children: ReactNode }) {
  return <div className="px-2 pb-1 pt-1.5 text-caption font-medium uppercase tracking-wide text-text-tertiary">{children}</div>;
}

export function DropdownSeparator() {
  return <div className="my-1 h-px bg-separator" />;
}
