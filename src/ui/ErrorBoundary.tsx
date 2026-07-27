import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button } from '@/ui/Button';
import { RefreshIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

type FallbackRender = (error: Error, reset: () => void) => ReactNode;

interface ErrorBoundaryProps {
  children: ReactNode;
  /** A custom fallback — a fixed node, or a render fn given the error and a reset callback. */
  fallback?: ReactNode | FallbackRender;
  /** When any value here changes between renders, the boundary clears its error and re-renders its
   *  children. Pass the identity of what's on screen (e.g. the selected entry's id) so navigating to
   *  another, healthy entry recovers on its own. */
  resetKeys?: unknown[];
  /** Extra classes for the default fallback's centering wrapper. */
  className?: string;
}

interface ErrorBoundaryState {
  error: Error | null;
}

/** A render-error firewall. When a component throws during render and nothing catches it, React
 *  unmounts the WHOLE tree — on Holistic's dark shell that leaves a black screen. This contains the
 *  blast radius: only the wrapped subtree is replaced, by a small recoverable notice, while the rest of
 *  the surface (top bar, list, siblings) stays alive. Reused wherever data-driven content could meet a
 *  bad or partial record — a dead run, an errored execution — so one such entry never takes the page
 *  down. `resetKeys` auto-clear the error when the viewer moves on (a new selection, a switched view). */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidUpdate(prev: ErrorBoundaryProps) {
    // A stale error must not outlive the thing that caused it: once the caller's resetKeys change
    // (a different entry selected, a different view shown) drop the error so the new children render.
    if (this.state.error && !sameKeys(prev.resetKeys, this.props.resetKeys)) {
      this.reset();
    }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Keep the crash diagnosable even though the UI stays up — the boundary never re-throws.
    console.error('ErrorBoundary caught a render error:', error, info.componentStack);
  }

  private reset = () => this.setState({ error: null });

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    const { fallback, className } = this.props;
    if (typeof fallback === 'function') return (fallback as FallbackRender)(error, this.reset);
    if (fallback !== undefined) return fallback;

    return (
      <div className={cn('flex h-full min-h-0 flex-col items-center justify-center gap-3 bg-bg-base px-6 text-center', className)}>
        <p className="max-w-md text-footnote text-text-secondary">Diese Ansicht konnte nicht angezeigt werden.</p>
        <Button variant="secondary" size="sm" onClick={this.reset}>
          <RefreshIcon className="h-4 w-4" /> Erneut versuchen
        </Button>
      </div>
    );
  }
}

/** Shallow (Object.is) compare of two resetKeys arrays. */
function sameKeys(a?: unknown[], b?: unknown[]): boolean {
  if (a === b) return true;
  if (!a || !b || a.length !== b.length) return false;
  return a.every((v, i) => Object.is(v, b[i]));
}
