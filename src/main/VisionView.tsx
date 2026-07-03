import { useEffect, useMemo, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { getDataSource } from '@/data';
import { CommentsThread } from '@/panels/CommentsThread';
import { renderMarkdown } from '@/lib/markdown';
import { LightbulbIcon } from '@/ui/icons';
import type { VisionFileKind } from '@/types';

function kindOf(name: string): VisionFileKind {
  const ext = name.includes('.') ? name.split('.').pop()!.toLowerCase() : '';
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'avif', 'bmp', 'ico'].includes(ext)) return 'image';
  if (ext === 'pdf') return 'pdf';
  if (ext === 'md' || ext === 'markdown') return 'markdown';
  if (['txt', 'json', 'yaml', 'yml', 'toml', 'csv', 'ts', 'tsx', 'js', 'go', 'py', 'sh', 'html', 'css'].includes(ext)) return 'text';
  return 'other';
}

/** Main-area viewer for a Vision-Catalog file: renders by type (image/pdf/markdown/text) with the
 *  threaded discussion below. Image/PDF stream from the raw byte endpoint; text/markdown reuse the
 *  JSON file endpoint. */
export function VisionView({ path }: { path: string }) {
  const { activeRepo } = useWorkspace();
  const source = useMemo(() => getDataSource(), []);
  const kind = kindOf(path);
  const rawUrl = source.rawUrl(activeRepo.id, path);
  const [text, setText] = useState<string | null>(null);

  useEffect(() => {
    if (kind !== 'markdown' && kind !== 'text') return;
    let cancelled = false;
    setText(null);
    source
      .fileContent(activeRepo.id, path)
      .then((fc) => !cancelled && setText(fc.code))
      .catch(() => !cancelled && setText(''));
    return () => {
      cancelled = true;
    };
  }, [kind, source, activeRepo.id, path]);

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface-raised">
      <div className="dl-no-select flex h-7 shrink-0 items-center gap-1.5 border-b border-separator px-3 text-caption text-text-tertiary">
        <LightbulbIcon className="h-3.5 w-3.5 text-accent" />
        <span className="truncate font-medium text-text-secondary">{path}</span>
        <a href={rawUrl} target="_blank" rel="noreferrer" className="ml-auto shrink-0 text-caption text-text-tertiary transition hover:text-accent">
          Open raw ↗
        </a>
      </div>

      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto">
        <div className="p-4">
          {kind === 'image' && <img src={rawUrl} alt={path} className="mx-auto max-w-full rounded-md ring-1 ring-separator" />}
          {kind === 'pdf' && <iframe title={path} src={rawUrl} className="h-[72vh] w-full rounded-md ring-1 ring-separator" />}
          {kind === 'markdown' && (
            <div className="dl-markdown mx-auto max-w-3xl text-footnote text-text-secondary" dangerouslySetInnerHTML={{ __html: renderMarkdown(text ?? '') }} />
          )}
          {kind === 'text' && (
            <pre className="dl-scroll overflow-x-auto rounded-md bg-fill/[0.06] p-3 font-mono text-caption leading-relaxed text-text-secondary">{text ?? '…'}</pre>
          )}
          {kind === 'other' && (
            <div className="mx-auto max-w-md rounded-md bg-fill/[0.06] p-6 text-center text-footnote text-text-tertiary">
              No inline preview for this file type.
              <div className="mt-2">
                <a href={rawUrl} target="_blank" rel="noreferrer" className="text-accent hover:underline">
                  Open / download raw ↗
                </a>
              </div>
            </div>
          )}
        </div>

        <CommentsThread repoId={activeRepo.id} path={path} />
      </div>
    </div>
  );
}
