import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  applyNotice,
  isSessionNotice,
  parseControlFrame,
  recallSession,
  rememberSession,
  sessionKeyName,
  statusText,
  type KeyStore,
} from './terminalSession.ts';

/** A minimal in-memory KeyStore. */
function store(seed: Record<string, string> = {}): KeyStore & { data: Record<string, string> } {
  const data = { ...seed };
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => {
      data[k] = v;
    },
    removeItem: (k) => {
      delete data[k];
    },
  };
}

test('a repo remembers the session it was attached to, and asks for it again', () => {
  const s = store();
  assert.equal(recallSession('widget', s), '');
  rememberSession('widget', 'sess-7', s);
  assert.equal(s.data[sessionKeyName('widget')], 'sess-7');
  assert.equal(recallSession('widget', s), 'sess-7');
  // Per repo, never shared across repos.
  assert.equal(recallSession('other', s), '');
});

test('an empty key forgets the session instead of storing an empty one', () => {
  const s = store({ [sessionKeyName('widget')]: 'sess-7' });
  rememberSession('widget', '', s);
  assert.equal(recallSession('widget', s), '');
});

test('unavailable storage is not an error — there is simply nothing to resume', () => {
  const hostile: KeyStore = {
    getItem() {
      throw new Error('denied');
    },
    setItem() {
      throw new Error('denied');
    },
    removeItem() {
      throw new Error('denied');
    },
  };
  assert.equal(recallSession('widget', hostile), '');
  assert.doesNotThrow(() => rememberSession('widget', 'x', hostile));
  assert.equal(recallSession('widget', null), '');
});

test('the session notice is parsed; anything else is a provider message or nothing', () => {
  const n = parseControlFrame('{"type":"session","state":"attached","session":"sess-7"}');
  assert.ok(isSessionNotice(n));
  assert.equal(n.state, 'attached');
  assert.equal(n.session, 'sess-7');

  const msg = parseControlFrame('{"message":"shell access disabled"}');
  assert.ok(msg && !isSessionNotice(msg));
  assert.equal((msg as { message: string }).message, 'shell access disabled');

  assert.equal(parseControlFrame('not json'), null);
  assert.equal(parseControlFrame('{"type":"session","state":"maybe"}'), null);
  assert.equal(parseControlFrame('{"type":"other"}'), null);
});

test('a resumed session is shown as resumed and keeps its key', () => {
  const r = applyNotice({ type: 'session', state: 'attached', session: 'sess-7' }, 'sess-7');
  assert.equal(r.status, 'attached');
  assert.equal(r.keep, 'sess-7');
  assert.equal(r.announce, ''); // nothing to announce: the screen is the same one
});

test('a session that could not be resumed is announced, never silently swapped', () => {
  const r = applyNotice(
    { type: 'session', state: 'new', reason: 'the previous terminal session has ended' },
    'sess-gone',
  );
  assert.equal(r.status, 'new');
  assert.equal(r.keep, ''); // the dead key is dropped, not asked for again
  assert.match(r.announce, /new session/);
  assert.match(r.announce, /has ended/);
});

test('a first terminal is a new session without an announcement', () => {
  const r = applyNotice({ type: 'session', state: 'new' }, '');
  assert.equal(r.status, 'new');
  assert.equal(r.keep, '');
  assert.equal(r.announce, '');
});

test('every status has a short, factual label', () => {
  for (const [status, text] of Object.entries(statusText)) {
    assert.ok(text.length > 0 && text.length < 32, `${status}: ${text}`);
    assert.doesNotMatch(text, /mock/i); // no simulated states are described
  }
});
