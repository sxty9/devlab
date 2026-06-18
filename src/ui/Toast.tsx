import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { CheckIcon, DotIcon, XIcon } from './icons';

type Variant = 'default' | 'success' | 'danger';

interface Toast {
  id: number;
  title: string;
  description?: string;
  variant: Variant;
}

interface ToastContextValue {
  toast: (t: { title: string; description?: string; variant?: Variant; duration?: number }) => void;
}

const ToastContext = createContext<ToastContextValue | null>(null);

const accent: Record<Variant, string> = {
  default: 'text-accent',
  success: 'text-success',
  danger: 'text-danger',
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const idRef = useRef(0);

  const dismiss = useCallback((id: number) => setToasts((t) => t.filter((x) => x.id !== id)), []);

  const toast = useCallback<ToastContextValue['toast']>(
    ({ title, description, variant = 'default', duration = 3400 }) => {
      const id = ++idRef.current;
      setToasts((t) => [...t, { id, title, description, variant }]);
      window.setTimeout(() => dismiss(id), duration);
    },
    [dismiss],
  );

  return (
    <ToastContext.Provider value={{ toast }}>
      {children}
      <div className="pointer-events-none fixed bottom-4 right-4 z-[120] flex w-80 max-w-[calc(100vw-2rem)] flex-col gap-2">
        {toasts.map((t) => (
          <div
            key={t.id}
            role="status"
            className="pointer-events-auto flex items-start gap-2.5 rounded-md border border-separator bg-surface-raised p-3 shadow-elev-3 ring-1 ring-black/5 animate-toast-in"
          >
            <span className={cn('mt-0.5 shrink-0', accent[t.variant])}>
              {t.variant === 'success' ? <CheckIcon className="h-4 w-4" /> : <DotIcon className="h-3.5 w-3.5" />}
            </span>
            <div className="min-w-0 flex-1">
              <p className="text-footnote font-medium text-text-primary">{t.title}</p>
              {t.description && <p className="mt-0.5 text-caption text-text-secondary">{t.description}</p>}
            </div>
            <button
              type="button"
              aria-label="Dismiss"
              onClick={() => dismiss(t.id)}
              className="-mr-1 -mt-1 flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-text-tertiary transition hover:bg-fill/10 hover:text-text-primary"
            >
              <XIcon className="h-3.5 w-3.5" />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used within <ToastProvider>');
  return ctx;
}
