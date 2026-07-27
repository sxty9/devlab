/** Shared file helpers used by every upload surface (Vision Catalog, ToDo media, …), so encoding and
 *  size formatting have a single definition rather than a copy per panel. */

/** Read a File as base64 (without the `data:…;base64,` prefix), ready for a JSON upload body. */
export function toBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => {
      const s = String(r.result);
      const i = s.indexOf(',');
      resolve(i >= 0 ? s.slice(i + 1) : s);
    };
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

/** A human-readable byte size ("512 B", "3 KB", "1.4 MB"). */
export function humanSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** The shape both a native ClipboardEvent and React's synthetic ClipboardEvent satisfy, so the
 *  extractor works with either without pulling React types into this framework-neutral module. */
type ClipboardCarrier = { clipboardData: DataTransfer | null };

/** The files carried by a paste, so a clipboard is a first-class upload source alongside the file
 *  dialog. Returns [] for a plain-text paste, which lets a caller ignore it and leave text entry
 *  untouched. Prefers `files`; falls back to `items` for engines that only surface pasted images
 *  there. Each file is normalized so repeated pastes do not collide on a generic name. */
export function filesFromClipboard(e: ClipboardCarrier): File[] {
  const dt = e.clipboardData;
  if (!dt) return [];
  let raw: File[] = dt.files && dt.files.length ? Array.from(dt.files) : [];
  if (raw.length === 0 && dt.items) {
    raw = Array.from(dt.items)
      .filter((it) => it.kind === 'file')
      .map((it) => it.getAsFile())
      .filter((f): f is File => f !== null);
  }
  return raw.map(normalizeClipboardFile);
}

/** True when the paste is landing in a field that owns text entry (an input, textarea, select or
 *  contenteditable), so an ambient file-paste handler can yield rather than hijack typing. */
export function isEditableTarget(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el || typeof el.tagName !== 'string') return false;
  const tag = el.tagName.toLowerCase();
  return tag === 'input' || tag === 'textarea' || tag === 'select' || el.isContentEditable === true;
}

/** Browsers name a pasted screenshot generically ("image.png") or not at all, so two pastes would
 *  clash on a duplicate name at the upload node. Give such files a unique name; a file that already
 *  carries a real name is returned untouched. */
function normalizeClipboardFile(file: File): File {
  const generic = !file.name || /^image\.\w+$/i.test(file.name);
  if (!generic) return file;
  const ext = (file.type.split('/')[1] || 'png').replace(/[^a-z0-9]/gi, '') || 'bin';
  return new File([file], `pasted-${stamp()}.${ext}`, { type: file.type, lastModified: file.lastModified });
}

/** A short, monotonic-enough stamp for disambiguating pasted-file names within a session. */
let seq = 0;
function stamp(): string {
  seq += 1;
  return `${Date.now().toString(36)}-${seq}`;
}
