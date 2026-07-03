import { marked } from 'marked';
import DOMPurify from 'dompurify';

/** Render untrusted markdown (comment bodies, vision .md files, AI answers) to sanitized HTML.
 *  marked handles GFM; DOMPurify strips any script/dangerous markup before it reaches the DOM. */
export function renderMarkdown(src: string): string {
  const html = marked.parse(src ?? '', { async: false, gfm: true, breaks: true }) as string;
  return DOMPurify.sanitize(html);
}
