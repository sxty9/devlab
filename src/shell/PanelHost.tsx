import { useCallback, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { Splitter } from '@/components/Splitter';
import { VisionPanel } from '@/panels/VisionPanel';
import { ProjectPanel } from '@/panels/ProjectPanel';
import { VersionControlPanel } from '@/panels/VersionControlPanel';
import { ClaudePanel } from '@/panels/ClaudePanel';
import { TerminalPanel } from '@/panels/TerminalPanel';

const MIN = 232;
const MAX = 560;
const STORAGE_KEY = 'dl.panelWidth';

const clamp = (n: number) => Math.max(MIN, Math.min(MAX, n));

function readWidth(): number {
  const raw = typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) : null;
  const n = raw ? Number(raw) : NaN;
  return Number.isFinite(n) ? clamp(n) : 288;
}

/** The resizable left panel column. Renders the panel for the active rail tool, or nothing
 *  when the column is collapsed. */
export function PanelHost() {
  const { activePanel } = useWorkspace();
  const [width, setWidth] = useState(readWidth);

  const onResize = useCallback((dx: number) => {
    setWidth((w) => {
      const next = clamp(w + dx);
      try {
        localStorage.setItem(STORAGE_KEY, String(next));
      } catch {
        /* ignore quota/availability errors */
      }
      return next;
    });
  }, []);

  if (!activePanel) return null;

  return (
    <>
      <aside
        style={{ width }}
        className="flex min-h-0 shrink-0 flex-col overflow-hidden border-r border-separator bg-surface-sidebar"
      >
        {activePanel === 'vision' && <VisionPanel />}
        {activePanel === 'project' && <ProjectPanel />}
        {activePanel === 'vcs' && <VersionControlPanel />}
        {activePanel === 'claude' && <ClaudePanel />}
        {activePanel === 'terminal' && <TerminalPanel />}
      </aside>
      <Splitter onResize={onResize} ariaLabel="Resize side panel" />
    </>
  );
}
