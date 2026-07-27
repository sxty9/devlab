// Shared value formatters for display — one home so every surface renders dates, times, token counts
// and costs identically, rather than each re-deriving the same toLocale* call.

/** A localized date+time (de-DE), or an em dash when absent/unparseable. */
export function fmtDateTime(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString('de-DE');
}

/** A localized clock time (de-DE, HH:mm). */
export function fmtTime(iso: string): string {
  return new Date(iso).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' });
}

/** A grouped integer (de-DE thousands separators). */
export const fmtNum = (n: number) => n.toLocaleString('de-DE');

/** A USD cost to four decimals. */
export const fmtCost = (n: number) => `$${n.toFixed(4)}`;
