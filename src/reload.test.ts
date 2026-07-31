// REQ-035 — after a browser reload the user is back where they were: the same tab, the same view,
// the same session. The URL hash is the single source of truth for the view axis, and the state
// that is not in the URL (which section of a capability, which repo, which panel width) is
// persisted deliberately.
//
// A reload is simulated the honest way: the route module is asked what a location hash means, and
// the persisted state is read back through the very keys the components write — a component that
// stops persisting therefore breaks this test.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { currentView, parseHash, toHash, type View } from './state/route.ts';

const here = dirname(fileURLToPath(import.meta.url));

/** Every main view of the surface, with the hash a reload would arrive with. */
const views: { name: string; view: View; hash: string }[] = [
  { name: 'dashboard', view: { kind: 'dashboard' }, hash: '#/' },
  { name: 'IDE on a repo', view: { kind: 'ide', repo: 'devlab' }, hash: '#/ide/devlab' },
  { name: 'Mercury', view: { kind: 'mercury' }, hash: '#/mercury' },
  { name: 'Atlas', view: { kind: 'atlas' }, hash: '#/atlas' },
];

test('a reload in any main view lands in the SAME view', () => {
  for (const { name, view, hash } of views) {
    assert.equal(toHash(view), hash, `${name}: the view does not serialise to its own address`);
    // The reload: the browser re-parses the address the previous session left behind.
    assert.deepEqual(parseHash(toHash(view)), view, `${name}: a reload does not restore the view`);
  }
});

test('the repo binding of the IDE survives the reload, encoding and all', () => {
  for (const repo of ['devlab', 'holistic-ui', 'a.b_c-d', 'repo with space', 'ünicode-repo']) {
    const restored = parseHash(toHash({ kind: 'ide', repo }));
    assert.deepEqual(restored, { kind: 'ide', repo }, `the IDE lost its repo "${repo}" across the reload`);
  }
});

test('an address that names nothing lands on the dashboard instead of a blank screen', () => {
  for (const hash of ['', '#', '#/', '#/nonsense', '#/ide', '#/ide/', '#//', '#/mercury/extra']) {
    const view = parseHash(hash);
    assert.ok(view.kind, `"${hash}" produced no view at all`);
    if (hash === '#/mercury/extra') {
      assert.equal(view.kind, 'mercury', 'a deeper Mercury address must still open Mercury');
    }
  }
  // A malformed escape must not throw: parseHash runs inside a state initialiser, so an exception
  // would blank the whole application instead of showing something.
  for (const hash of ['#/ide/%', '#/ide/a%zz', '#/ide/%E0%A4%A']) {
    assert.doesNotThrow(() => parseHash(hash), `"${hash}" threw instead of degrading`);
    assert.equal(parseHash(hash).kind, 'ide', `"${hash}" lost the view it names`);
  }
});

test('currentView reads the live address and never throws without a browser', () => {
  const g = globalThis as { window?: { location: { hash: string } } };
  const had = 'window' in g;
  try {
    g.window = { location: { hash: '#/mercury' } };
    assert.deepEqual(currentView(), { kind: 'mercury' });
    g.window = { location: { hash: '#/ide/devlab' } };
    assert.deepEqual(currentView(), { kind: 'ide', repo: 'devlab' });
  } finally {
    if (!had) delete g.window;
  }
});

// ── the state that is NOT in the address ─────────────────────────────────────────────────────
//
// A view has an inner place (which Mercury section, which repo the dashboard resumes, how wide the
// panel column is). REQ-035 covers that too: it is persisted per surface, restored on mount, and
// guarded against unreadable storage.

/** Read a component's source once; the assertions below are about what it persists. */
function source(rel: string): string {
  return readFileSync(join(here, rel), 'utf8');
}

const persisted: { name: string; file: string; key: string }[] = [
  { name: 'the Mercury section', file: 'views/mercury/MercuryView.tsx', key: 'mercury.section' },
  { name: 'the repo the dashboard resumes', file: 'state/session.tsx', key: 'dl.lastRepo' },
  { name: 'the width of the panel column', file: 'shell/PanelHost.tsx', key: 'dl.panelWidth' },
];

