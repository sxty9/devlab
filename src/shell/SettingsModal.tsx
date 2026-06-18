import { useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { Modal } from '@/ui/Modal';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';

type Theme = 'dark' | 'light';

function Segmented<T extends string | number>({
  value,
  options,
  onChange,
}: {
  value: T;
  options: { label: string; value: T }[];
  onChange: (v: T) => void;
}) {
  return (
    <div className="inline-flex rounded-md bg-fill/10 p-0.5">
      {options.map((o) => (
        <button
          key={String(o.value)}
          type="button"
          onClick={() => onChange(o.value)}
          className={cn(
            'rounded-sm px-3 py-1 text-caption font-medium transition',
            value === o.value ? 'bg-surface-raised text-text-primary shadow-elev-1' : 'text-text-secondary hover:text-text-primary',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 py-3">
      <div className="min-w-0">
        <p className="text-subhead text-text-primary">{label}</p>
        {hint && <p className="text-caption text-text-tertiary">{hint}</p>}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}

export function SettingsModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { settings, updateSettings } = useWorkspace();
  const [theme, setTheme] = useState<Theme>(
    () => (document.documentElement.getAttribute('data-theme') as Theme) || 'dark',
  );

  const applyTheme = (t: Theme) => {
    document.documentElement.setAttribute('data-theme', t);
    try {
      localStorage.setItem('dl.theme', t);
    } catch {
      /* ignore */
    }
    setTheme(t);
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Settings"
      description="Appearance & editor preferences (saved locally)."
      footer={
        <Button variant="primary" size="sm" onClick={onClose}>
          Done
        </Button>
      }
    >
      <div className="divide-y divide-separator">
        <Row label="Theme" hint="Apple-dark glass · or light">
          <Segmented<Theme>
            value={theme}
            onChange={applyTheme}
            options={[
              { label: 'Dark', value: 'dark' },
              { label: 'Light', value: 'light' },
            ]}
          />
        </Row>
        <Row label="Editor font size" hint={`${settings.fontSize}px`}>
          <div className="inline-flex items-center gap-1.5">
            <Button size="sm" variant="secondary" onClick={() => updateSettings({ fontSize: Math.max(10, settings.fontSize - 1) })} aria-label="Decrease font size">
              −
            </Button>
            <span className="w-8 text-center font-mono text-subhead text-text-primary">{settings.fontSize}</span>
            <Button size="sm" variant="secondary" onClick={() => updateSettings({ fontSize: Math.min(22, settings.fontSize + 1) })} aria-label="Increase font size">
              +
            </Button>
          </div>
        </Row>
        <Row label="Tab size" hint="Spaces per indent">
          <Segmented<number>
            value={settings.tabSize}
            onChange={(v) => updateSettings({ tabSize: v })}
            options={[
              { label: '2', value: 2 },
              { label: '4', value: 4 },
              { label: '8', value: 8 },
            ]}
          />
        </Row>
      </div>
    </Modal>
  );
}
