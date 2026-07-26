import { SessionProvider, useSession } from '@/state/session';
import { WorkspaceProvider } from '@/state/workspace';
import { ToastProvider } from '@/ui/Toast';
import { TopBar } from '@/shell/TopBar';
import { ViewHost } from '@/views/ViewHost';
import { Overlays } from '@/shell/Overlays';
import { ErrorBoundary } from '@/ui/ErrorBoundary';

/** The chrome: one top bar over whichever view is active — Dashboard, IDE, Mercury or Atlas.
 *
 *  WorkspaceProvider wraps the whole chrome rather than just the IDE, because the top bar's repo and
 *  branch selectors are part of the IDE's state even though they render in shared chrome. It mounts
 *  only once a repo has been opened, and then stays mounted across every view — so unsaved drafts,
 *  the terminal socket and a running agent survive a trip to the dashboard. */
function Shell() {
  const { openedRepo } = useSession();

  const chrome = (
    <div className="flex h-full flex-col overflow-hidden bg-bg-base text-text-primary">
      <TopBar />
      <ViewHost />
    </div>
  );

  return openedRepo ? <WorkspaceProvider repoId={openedRepo}>{chrome}</WorkspaceProvider> : chrome;
}

export default function App() {
  return (
    <SessionProvider>
      <ToastProvider>
        {/* The outermost net: finer boundaries (per view, per detail pane) catch first and keep more of
            the UI alive, so this only ever fires for a crash in the chrome itself (top bar, modals). Even
            then the page shows a recoverable notice rather than going black. */}
        <ErrorBoundary>
          <Shell />
          <Overlays />
        </ErrorBoundary>
      </ToastProvider>
    </SessionProvider>
  );
}
