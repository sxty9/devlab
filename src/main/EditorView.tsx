import { useCallback } from 'react';
import Editor, { type OnMount } from '@monaco-editor/react';
import { ChevronRightIcon } from '@/ui/icons';

/** Monaco editor surface with a path breadcrumb. Editable (per phase-1 decision); contents are
 *  mock until the backend lands. A custom dark theme matches the Holistic surface tokens.
 *  `repoId` namespaces the Monaco model so identical paths across repos don't collide. */
export function EditorView({ repoId, path, lang, code }: { repoId: string; path: string; lang: string; code: string }) {
  const onMount = useCallback<OnMount>((_editor, monaco) => {
    monaco.editor.defineTheme('devlab-dark', {
      base: 'vs-dark',
      inherit: true,
      rules: [
        { token: 'comment', foreground: '6b6b70', fontStyle: 'italic' },
        { token: 'keyword', foreground: 'ff7ab2' },
        { token: 'string', foreground: 'a3e28b' },
        { token: 'number', foreground: 'd0a0ff' },
        { token: 'type', foreground: '8fd3ff' },
      ],
      colors: {
        'editor.background': '#1c1c1e',
        'editor.foreground': '#f5f5f7',
        'editor.lineHighlightBackground': '#ffffff0a',
        'editor.lineHighlightBorder': '#00000000',
        'editorLineNumber.foreground': '#ffffff2e',
        'editorLineNumber.activeForeground': '#ffffff8c',
        'editorIndentGuide.background1': '#ffffff10',
        'editorIndentGuide.activeBackground1': '#ffffff24',
        'editor.selectionBackground': '#0a84ff44',
        'editor.inactiveSelectionBackground': '#0a84ff22',
        'editorCursor.foreground': '#0a84ff',
        'editorGutter.background': '#1c1c1e',
        'editorWidget.background': '#1c1c1e',
        'editorWidget.border': '#ffffff1f',
        'scrollbarSlider.background': '#ffffff1f',
        'scrollbarSlider.hoverBackground': '#ffffff33',
        'scrollbarSlider.activeBackground': '#ffffff44',
        'editorBracketMatch.background': '#0a84ff22',
        'editorBracketMatch.border': '#0a84ff55',
      },
    });
    monaco.editor.setTheme('devlab-dark');
  }, []);

  const segments = path.split('/');

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface-raised">
      <div className="dl-no-select flex h-7 shrink-0 items-center gap-0.5 overflow-x-auto px-3 text-caption text-text-tertiary">
        {segments.map((seg, i) => (
          <span key={i} className="flex shrink-0 items-center gap-0.5">
            {i > 0 && <ChevronRightIcon className="h-3 w-3 text-text-tertiary" />}
            <span className={i === segments.length - 1 ? 'text-text-secondary' : ''}>{seg}</span>
          </span>
        ))}
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
            fontSize: 13,
            lineHeight: 20,
            fontLigatures: true,
            minimap: { enabled: false },
            scrollBeyondLastLine: false,
            renderLineHighlight: 'all',
            smoothScrolling: true,
            cursorBlinking: 'smooth',
            cursorSmoothCaretAnimation: 'on',
            padding: { top: 12, bottom: 12 },
            tabSize: 2,
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
