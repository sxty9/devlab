import { useSession } from '@/state/session';
import { SettingsModal } from './SettingsModal';
import { HelpModal } from './HelpModal';

/** Renders whichever full-screen overlay (Settings / Help) is currently open. */
export function Overlays() {
  const { overlay, setOverlay } = useSession();
  return (
    <>
      <SettingsModal open={overlay === 'settings'} onClose={() => setOverlay(null)} />
      <HelpModal open={overlay === 'help'} onClose={() => setOverlay(null)} />
    </>
  );
}
