import { test } from 'node:test';
import assert from 'node:assert/strict';
import { describePerson, initials } from './person.ts';

test('a known person is shown by name with a stable, deterministic avatar', () => {
  const v = describePerson({ username: 'alice', displayName: 'Alice Ng' });
  assert.equal(v.kind, 'person');
  assert.equal(v.label, 'Alice Ng');
  assert.equal(v.initials, 'AN');
  assert.equal(v.title, 'Alice Ng (@alice)');
  // The hue is keyed to the username, so it is identical regardless of the display name.
  assert.equal(v.tone, describePerson({ username: 'alice', displayName: 'Someone Else' }).tone);
});

test('a record with no author is explicitly unknown, never guessed', () => {
  const v = describePerson({});
  assert.equal(v.kind, 'unknown');
  assert.equal(v.label, 'Unknown');
  assert.equal(v.initials, '?');
  // A blank username is treated as absent, too — not a person named "  ".
  assert.equal(describePerson({ username: '   ' }).kind, 'unknown');
});

test('an autonomous process is not shown as a person', () => {
  const v = describePerson({ autonomous: true }, { autonomousLabel: 'Autonomous run' });
  assert.equal(v.kind, 'autonomous');
  assert.equal(v.label, 'Autonomous run');
  assert.equal(v.initials, ''); // never human initials
});

test('username-only falls back to @username', () => {
  const v = describePerson({ username: 'bob' });
  assert.equal(v.label, 'bob');
  assert.equal(v.title, '@bob');
  assert.equal(v.initials, 'BO');
});

test('initials handles empty, one, and two names', () => {
  assert.equal(initials(''), '?');
  assert.equal(initials('nanu'), 'NA');
  assert.equal(initials('Grace Hopper'), 'GH');
});
