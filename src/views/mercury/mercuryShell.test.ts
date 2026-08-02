// Rules of the Mercury SHELL that are decided by the source, not by runtime data (the same
// instrument the IDE surface uses): the shell routes sections and carries exactly one statement of
// its own — how many disturbances still demand attention.
//
// Why a source test: the rule is a WIRING rule. A disturbance that only becomes visible once the
// notices section happens to be opened does not reach anybody (REQ-031.2), and a subscription mounted
// outside the live provider is inert BY DESIGN (state/live.tsx) — both are properties of how the
// components are put together, not of a pure function's return value.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const read = (p: string) => readFileSync(new URL(p, import.meta.url), 'utf8');

/** Code with comments stripped — these greps must judge CODE, not the prose above it. */
const codeOnly = (src: string) => src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '');

test('the shell shows unread disturbances ON the section, so an alarm needs no visit to be seen', () => {
  const code = codeOnly(read('./MercuryView.tsx'));
  // Read through the ONE notices access point…
  assert.match(code, /source\.mercuryRunNotices\(/);
  // …counted by the ONE rule the notices surface itself applies (no second count).
  assert.match(code, /import \{ attentionCount \} from '\.\/notices'/);
  assert.ok(!/\.read\b|filter\(\(n\)/.test(code), 'the shell must not re-implement what counts as attention');
  // …and rendered at the section it belongs to, only while a disturbance is actually unread.
  assert.match(code, /s\.id === 'notices' && attention > 0/);
  assert.match(code, /\{attention\}/);
});

test('the attention count refreshes on the notices topic — and only there (REQ-034)', () => {
  const code = codeOnly(read('./MercuryView.tsx'));
  assert.match(code, /useLiveTopic\('notices'/);
  assert.ok(!/setInterval|setTimeout/.test(code), 'the shell must not poll for hints');
});

test('the shell subscribes BELOW the live provider — a subscription outside it is inert', () => {
  const src = read('./MercuryView.tsx');
  const code = codeOnly(src);
  // The provider mounts the stream and renders the sections as its child …
  assert.match(code, /<LiveProvider>\s*<MercurySections\s*\/>\s*<\/LiveProvider>/);
  // … and every hook that needs the stream lives in that child, never in the component that mounts
  // the provider (which sits OUTSIDE its own context).
  const mounts = code.indexOf('export function MercuryView(');
  const child = code.indexOf('function MercurySections(');
  const sub = code.indexOf("useLiveTopic('notices'");
  assert.ok(mounts >= 0 && child > mounts, 'the sections must be their own component below the provider');
  assert.ok(sub > child, 'the subscription must sit in the component below the provider');
  const mountBody = code.slice(mounts, child);
  assert.ok(!/useLiveTopic\(|useAttentionNotices\(/.test(mountBody), 'the provider-mounting component must not subscribe itself');
});
