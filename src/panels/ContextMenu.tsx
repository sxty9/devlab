import { useEffect, useRef } from 'react';
import { cn } from '@/lib/cn';
import type { MenuEntry } from './treeOps';

/** Where a context menu was opened and what it acts on. */
export interface MenuAt<T> {
  x: number;
  y: number;
  subject: T;
}

/** The ONE context menu of the IDE surfaces (file tree, changes, editor tabs): the entries are
 *  decided by the surface (treeOps), the rendering and dismissal happen here — so a right click
 *  behaves identically everywhere instead of once per panel. It closes on Escape, on an outside
 *  click and after a choice, and it keeps itself inside the viewport. */
export function ContextMenu({
  x,
  y,
  entries,
  onChoose,
  onClose,
}: {
  x: number;
  y: number;
  entries: MenuEntry[];
  onChoose: (id: string) => void;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('mousedown', onDown);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('mousedown', onDown);
      window.removeEventListener('keydown', onKey);
    };
  }, [onClose]);

  // Keep the menu on screen: flip it back inside when it would overflow.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    if (r.right > window.innerWidth) el.style.left = `${Math.max(0, window.innerWidth - r.width - 4)}px`;
    if (r.bottom > window.innerHeight) el.style.top = `${Math.max(0, window.innerHeight - r.height - 4)}px`;
  }, [x, y, entries]);

  if (entries.length === 0) return null;

  return (
    <div
      ref={ref}
      role="menu"
      style={{ left: x, top: y }}
      className="fixed z-50 min-w-40 rounded-md border border-separator bg-surface-raised py-1 shadow-elev-2"
    >
      {entries.map((e) => (
        <button
          key={e.id}
          type="button"
          role="menuitem"
          onClick={() => {
            onChoose(e.id);
            onClose();
          }}
          className={cn(
            'block w-full px-3 py-1.5 text-left text-footnote transition duration-fast hover:bg-fill/10',
            e.danger ? 'text-danger' : 'text-text-primary',
          )}
        >
          {e.label}
        </button>
      ))}
    </div>
  );
}
