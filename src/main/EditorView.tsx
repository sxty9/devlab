import { useCallback, useRef } from 'react';
import Editor, { type OnMount } from '@monaco-editor/react';
import { useWorkspace } from '@/state/workspace';
import { useToast } from '@/ui/Toast';
import { ensureDevlabTheme } from './monacoTheme';
import { ChevronRightIcon, CopyIcon, SplitIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';

/** Monaco editor surface with a path breadcrumb. Fully editable: edits are held as per-tab drafts
 *  in the workspace, Cmd/Ctrl-S writes them to the backend working tree. Read-only repos (GitHub
 *  pull access) lock the editor. `repoId` namespaces the Monaco model so identical paths across
 *  repos don't collide. */
export function EditorView({ repoId, path, lang }: { repoId: string; path: string; lang: string }) {
  const { settings, editorValue, onEdit, saveFile, isDirty, canWrite } = useWorkspace();
  const { toast } = useToast();

  const code = editorValue(path);

  // Keep the Cmd-S handler (registered once on mount) calling the latest save closure.
  const saveRef = useRef<() => void>(() => {});
  saveRef.current = () => {
    if (canWrite && isDirty(path)) {
      void saveFile(path).catch((e: unknown) => toast({ title: 'Save failed', description: String((e as Error)?.message ?? e), variant: 'danger' }));
    }
  };

  const onMount = useCallback<OnMount>((editor, monaco) => {
    ensureDevlabTheme(monaco);
    editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => saveRef.current());
  }, []);

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
          {!canWrite && (
            <span className="ml-1.5 shrink-0 rounded-sm bg-fill/10 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-text-tertiary" title="Read-only access on GitHub">
              read-only
            </span>
          )}
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
          onChange={(v) => onEdit(path, v ?? '')}
          loading={<div className="p-4 text-footnote text-text-tertiary">Loading editor…</div>}
          options={{
            readOnly: !canWrite,
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
