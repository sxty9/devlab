import { useWorkspace } from '@/state/workspace';
import { IconRail } from '@/shell/IconRail';
import { PanelHost } from '@/shell/PanelHost';
import { ClaudePanel } from '@/panels/ClaudePanel';
import { MainArea } from '@/main/MainArea';
import { StatusBar } from '@/shell/StatusBar';

/** The DevLab IDE: tool rail + panel column · editor · AI sidebar + AI rail · status bar. Sits
 *  under the shared TopBar.
 *
 *  The AI lives on the RIGHT — sidebar and symbol on the same side (REQ-043.2) — while the
 *  workspace tools stay on the left. Both rails drive the one `activePanel` state, so exactly one
 *  panel is open at a time and there is no second source of truth for which tool is showing. */
export function IdeShell() {
  const { activePanel } = useWorkspace();
  const aiOpen = activePanel === 'claude';

  return (
    <>
      <div className="flex min-h-0 flex-1">
        <IconRail side="left" />
        {!aiOpen && <PanelHost />}
        <MainArea />
        {aiOpen && (
          <aside className="flex min-h-0 w-[22rem] shrink-0 flex-col overflow-hidden border-l border-separator bg-surface-sidebar">
            <ClaudePanel />
          </aside>
        )}
        <IconRail side="right" />
      </div>
      <StatusBar />
    </>
  );
}
