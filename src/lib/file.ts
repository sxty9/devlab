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
