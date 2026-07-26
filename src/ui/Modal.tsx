import { useEffect, useRef, type ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { IconButton } from './Button';
import { XIcon } from './icons';

/** A centered glass modal with backdrop, Escape-to-close, and an accessible dialog role. */
export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = 'md',
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  children: ReactNode;
  footer?: ReactNode;
  size?: 'sm' | 'md' | 'lg' | 'xl';
}) {
  const cardRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onClose();
      }
    };
    document.addEventListener('keydown', onKey);
    cardRef.current?.focus();
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-black/60 animate-overlay-in" onClick={onClose} />
      <div
        ref={cardRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={cn(
          'relative w-full origin-center overflow-hidden rounded-card border border-separator bg-surface-raised',
          'shadow-elev-3 ring-1 ring-black/5 animate-pop-in focus:outline-none',
          size === 'sm' ? 'max-w-sm' : size === 'lg' ? 'max-w-2xl' : size === 'xl' ? 'max-w-5xl' : 'max-w-md',
        )}
      >
        <div className="flex items-start gap-3 border-b border-separator px-5 py-4">
          <div className="min-w-0">
            <h2 className="text-title3 font-semibold tracking-tight text-text-primary">{title}</h2>
            {description && <p className="mt-0.5 text-footnote text-text-secondary">{description}</p>}
          </div>
          <IconButton label="Close" onClick={onClose} className="-mr-1.5 -mt-1 ml-auto">
            <XIcon className="h-4 w-4" />
          </IconButton>
        </div>
        <div className="dl-scroll max-h-[70vh] overflow-y-auto px-5 py-4">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-separator px-5 py-3">{footer}</div>}
      </div>
    </div>
  );
}
