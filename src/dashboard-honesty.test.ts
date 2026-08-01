// The dashboard must never name a cause it has not measured, and never leave a dead control
// unexplained. Both were found by the browser inspection on 2026-07-31: the repository list said
// "GitHub is unreachable right now" while OUR OWN server was answering 502, and the IDE card went
// silently dark as a consequence, with no reason on it at all.
//
// Asserted on the sources themselves, because both are single decisions in the markup — a
// re-implementation here would only agree with itself.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const HERE = new URL('.', import.meta.url).pathname;
const dashboard = readFileSync(join(HERE, 'views/Dashboard.tsx'), 'utf8');
const session = readFileSync(join(HERE, 'state/session.tsx'), 'utf8');

test('a failed repository read names the MEASURED reason, never an assumed culprit', () => {
  assert.doesNotMatch(
    dashboard,
    /GitHub is unreachable/,
    'the dashboard states GitHub as the cause of a fault it never measured',
  );
  assert.match(
    dashboard,
    /could not be read: \$\{reposError\}/,
    'the dashboard must show the reason it actually got',
  );
});

test('the reason of a failed repository read is kept, not thrown away', () => {
  assert.doesNotMatch(
    session,
    /\}\s*catch\s*\{\s*\n[^}]*setReposError\(true\)/,
    'the catch discards the reason, which is what forced the dashboard to invent one',
  );
  assert.match(session, /setReposError\(errMsg\(e\)\)/, 'the measured reason must reach the surface');
});

test('a disabled capability card states why it is disabled', () => {
  // The card is disabled by exactly one condition; that condition must also produce a reason.
  assert.match(
    dashboard,
    /const ideBlocked[\s\S]{0,400}?reposError \?/,
    'the IDE card is greyed out without saying why',
  );
  assert.match(dashboard, /subtitle=\{ideBlocked\(c\.id\) \?\? c\.role\}/, 'the reason must reach the card');
});
