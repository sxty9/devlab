import { useSession } from '@/state/session';
import { IdeShell } from '@/shell/IdeShell';
import { Dashboard } from '@/views/Dashboard';
import { MercuryView } from '@/views/mercury/MercuryView';
import { AtlasView } from '@/views/AtlasView';
import { ErrorBoundary } from '@/ui/ErrorBoundary';

/** The single view gate: Dashboard · IDE · Mercury · Atlas.
 *
 *  Once a repo has been opened the IDE stays MOUNTED and is merely hidden behind the other views —
 *  unmounting it would throw away unsaved editor drafts, drop the terminal's WebSocket and abort a
 *  running agent every time you glanced at the dashboard. Monaco (automaticLayout) and xterm (a
 *  ResizeObserver on its host) both re-measure themselves when the column is shown again. */
export function ViewHost() {
  const { view, openedRepo } = useSession();

  return (
    <>
      {/* A render crash in a data-driven view must never blank the whole app — this contains it to the
          content area (the top bar stays), and resetting on view.kind lets switching tabs recover. The
          always-mounted IDE gets its OWN boundary so a crash on one side can't unmount the other (which
          would drop the IDE's unsaved drafts, terminal socket and running agent, and vice-versa). */}
      <ErrorBoundary resetKeys={[view.kind]}>
        {view.kind === 'dashboard' && <Dashboard />}
        {view.kind === 'mercury' && <MercuryView />}
        {view.kind === 'atlas' && <AtlasView />}
      </ErrorBoundary>
      {openedRepo && (
        <div className={view.kind === 'ide' ? 'contents' : 'hidden'}>
          <ErrorBoundary>
            <IdeShell />
          </ErrorBoundary>
        </div>
      )}
    </>
  );
}
