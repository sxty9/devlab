import { useCallback } from 'react';
import Editor, { type OnMount } from '@monaco-editor/react';
import { useWorkspace } from '@/state/workspace';
import { useToast } from '@/ui/Toast';
import { ensureDevlabTheme } from './monacoTheme';
import { ChevronRightIcon, CopyIcon, SplitIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

/** Monaco editor surface with a path breadcrumb. Editable (per phase-1 decision); contents are
 *  mock until the backend lands. A custom dark theme matches the Holistic surface tokens.
 *  `repoId` namespaces the Monaco model so identical paths across repos don't collide. */
export function EditorView({ repoId, path, lang, code }: { repoId: string; path: string; lang: string; code: string }) {
  const { settings } = useWorkspace();
  const { toast } = useToast();

  const onMount = useCallback<OnMount>((_editor, monaco) => ensureDevlabTheme(monaco), []);

  const segments = path.split('/');

  const copyPath = () => {
    navigator.clipboard?.writeText(path).then(
      () => toast({ title: 'Path copied', description: path }),
      () => toast({ title: 'Could not copy path', variant: 'danger' }),
    );
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface-raised">
      <div className="dl-no-select flex h-7 shrink-0 items-center gap-0.5 border-b border-separator px-2 text-caption text-text-tertiary">
        <div className="dl-scroll flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto">
          {segments.map((seg, i) => {
            const last = i === segments.length - 1;
            return (
              <span key={i} className="flex shrink-0 items-center gap-0.5">
                {i > 0 && <ChevronRightIcon className="h-3 w-3 text-text-tertiary" />}
                <span
                  className={cn(
                    'rounded-sm px-1 py-0.5 transition-colors',
                    last ? 'font-medium text-text-secondary' : 'hover:bg-fill/10 hover:text-text-secondary',
                  )}
                >
                  {seg}
                </span>
              </span>
            );
          })}
        </div>
        <button
          type="button"
          onClick={copyPath}
          aria-label="Copy path"
          title="Copy path"
          className="flex h-5 w-5 shrink-0 items-center justify-center rounded-sm text-text-tertiary transition hover:bg-fill/10 hover:text-text-secondary"
        >
          <CopyIcon className="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          onClick={() => toast({ title: 'Split editor', description: 'Side-by-side editing arrives with the backend.' })}
          aria-label="Split editor"
          title="Split editor (phase 2)"
          className="flex h-5 w-5 shrink-0 items-center justify-center rounded-sm text-text-tertiary transition hover:bg-fill/10 hover:text-text-secondary"
        >
          <SplitIcon className="h-3.5 w-3.5" />
        </button>
      </div>
      <div className="min-h-0 flex-1">
        <Editor
          path={`${repoId}/${path}`}
          language={lang}
          value={code}
          theme="devlab-dark"
          onMount={onMount}
          loading={<div className="p-4 text-footnote text-text-tertiary">Loading editor…</div>}
          options={{
            fontFamily: 'var(--font-mono)',
            fontSize: settings.fontSize,
            lineHeight: Math.round(settings.fontSize * 1.55),
            fontLigatures: true,
            minimap: { enabled: false },
            scrollBeyondLastLine: false,
            renderLineHighlight: 'all',
            smoothScrolling: true,
            cursorBlinking: 'smooth',
            cursorSmoothCaretAnimation: 'on',
            padding: { top: 12, bottom: 12 },
            tabSize: settings.tabSize,
            automaticLayout: true,
            scrollbar: { verticalScrollbarSize: 10, horizontalScrollbarSize: 10 },
            guides: { indentation: true },
            overviewRulerLanes: 0,
            roundedSelection: true,
          }}
        />
      </div>
    </div>
  );
}
