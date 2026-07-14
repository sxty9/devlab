import { useSession } from '@/state/session';
import { IdeShell } from '@/shell/IdeShell';
import { Dashboard } from '@/views/Dashboard';
import { MercuryView } from '@/views/MercuryView';
import { AtlasView } from '@/views/AtlasView';

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
      {view.kind === 'dashboard' && <Dashboard />}
      {view.kind === 'mercury' && <MercuryView />}
      {view.kind === 'atlas' && <AtlasView />}
      {openedRepo && (
        <div className={view.kind === 'ide' ? 'contents' : 'hidden'}>
          <IdeShell />
        </div>
      )}
    </>
  );
}
