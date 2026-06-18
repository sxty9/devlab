// Shared Monaco theme matching the Holistic surface tokens. Defined once, reused by the
// code editor and the diff editor.
let defined = false;

/** Define (once) and apply the DevLab dark theme. `monaco` is the namespace from onMount. */
export function ensureDevlabTheme(monaco: { editor: { defineTheme: (n: string, t: object) => void; setTheme: (n: string) => void } }) {
  if (!defined) {
    defined = true;
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
  monaco.editor.setTheme('devlab-dark');
}