test('the place INSIDE a view is persisted and restored, not reset on reload', () => {
  for (const { name, file, key } of persisted) {
    const src = source(file);
    assert.ok(src.includes(key), `${name}: ${file} does not persist under the key "${key}"`);
    assert.ok(
      src.includes('getItem') && src.includes('setItem'),
      `${name}: ${file} writes or reads its state but not both — a reload would lose it`,
    );
    // Storage can be unavailable (private mode). Restoring must degrade, never throw: the value
    // lives inside a state initialiser.
    assert.ok(
      /catch\s*(\([^)]*\))?\s*\{/.test(src),
      `${name}: ${file} reads storage without guarding against an unavailable one`,
    );
  }
});

test('a running execution stays visible after the reload: WHICH one is remembered, its state is refetched', () => {
  const files = ['views/mercury/exec/ActiveList.tsx', 'views/mercury/exec/ExecutionsView.tsx'];
  for (const file of files) {
    const src = source(file);
    // Remembering WHICH execution the user was looking at is the point of REQ-035…
    assert.ok(
      /localStorage\.(get|set)Item/.test(src),
      `${file} forgets which execution the user had open — a reload would drop them elsewhere`,
    );
    // …but the execution's STATE must come from the server. A stored payload would survive the
    // reload as a ghost of a run that has long since ended.
    assert.ok(
      !/setItem\([^)]*JSON\.stringify/.test(src),
      `${file} stores execution data in the browser — after a reload that is a ghost, not the truth`,
    );
    assert.ok(
      /source\.mercuryRun(Active|Executions)\(/.test(src),
      `${file} does not read the executions from the server on mount`,
    );
    assert.ok(
      /useLiveTopic\(/.test(src),
      `${file} has no live subscription — after the reload the view would go stale silently`,
    );
  }
});

test('the session itself survives: the reload re-probes identity instead of trusting a cached one', () => {
  const session = source('state/session.tsx');
  // The user is fetched, never persisted: a stale cached identity would outlive a logout.
  assert.ok(!/localStorage\.setItem\(\s*['"`][^'"`]*user/i.test(session), 'the identity is cached in the browser');
  assert.ok(
    session.includes('init()') || session.includes('.init('),
    'the boot does not probe the session — a reload could not restore it',
  );
});

// B-23 — an expired access token must not throw the user out (which would lose exactly the state
// REQ-035 is about): the seam re-mints once, retries the request, and coalesces a burst of
// concurrent 401s into a SINGLE refresh.
//
// This is a structural audit rather than a behavioural one: the data seam is a frozen contract
// written for the bundler (extensionless module specifiers), so it cannot be imported under
// `node --test`. The behaviour itself is measured by hand — see the reload section of
// deploy/migration/20-sichtpruefung.md.
test('an expired token is re-minted ONCE for a burst of requests, and the retry is bounded', () => {
  const seam = source('data/httpSource.ts');

  // Exactly one place asks for a fresh token.
  const refreshCalls = [...seam.matchAll(/['"`]\/api\/auth\/refresh['"`]/g)];
  assert.equal(refreshCalls.length, 1, `the refresh is requested from ${refreshCalls.length} places, want exactly one`);

  // The single flight: an in-flight promise is shared, and cleared when it settles (a refresh that
  // never cleared would wedge every later 401).
  assert.match(seam, /let refreshing:\s*Promise<[^>]+>\s*\|\s*null\s*=\s*null/, 'no in-flight refresh handle');
  assert.match(seam, /if\s*\(!refreshing\)/, 'the refresh is not coalesced — a burst of 401s would storm the endpoint');
  assert.match(seam, /\.finally\(\s*\(\)\s*=>\s*\{\s*\n?\s*refreshing = null;?/, 'the in-flight handle is not cleared when the refresh settles');

  // The retry is bounded: the second attempt passes the retry flag off, so a persistent 401 cannot
  // recurse.
  assert.match(seam, /retry\s*=\s*true/, 'the retry is not a bounded parameter');
  assert.match(seam, /return request\(input,\s*withFreshCsrf\(init\),\s*false\)/, 'the retry does not switch retrying off');

  // The re-minted CSRF cookie is re-read for the retried mutating call.
  assert.match(seam, /function withFreshCsrf/, 'the retry reuses the stale CSRF token');

  // A 401 that survives the refresh surfaces as the named condition, never as data.
  assert.match(seam, /if \(res\.status === 401\) throw new AuthRequiredError\(\)/, 'an unauthenticated answer is not surfaced as such');
});
