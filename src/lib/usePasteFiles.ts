import { useEffect } from 'react';
import { filesFromClipboard, isEditableTarget } from './file';

/** Make the clipboard an upload source for a surface that has no focus anchor of its own — a side
 *  panel where the user just presses Cmd/Ctrl+V. It listens at the window and routes pasted files to
 *  `onFiles`, yielding when:
 *   - the paste was already consumed by a more specific surface (`defaultPrevented`) — e.g. an editor
 *     that scoped `onPaste` to its own root, so the bounded surface wins over the ambient panel;
 *   - it carries no files (a plain-text paste); or
 *   - it targets an editable field, so text entry is never hijacked.
 *  A bounded surface (an open editor/form) should instead put `onPaste` on its own root element;
 *  this hook is for the ambient case. Pass `enabled=false` to detach (e.g. a read-only surface).
 *
 *  `onFiles` should be stable (memoised) so the listener is not re-bound on every render. */
export function usePasteFiles(onFiles: (files: File[]) => void, enabled = true): void {
  useEffect(() => {
    if (!enabled) return;
    const handler = (e: ClipboardEvent) => {
      if (e.defaultPrevented || isEditableTarget(e.target)) return;
      const files = filesFromClipboard(e);
      if (files.length === 0) return;
      e.preventDefault();
      onFiles(files);
    };
    window.addEventListener('paste', handler);
    return () => window.removeEventListener('paste', handler);
  }, [onFiles, enabled]);
}
