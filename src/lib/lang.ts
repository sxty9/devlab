// Shared file-path helpers — basename/dirname, file extension, the Monaco language id, and the
// Vision-Catalog kind. One home for "given a path or name, tell me X", so no caller re-derives it.
import type { VisionFileKind } from '@/types';

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

export function basename(path: string): string {
  return path.split('/').pop() ?? path;
}

export function dirname(path: string): string {
  const i = path.lastIndexOf('/');
  return i === -1 ? '' : path.slice(0, i);
}

/** Lower-cased file extension without the dot ('' when the name has none). */
export function extension(name: string): string {
  return name.includes('.') ? (name.split('.').pop() ?? '').toLowerCase() : '';
}

export function guessLang(path: string): string {
  const name = basename(path);
  if (name === 'Dockerfile') return 'dockerfile';
  if (name === 'sxgate' || name.endsWith('.conf')) return 'shell';
  return EXT[extension(name)] ?? 'plaintext';
}

const VISION_IMAGE = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'avif', 'bmp', 'ico'];
const VISION_TEXT = ['txt', 'json', 'yaml', 'yml', 'toml', 'csv', 'ts', 'tsx', 'js', 'go', 'py', 'sh', 'html', 'css'];

/** Classify a Vision-Catalog file by extension into how the viewer renders it. */
export function visionKind(name: string): VisionFileKind {
  const ext = extension(name);
  if (VISION_IMAGE.includes(ext)) return 'image';
  if (ext === 'pdf') return 'pdf';
  if (ext === 'md' || ext === 'markdown') return 'markdown';
  if (VISION_TEXT.includes(ext)) return 'text';
  return 'other';
}
