import { Modal } from '@/ui/Modal';
import { ClaudeIcon, FilesIcon, GitBranchIcon, LightbulbIcon, TerminalIcon } from '@/ui/icons';

const isMac = typeof navigator !== 'undefined' && /Mac|iP(hone|ad)/.test(navigator.platform);
const mod = isMac ? '⌘' : 'Ctrl';

const SHORTCUTS: { keys: string[]; label: string }[] = [
  { keys: [mod, 'B'], label: 'Toggle the side panel column' },
  { keys: ['?'], label: 'Open this shortcuts reference' },
  { keys: ['Esc'], label: 'Close a dialog or menu' },
  { keys: ['Enter'], label: 'Send (Claude) · run (Terminal)' },
  { keys: ['↑', '↓'], label: 'Navigate an open menu' },
];

const TOOLS: { Icon: (p: { className?: string }) => JSX.Element; name: string; desc: string }[] = [
  { Icon: LightbulbIcon, name: 'Vision', desc: 'Specs, mindmaps & jets — the front of the pipeline' },
  { Icon: FilesIcon, name: 'Project', desc: 'Browse and open the repo file tree' },
  { Icon: GitBranchIcon, name: 'Version Control', desc: 'Staged & unstaged changes, commit' },
  { Icon: ClaudeIcon, name: 'Claude', desc: 'A Claude session per repo' },
  { Icon: TerminalIcon, name: 'Terminal', desc: 'A shell per repo' },
];

function Key({ children }: { children: React.ReactNode }) {
  return (
    <kbd className="inline-flex h-6 min-w-[24px] items-center justify-center rounded-sm border border-separator bg-fill/10 px-1.5 font-mono text-caption text-text-primary">
      {children}
    </kbd>
  );
}

export function HelpModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <Modal open={open} onClose={onClose} title="DevLab" description="Develop, maintain & ship Holistic services — from vision to preview to prod." size="lg">
      <div className="grid gap-6 sm:grid-cols-2">
        <div>
          <h3 className="mb-2 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Keyboard</h3>
          <ul className="space-y-2">
            {SHORTCUTS.map((s) => (
              <li key={s.label} className="flex items-center gap-3">
                <span className="flex shrink-0 gap-1">
                  {s.keys.map((k) => (
                    <Key key={k}>{k}</Key>
                  ))}
                </span>
                <span className="text-footnote text-text-secondary">{s.label}</span>
              </li>
            ))}
          </ul>
        </div>
        <div>
          <h3 className="mb-2 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Tools</h3>
          <ul className="space-y-2.5">
            {TOOLS.map((t) => (
              <li key={t.name} className="flex items-start gap-2.5">
                <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-fill/10 text-text-secondary">
                  <t.Icon className="h-4 w-4" />
                </span>
                <span>
                  <span className="block text-footnote font-medium text-text-primary">{t.name}</span>
                  <span className="block text-caption text-text-tertiary">{t.desc}</span>
                </span>
              </li>
            ))}
          </ul>
        </div>
      </div>
      <p className="mt-6 border-t border-separator pt-4 text-caption text-text-tertiary">
        Phase 1 — UI shell with mock data, preview-deployed via sxgate. Git, Claude, terminal &amp; live preview arrive with the backend.
      </p>
    </Modal>
  );
}
