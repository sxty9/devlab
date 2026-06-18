// Map a file path to a Monaco language id. Kept tiny and shared by the tree, VCS panel and editor.
const EXT: Record<string, string> = {
  ts: 'typescript',
  tsx: 'typescript',
  js: 'javascript',
  jsx: 'javascript',
  go: 'go',
  py: 'python',
  rs: 'rust',
  sh: 'shell',
  bash: 'shell',
  conf: 'shell',
  json: 'json',
  css: 'css',
  scss: 'scss',
  html: 'html',
  md: 'markdown',
  yml: 'yaml',
  yaml: 'yaml',
  toml: 'ini',
};

export function guessLang(path: string): string {
  const name = path.split('/').pop() ?? path;
  if (name === 'Dockerfile') return 'dockerfile';
  if (name === 'sxgate' || name.endsWith('.conf')) return 'shell';
  const ext = name.includes('.') ? name.split('.').pop()! : '';
  return EXT[ext] ?? 'plaintext';
}

export function basename(path: string): string {
  return path.split('/').pop() ?? path;
}

export function dirname(path: string): string {
  const i = path.lastIndexOf('/');
  return i === -1 ? '' : path.slice(0, i);
}
