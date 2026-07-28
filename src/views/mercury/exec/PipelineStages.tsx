// PipelineStages — Welle-0 stub (B9 fills it). Renders ONLY the server stage array; the four
// terminal states are distinguishable without text (REQ-030.5). OkPill is the shared pass/
// fail chip other surfaces reuse.
import type { RepoPipeline } from '@/types';

export function OkPill({ ok }: { ok: boolean }) {
  return (
    <span
      className={
        'shrink-0 rounded px-1.5 py-0.5 text-caption font-medium ' +
        (ok ? 'bg-success/15 text-success' : 'bg-danger/15 text-danger')
      }
    >
      {ok ? 'Succeeded' : 'Failed'}
    </span>
  );
}

export function PipelineStages({ pipeline }: { pipeline: RepoPipeline }) {
  return (
    <p className="text-footnote text-text-tertiary">
      Stage badges for {pipeline.repo} arrive with their building block (B9).
    </p>
  );
}
