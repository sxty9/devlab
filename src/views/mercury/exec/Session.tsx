// Transcript — the agent's transcript as it happens: watching an execution IS reading this pane
// (REQ-036), the same way one watches a Claude-Code session. The text is the stage's own recorded
// log — the compacted journal the executor streams line by line — so it is preserved when a run is
// killed and stays readable afterwards (F11). While the stage runs the pane follows the newest
// line; once it has settled the viewer scrolls freely.
import { useEffect, useRef } from 'react';
import { cn } from '@/lib/cn';

export function Transcript({ text, live, className }: { text?: string; live?: boolean; className?: string }) {
  const ref = useRef<HTMLPreElement>(null);

  // Follow the tail while it grows; never fight the viewer's scrolling once it has settled.
  useEffect(() => {
    if (live && ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [live, text]);

  return (
    <pre
      ref={ref}
      className={cn(
        'dl-scroll max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-separator bg-bg-base p-3 font-mono text-caption text-text-secondary',
        className,
      )}
    >
      {text || (live ? 'The agent is working…' : 'No transcript recorded.')}
    </pre>
  );
}
