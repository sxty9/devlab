import { useWorkspace } from '@/state/workspace';
import { SettingsModal } from './SettingsModal';
import { HelpModal } from './HelpModal';

/** Renders whichever full-screen overlay (Settings / Help) is currently open. */
export function Overlays() {
  const { overlay, setOverlay } = useWorkspace();
  return (
    <>
      <SettingsModal open={overlay === 'settings'} onClose={() => setOverlay(null)} />
      <HelpModal open={overlay === 'help'} onClose={() => setOverlay(null)} />
    </>
  );
}
