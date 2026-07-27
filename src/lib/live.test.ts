import { test } from 'node:test';
import assert from 'node:assert/strict';
import { openLiveStream, classifyExternalChange, isMercuryTopic, type LiveEventSource, type LiveStreamDeps } from './live.ts';

/** A controllable fake EventSource + a harness that captures scheduled timers so a test drives reconnects
 *  deterministically (no real clock, no DOM). */
class FakeES implements LiveEventSource {
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  close() {
    this.closed = true;
  }
}

function harness(extra: Partial<LiveStreamDeps> = {}) {
  const created: FakeES[] = [];
  let timer: { fn: () => void; ms: number } | null = null;
  const deps: LiveStreamDeps = {
    create: () => {
      const es = new FakeES();
      created.push(es);
      return es;
    },
    setTimer: (fn, ms) => {
      timer = { fn, ms };
      return timer;
    },
    clearTimer: () => {
      timer = null;
    },
    ...extra,
  };
  return {
    deps,
    created,
    fireTimer: () => {
      const t = timer;
      timer = null;
      t?.fn();
    },
    pendingTimer: () => timer,
  };
}

test('routes valid topics and ignores junk', () => {
  const topics: string[] = [];
  const h = harness();
  openLiveStream('/x', { onTopic: (t) => topics.push(t) }, h.deps);
  const es = h.created[0];
  es.onmessage!({ data: 'runs' });
  es.onmessage!({ data: 'not-a-topic' });
  es.onmessage!({ data: 'active' });
  assert.deepEqual(topics, ['runs', 'active']);
});

test('reconnects with capped exponential backoff, reset by a healthy open', () => {
  const h = harness();
  openLiveStream('/x', { onTopic: () => {} }, h.deps);
  assert.equal(h.created.length, 1);

  h.created[0].onerror!(); // first failure
  assert.equal(h.pendingTimer()!.ms, 1000);
  h.fireTimer();
  assert.equal(h.created.length, 2);

  h.created[1].onerror!(); // second consecutive failure → doubles
  assert.equal(h.pendingTimer()!.ms, 2000);
  h.fireTimer();
  assert.equal(h.created.length, 3);

  h.created[2].onopen!(); // healthy → resets backoff
  h.created[2].onerror!();
  assert.equal(h.pendingTimer()!.ms, 1000);
});

test('refreshes the session before reconnecting', async () => {
  let refreshed = 0;
  const h = harness({ refresh: async () => void refreshed++ });
  openLiveStream('/x', { onTopic: () => {} }, h.deps);
  h.created[0].onerror!();
  h.fireTimer();
  await Promise.resolve(); // let the refresh promise settle
  await Promise.resolve();
  assert.equal(refreshed, 1);
  assert.equal(h.created.length, 2, 'reconnects even if refresh resolves');
});

test('close stops the loop and shuts the connection', () => {
  const h = harness();
  const stream = openLiveStream('/x', { onTopic: () => {} }, h.deps);
  h.created[0].onerror!();
  assert.ok(h.pendingTimer(), 'a reconnect is pending');
  stream.close();
  assert.equal(h.pendingTimer(), null, 'close clears the pending timer');
  assert.equal(h.created[0].closed, true);
});

test('classifyExternalChange truth table', () => {
  assert.equal(classifyExternalChange(undefined, { updatedAt: '2' }), 'none'); // composing new
  assert.equal(classifyExternalChange('1', { updatedAt: '1' }), 'none'); // unchanged
  assert.equal(classifyExternalChange('1', { updatedAt: '2' }), 'updated'); // changed elsewhere
  assert.equal(classifyExternalChange('1', null), 'deleted'); // removed elsewhere
  assert.equal(classifyExternalChange('1', undefined), 'deleted');
});

test('isMercuryTopic guards the vocabulary', () => {
  assert.equal(isMercuryTopic('deliveries'), true);
  assert.equal(isMercuryTopic('nope'), false);
});
