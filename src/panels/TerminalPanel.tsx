import { useEffect, useRef, useState } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { useWorkspace } from '@/state/workspace';
import { getDataSource } from '@/data';
import { PanelHeader } from './PanelHeader';
import { IconButton } from '@/ui/Button';
import { RefreshIcon } from '@/ui/icons';

type Status = 'connecting' | 'connected' | 'closed' | 'offline';

const statusText: Record<Status, string> = {
  connecting: 'connecting…',
  connected: 'connected',
  closed: 'disconnected',
  offline: 'offline (mock)',
};
const statusColor: Record<Status, string> = {
  connecting: 'text-warning',
  connected: 'text-success',
  closed: 'text-text-tertiary',
  offline: 'text-text-tertiary',
};

// xtermTheme derives an xterm palette from the app's live design tokens so the terminal
// matches the active light/dark theme. Background/foreground follow --bg-base; the ANSI 16
// are a stable, legible set that reads on either background.
function xtermTheme(): NonNullable<ConstructorParameters<typeof Terminal>[0]>['theme'] {
  const css = getComputedStyle(document.documentElement);
  const bg = css.getPropertyValue('--bg-base').trim() || '#000000';
  const dark = isDark(bg);
  return {
    background: bg,
    foreground: dark ? '#e6e6eb' : '#1c1c1e',
    cursor: '#0a84ff',
    cursorAccent: bg,
    selectionBackground: dark ? 'rgba(255,255,255,0.20)' : 'rgba(0,0,0,0.15)',
    black: dark ? '#4b4f57' : '#2a2b30',
    red: '#ff5f56',
    green: '#34c759',
    yellow: dark ? '#ffd60a' : '#b8860b',
    blue: '#0a84ff',
    magenta: '#ff6ac1',
    cyan: '#5ac8fa',
    white: dark ? '#d5d5da' : '#3c3c43',
    brightBlack: '#6c7079',
    brightRed: '#ff6b60',
    brightGreen: '#30d158',
    brightYellow: '#ffe14d',
    brightBlue: '#4d9bff',
    brightMagenta: '#ff8ad4',
    brightCyan: '#7ad4fb',
    brightWhite: dark ? '#ffffff' : '#1c1c1e',
  };
}

// isDark reports whether a CSS background color is dark (so we pick a light foreground).
function isDark(color: string): boolean {
  const hex = color.startsWith('#') ? color : '';
  if (hex.length >= 7) {
    const r = parseInt(hex.slice(1, 3), 16);
    const g = parseInt(hex.slice(3, 5), 16);
    const b = parseInt(hex.slice(5, 7), 16);
    return 0.299 * r + 0.587 * g + 0.114 * b < 128;
  }
  return true; // default to a dark assumption when we can't parse (safer contrast)
}

export function TerminalPanel() {
  const { activeRepo } = useWorkspace();
  const hostRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<Status>('connecting');
  // Bumping this nonce tears down and re-opens the terminal (the reconnect button).
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const url = getDataSource().terminalUrl(activeRepo.id);
    const term = new Terminal({
      fontFamily: 'var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
      fontSize: 12.5,
      lineHeight: 1.2,
      cursorBlink: true,
      scrollback: 5000,
      theme: xtermTheme(),
      allowProposedApi: true,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    const safeFit = () => {
      try {
        fit.fit();
      } catch {
        /* container not laid out yet */
      }
    };
    safeFit();

    // Offline / mock: no live provider. Show a notice instead of dialling.
    if (!url) {
      setStatus('offline');
      term.writeln('\x1b[90mTerminal is offline (mock data source).\x1b[0m');
      term.writeln('\x1b[90mRun against the devlabd backend for a real shell in this workspace.\x1b[0m');
      const ro = new ResizeObserver(safeFit);
      ro.observe(host);
      return () => {
        ro.disconnect();
        term.dispose();
      };
    }

    setStatus('connecting');
    const ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    const encoder = new TextEncoder();
    let closed = false;

    const sendResize = () => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }));
      }
    };

    ws.onopen = () => {
      setStatus('connected');
      safeFit();
      sendResize();
      term.focus();
    };
    ws.onmessage = (e) => {
      if (typeof e.data === 'string') {
        // Control frame (e.g. a remshel error JSON) — surface its message dimly.
        try {
          const c = JSON.parse(e.data) as { type?: string; message?: string };
          if (c.message) term.writeln(`\x1b[33m${c.message}\x1b[0m`);
        } catch {
          /* ignore non-JSON text */
        }
        return;
      }
      term.write(new Uint8Array(e.data as ArrayBuffer));
    };
    ws.onclose = () => {
      if (closed) return;
      setStatus('closed');
      term.writeln('\r\n\x1b[90m[session ended]\x1b[0m');
    };
    ws.onerror = () => setStatus('closed');

    const onData = term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(d));
    });

    const ro = new ResizeObserver(() => {
      safeFit();
      sendResize();
    });
    ro.observe(host);

    return () => {
      closed = true;
      ro.disconnect();
      onData.dispose();
      try {
        ws.close();
      } catch {
        /* already closing */
      }
      term.dispose();
    };
  }, [activeRepo.id, nonce]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Terminal"
        actions={
          <>
            <span className={`mr-1 text-[11px] ${statusColor[status]}`}>{statusText[status]}</span>
            <IconButton label="Reconnect" onClick={() => setNonce((n) => n + 1)}>
              <RefreshIcon className="h-4 w-4" />
            </IconButton>
          </>
        }
      />
      {/* xterm mounts its canvas here; the wrapper owns padding + fills the panel. */}
      <div className="min-h-0 flex-1 overflow-hidden bg-bg-base px-2 py-1.5">
        <div ref={hostRef} className="h-full w-full" />
      </div>
    </div>
  );
}
