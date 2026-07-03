import { useWorkspace } from '@/state/workspace';
import { TabStrip } from './TabStrip';
import { EditorView } from './EditorView';
import { DiffView } from './DiffView';
import { StructureView } from './StructureView';
import { VisionView } from './VisionView';
import { WelcomeView } from './WelcomeView';

/** The editor region: tab strip + the active tab's body (code / structure / welcome). */
export function MainArea() {
  const { openTabs, activeTabId, fileContent, activeRepo } = useWorkspace();
  const activeTab = openTabs.find((t) => t.id === activeTabId) ?? null;

  return (
    <main className="flex min-h-0 min-w-0 flex-1 flex-col bg-bg-base">
      {openTabs.length > 0 && <TabStrip />}
      {!activeTab && <WelcomeView />}
      {activeTab?.kind === 'structure' && <StructureView />}
      {activeTab?.kind === 'diff' && activeTab.path && <DiffView key={`${activeRepo.id}/diff/${activeTab.path}`} path={activeTab.path} />}
      {activeTab?.kind === 'vision' && activeTab.path && <VisionView key={`${activeRepo.id}/vision/${activeTab.path}`} path={activeTab.path} />}
      {activeTab?.kind === 'code' && activeTab.path && (
        <EditorView
          key={`${activeRepo.id}/${activeTab.path}`}
          repoId={activeRepo.id}
          path={activeTab.path}
          lang={fileContent(activeTab.path).lang}
        />
      )}
    </main>
  );
}
