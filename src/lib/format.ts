// Shared value presentation — one home for how a value READS, and in which language it reads.
//
// Every surface renders dates, times, token counts and costs through the formatters below instead of
// re-deriving the same toLocale* call, and every one of them renders in the CHOSEN UI language, never
// in a language nailed into the code: DevLab is multilingual, so the surface's language decides how a
// date and a number look. No caller passes a locale — the formatting API stays one call per value and
// the language stays one fact in one place.
//
// That one fact is the document's own declaration, `<html lang="…">`: where a surface states its
// language, what the browser and assistive technology already read, and the single attribute a
// language switch has to change. A missing or unusable declaration means NEUTRAL — the platform then
// formats in the viewer's own preference.

/** The chosen UI language as a BCP-47 tag, or undefined for neutral (the viewer's own preference). */
export function uiLocale(): string | undefined {
  const tag = documentLang();
  return validTag(tag) ? tag : undefined;
}

/** Switches the surface's language — the ONE place a language choice is applied. An empty or
 *  unusable tag returns the surface to neutral formatting. */
export function setUiLocale(tag: string): void {
  const root = documentRoot();
  if (!root) return;
  const next = tag.trim();
  root.setAttribute('lang', validTag(next) ? next : '');
}

function documentRoot(): HTMLElement | null {
  return typeof document === 'undefined' ? null : document.documentElement;
}

function documentLang(): string {
  return documentRoot()?.getAttribute('lang')?.trim() ?? '';
}

/** A usable BCP-47 language tag. "und" is the standard's own "undetermined" — neutral, not a
 *  language. */
function validTag(tag: string): boolean {
  if (tag === '' || tag.toLowerCase() === 'und') return false;
  return /^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$/.test(tag);
}

/** A localized date+time, or an em dash when absent/unparseable. */
export function fmtDateTime(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString(uiLocale());
}

/** A localized clock time (hour and minute in the chosen language's own convention). */
export function fmtTime(iso: string): string {
  return new Date(iso).toLocaleTimeString(uiLocale(), { hour: '2-digit', minute: '2-digit' });
}

/** A grouped integer (the chosen language's own thousands separator). */
export const fmtNum = (n: number) => n.toLocaleString(uiLocale());

/** A USD cost to four decimals — money, not a locale decision. */
export const fmtCost = (n: number) => `$${n.toFixed(4)}`;
