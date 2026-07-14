// The view axis. DevLab is a Dashboard plus a set of capabilities, and the URL hash is the single
// source of truth for which one you are looking at — so a view is deep-linkable, survives a reload,
// and the browser's back button works without any history bookkeeping of our own.
//
// Hash rather than pushState: it needs no server cooperation at all (serveSPA already falls back to
// index.html, but a hash never even reaches the server), and `hashchange` gives back/forward for free.

/** Which view the app is showing. `ide` carries the repo it is bound to. */
export type View =
  | { kind: 'dashboard' }
  | { kind: 'ide'; repo: string }
  | { kind: 'mercury' }
  | { kind: 'atlas' };

/** Parse a location hash into a View. Anything unrecognised lands on the dashboard. */
export function parseHash(hash: string): View {
  const segments = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  const [head, ...rest] = segments;
  if (head === 'ide' && rest[0]) return { kind: 'ide', repo: decodeURIComponent(rest[0]) };
  if (head === 'mercury') return { kind: 'mercury' };
  if (head === 'atlas') return { kind: 'atlas' };
  return { kind: 'dashboard' };
}

/** Serialise a View back to a location hash. */
export function toHash(view: View): string {
  switch (view.kind) {
    case 'ide':
      return `#/ide/${encodeURIComponent(view.repo)}`;
    case 'mercury':
      return '#/mercury';
    case 'atlas':
      return '#/atlas';
    case 'dashboard':
      return '#/';
  }
}

/** The View the current URL names. */
export function currentView(): View {
  return parseHash(typeof window === 'undefined' ? '' : window.location.hash);
}
