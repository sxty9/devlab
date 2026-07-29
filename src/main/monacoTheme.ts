// Shared Monaco theme matching the Holistic surface tokens. Defined once, reused by the
// code editor and the diff editor — in BOTH appearances, so the editor follows the theme the user
// chose instead of staying dark inside a light surface.
type MonacoNs = {
  editor: { defineTheme: (n: string, t: object) => void; setTheme: (n: string) => void };
};

let defined = false;
let watched: MonacoNs | null = null;

/** The theme name for the appearance currently active on the document root. */
export function devlabThemeName(): 'devlab-dark' | 'devlab-light' {
  const attr = typeof document !== 'undefined' ? document.documentElement.getAttribute('data-theme') : null;
  return attr === 'light' ? 'devlab-light' : 'devlab-dark';
}

/** Define (once) and apply the DevLab themes. `monaco` is the namespace from onMount. A single
 *  observer keeps the editor in step with later theme switches. */
export function ensureDevlabTheme(monaco: MonacoNs) {
  if (!defined) {
    defined = true;
    monaco.editor.defineTheme('devlab-light', {
      base: 'vs',
      inherit: true,
      rules: [
        { token: 'comment', foreground: '8a8a8f', fontStyle: 'italic' },
        { token: 'keyword', foreground: 'ad1a72' },
        { token: 'string', foreground: '2f7d32' },
        { token: 'number', foreground: '6f42c1' },
        { token: 'type', foreground: '0b6fa4' },
      ],
      colors: {
        'editor.background': '#ffffff',
        'editor.foreground': '#1c1c1e',
        'editor.lineHighlightBackground': '#0000000a',
        'editorLineNumber.foreground': '#0000003d',
        'editorLineNumber.activeForeground': '#0000008c',
        'editor.selectionBackground': '#0a84ff33',
        'editorCursor.foreground': '#0a84ff',
        'editorBracketMatch.background': '#0a84ff1f',
        'editorBracketMatch.border': '#0a84ff55',
        'diffEditor.insertedTextBackground': '#34c7591f',
        'diffEditor.removedTextBackground': '#ff3b301f',
        'diffEditor.insertedLineBackground': '#34c75914',
        'diffEditor.removedLineBackground': '#ff3b3012',
        'diffEditorGutter.insertedLineBackground': '#34c75933',
        'diffEditorGutter.removedLineBackground': '#ff3b3033',
      },
    });
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
        // Diff colors
        'diffEditor.insertedTextBackground': '#30d15822',
        'diffEditor.removedTextBackground': '#ff453a22',
        'diffEditor.insertedLineBackground': '#30d15818',
        'diffEditor.removedLineBackground': '#ff453a16',
        'diffEditorGutter.insertedLineBackground': '#30d15833',
        'diffEditorGutter.removedLineBackground': '#ff453a33',
      },
    });
  }
  monaco.editor.setTheme(devlabThemeName());
  if (!watched && typeof MutationObserver !== 'undefined' && typeof document !== 'undefined') {
    watched = monaco;
    new MutationObserver(() => watched?.editor.setTheme(devlabThemeName())).observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme'],
    });
  }
}
