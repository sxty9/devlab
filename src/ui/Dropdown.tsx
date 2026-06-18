import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
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

/**
 * A click-to-open popover menu. The menu is rendered in a PORTAL to document.body — this
 * escapes the top bar's `backdrop-filter` ancestor (which otherwise makes descendant popovers
 * appear to "bleed through" their opaque background on some browsers). Includes outside-click +
 * Escape dismissal, roving keyboard focus, and focus return to the trigger.
 */
export function Dropdown({ trigger, ariaLabel, align = 'start', children, triggerClassName, menuClassName }: DropdownProps) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left?: number; right?: number; minWidth: number } | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const items = () => Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);

  const place = () => {
    const r = triggerRef.current?.getBoundingClientRect();
    if (!r) return;
    setPos({
      top: r.bottom + 6,
      ...(align === 'end' ? { right: window.innerWidth - r.right } : { left: r.left }),
      minWidth: r.width,
    });
  };

  const close = (returnFocus = true) => {
    setOpen(false);
    if (returnFocus) triggerRef.current?.focus();
  };

  useLayoutEffect(() => {
    if (open) place();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (!open) return;
    items()[0]?.focus();
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!triggerRef.current?.contains(t) && !menuRef.current?.contains(t)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        close();
      }
    };
    const onReflow = () => place();
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', onReflow);
    window.addEventListener('scroll', onReflow, true);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('resize', onReflow);
      window.removeEventListener('scroll', onReflow, true);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const onMenuKeyDown = (e: React.KeyboardEvent) => {
    const list = items();
    if (!list.length) return;
    const idx = list.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      list[(idx + 1 + list.length) % list.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      list[(idx - 1 + list.length) % list.length]?.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      list[0]?.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      list[list.length - 1]?.focus();
    }
  };

  return (
    <div className="contents">
      <button
        ref={triggerRef}
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
      {open &&
        pos &&
        createPortal(
          <div
            ref={menuRef}
            role="menu"
            aria-label={ariaLabel}
            onKeyDown={onMenuKeyDown}
            style={{ position: 'fixed', top: pos.top, left: pos.left, right: pos.right, minWidth: pos.minWidth }}
            className={cn(
              'z-[200] max-h-[70vh] origin-top overflow-y-auto rounded-md border border-separator',
              'bg-surface-raised p-1 shadow-elev-3 ring-1 ring-black/20 animate-pop-in dl-scroll',
              menuClassName,
            )}
          >
            {children(() => close())}
          </div>,
          document.body,
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
      aria-current={selected}
      onClick={onClick}
      className={cn(
        'flex w-full items-center gap-2.5 rounded-sm px-2 py-1.5 text-left transition-colors duration-fast hover:bg-fill/10 focus:bg-fill/15 focus:outline-none',
        selected && 'bg-accent/10',
      )}
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
