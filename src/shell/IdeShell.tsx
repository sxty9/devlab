import { IconRail } from '@/shell/IconRail';
import { PanelHost } from '@/shell/PanelHost';
import { MainArea } from '@/main/MainArea';
import { StatusBar } from '@/shell/StatusBar';

/** The DevLab IDE: left rail + panel column · editor · pipeline bar. Sits under the shared TopBar. */
export function IdeShell() {
  return (
    <>
      <div className="flex min-h-0 flex-1">
        <IconRail />
        <PanelHost />
        <MainArea />
      </div>
      <StatusBar />
    </>
  );
}
