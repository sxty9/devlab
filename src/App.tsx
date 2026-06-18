import { WorkspaceProvider } from '@/state/workspace';
import { ToastProvider } from '@/ui/Toast';
import { TopBar } from '@/shell/TopBar';
import { IconRail } from '@/shell/IconRail';
import { PanelHost } from '@/shell/PanelHost';
import { MainArea } from '@/main/MainArea';
import { StatusBar } from '@/shell/StatusBar';
import { Overlays } from '@/shell/Overlays';

/** The DevLab IDE shell: top bar · left rail + panel column · editor · pipeline bar. */
export default function App() {
  return (
    <WorkspaceProvider>
      <ToastProvider>
        <div className="flex h-full flex-col overflow-hidden bg-bg-base text-text-primary">
          <TopBar />
          <div className="flex min-h-0 flex-1">
            <IconRail />
            <PanelHost />
            <MainArea />
          </div>
          <StatusBar />
        </div>
        <Overlays />
      </ToastProvider>
    </WorkspaceProvider>
  );
}
