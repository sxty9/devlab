// The instance's own addresses are DERIVED, never baked in (B-41, "no instance specifics"), and
// they are derived in ONE place: the static access to a running service, the way to the dashboard
// and the deep link into the AI service all come from here, so no view can drift from another.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { aigenticUrl, devServiceUrl, holisticOrigin, instanceBaseDomain, instanceEnvSuffix, serviceTabUrl } from './constants.ts';

/** Serve the module from a given host, the way a browser would. */
function servedFrom(hostname: string, protocol = 'https:') {
  const origin = `${protocol}//${hostname}`;
  (globalThis as { window?: unknown }).window = { location: { hostname, protocol, origin, host: hostname } };
}
function noWindow() {
  delete (globalThis as { window?: unknown }).window;
}

test('the zone is read off the host DevLab is served from — never hard-coded', () => {
  servedFrom('devlab.example.org');
  assert.equal(instanceBaseDomain(), 'example.org');
  // Any service label, any depth of zone: the first label is the service, the rest is the zone.
  servedFrom('devlab.eu.example.co.uk');
  assert.equal(instanceBaseDomain(), 'eu.example.co.uk');
  // A host that carries no zone yields none — nothing is invented.
  servedFrom('localhost');
  assert.equal(instanceBaseDomain(), '');
  servedFrom('devlab.local');
  assert.equal(instanceBaseDomain(), '');
  noWindow();
  assert.equal(instanceBaseDomain(), '');
});

test('the dashboard, a service tab and the AI service all follow from that one derivation', () => {
  servedFrom('devlab.example.org');
  assert.equal(holisticOrigin(), 'https://holistic.example.org');
  assert.equal(serviceTabUrl('remshel'), 'https://holistic.example.org/app/remshel');
  assert.equal(aigenticUrl(), 'https://holistic.example.org/app/aigentic');
  // The static access to a repository's running service goes to the same place — one derivation.
  assert.equal(devServiceUrl('prizm'), 'https://holistic.example.org/app/prizm');
  // The scheme follows the current one; a plain-http instance is not silently upgraded.
  servedFrom('devlab.example.org', 'http:');
  assert.equal(holisticOrigin(), 'http://holistic.example.org');
});

test('a jump stays inside its environment — a dev host never reaches production', () => {
  // The environment marker rides on the first label and is carried to every sibling: a session
  // opened from DevLab dev lands in the Holistic DEV dashboard, not in production with live data.
  servedFrom('devlab-dev.example.org');
  assert.equal(instanceBaseDomain(), 'example.org', 'the zone is shared by every environment');
  assert.equal(instanceEnvSuffix(), '-dev');
  assert.equal(holisticOrigin(), 'https://holistic-dev.example.org');
  assert.equal(serviceTabUrl('remshel'), 'https://holistic-dev.example.org/app/remshel');
  assert.equal(aigenticUrl(), 'https://holistic-dev.example.org/app/aigentic');
  assert.equal(devServiceUrl('prizm'), 'https://holistic-dev.example.org/app/prizm');

  // Production carries no marker, so it reaches production — and nothing else.
  servedFrom('devlab.example.org');
  assert.equal(instanceEnvSuffix(), '');
  assert.equal(holisticOrigin(), 'https://holistic.example.org');

  // A host that matches neither the plain prod shape nor the `-dev` one keeps its own marker: an
  // unrecognised environment stays named (`-staging`) and does NOT collapse into production.
  servedFrom('devlab-staging.example.org');
  assert.equal(instanceEnvSuffix(), '-staging');
  assert.equal(holisticOrigin(), 'https://holistic-staging.example.org');
});

test('without a zone everything stays on the current origin, and without a window on a path', () => {
  servedFrom('localhost:5173'.split(':')[0]);
  assert.equal(holisticOrigin(), 'https://localhost');
  assert.equal(devServiceUrl('prizm'), 'https://localhost/app/prizm');
  noWindow();
  assert.equal(holisticOrigin(), '/');
  assert.equal(devServiceUrl('prizm'), '/app/prizm', 'a relative link is always safe — a guessed host is not');
});

test('a repository name is escaped into the link, never pasted', () => {
  servedFrom('devlab.example.org');
  assert.equal(devServiceUrl('a b/c'), 'https://holistic.example.org/app/a%20b%2Fc');
  noWindow();
});

test('this module holds no instance literal, and it is the only place that derives the host', () => {
  const here = new URL('.', import.meta.url).pathname;
  const src = readFileSync(join(here, 'constants.ts'), 'utf8');
  // No dotted domain literal anywhere in the derivation (the zone comes from the runtime).
  const code = src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '');
  assert.ok(!/['"`][a-z0-9-]+\.[a-z]{2,}['"`]/.test(code), 'no host literal may appear in the derivation');

  // Nobody else assembles the dashboard host: one derivation, no drifting siblings (B-41).
  const root = join(here, '..');
  for (const path of walk(root)) {
    if (path === join(here, 'constants.ts')) continue;
    const other = readFileSync(path, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '');
    assert.ok(
      !/holistic[-.]?\$\{|`.*\/\/holistic[-.]/.test(other),
      `${path.slice(root.length + 1)} must take the dashboard origin from lib/constants`,
    );
  }
});

function walk(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(path));
    else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) out.push(path);
  }
  return out;
}
