import { useCallback } from 'react';
import { DiffEditor, type DiffOnMount } from '@monaco-editor/react';
import { useWorkspace } from '@/state/workspace';
import { devlabThemeName, ensureDevlabTheme } from './monacoTheme';
import { PathBreadcrumb } from '@/ui/PathBreadcrumb';
import { DiffIcon } from '@/ui/icons';

/** Side-by-side diff (working tree ↔ HEAD) for a changed file. */
export function DiffView({ path }: { path: string }) {
  const { fileDiff, settings } = useWorkspace();
  const { before, after, lang } = fileDiff(path);
  const onMount = useCallback<DiffOnMount>((_editor, monaco) => ensureDevlabTheme(monaco), []);

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface-raised">
      <div className="dl-no-select flex h-7 shrink-0 items-center gap-2 border-b border-separator px-3 text-caption text-text-tertiary">
        <DiffIcon className="h-3.5 w-3.5 shrink-0 text-text-secondary" />
        <div className="flex min-w-0 items-center gap-0.5 overflow-x-auto">
          <PathBreadcrumb path={path} />
        </div>
        <span className="ml-auto flex shrink-0 items-center gap-2 font-mono text-[11px]">
          <span className="text-text-tertiary">HEAD</span>
          <span aria-hidden>↔</span>
          <span className="text-accent">Working tree</span>
        </span>
      </div>
      <div className="min-h-0 flex-1">
        <DiffEditor
          original={before}
          modified={after}
          language={lang}
          theme={devlabThemeName()}
          onMount={onMount}
          loading={<div className="p-4 text-footnote text-text-tertiary">Loading diff…</div>}
          options={{
            readOnly: true,
            renderSideBySide: true,
            originalEditable: false,
            fontFamily: 'var(--font-mono)',
            fontSize: settings.fontSize,
            lineHeight: Math.round(settings.fontSize * 1.55),
            minimap: { enabled: false },
            scrollBeyondLastLine: false,
            renderOverviewRuler: false,
            automaticLayout: true,
            padding: { top: 10, bottom: 10 },
            scrollbar: { verticalScrollbarSize: 10, horizontalScrollbarSize: 10 },
            diffWordWrap: 'off',
          }}
        />
      </div>
    </div>
  );
}
