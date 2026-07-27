// Run with:  node --test --experimental-strip-types src/lib/live.test.ts
// (see package.json "test"). No DOM/React — the stream takes its EventSource, timers and refresh
// through the deps seam, so a fake drives every branch deterministically.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  openLiveStream,
  classifyExternalChange,
  type LiveEventSource,
  type LiveStreamDeps,
  type LiveStreamHandlers,
} from './live.ts';

class FakeES implements LiveEventSource {
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  close() {
    this.closed = true;
  }
  emit(data: string) {
    this.onmessage?.({ data });
  }
  fail() {
    this.onerror?.();
  }
  open() {
    this.onopen?.();
  }
}

interface Timer {
  fn: () => void;
  ms: number;
  cleared: boolean;
}

function harness(opts?: { withRefresh?: boolean }) {
  const created: FakeES[] = [];
  const timers: Timer[] = [];
  const topics: string[] = [];
  const statuses: boolean[] = [];
  let refreshCalls = 0;

  const deps: LiveStreamDeps = {
    create: () => {
      const es = new FakeES();
      created.push(es);
      return es;
    },
    setTimer: (fn, ms) => {
      const t: Timer = { fn, ms, cleared: false };
      timers.push(t);
      return t;
    },
    clearTimer: (h) => {
      (h as Timer).cleared = true;
    },
    minBackoffMs: 1000,
    maxBackoffMs: 8000,
    ...(opts?.withRefresh
      ? {
          refresh: async () => {
            refreshCalls++;
          },
        }
      : {}),
  };
  const handlers: LiveStreamHandlers = {
    onTopic: (t) => topics.push(t),
    onStatus: (c) => statuses.push(c),
  };
  const stream = openLiveStream('/events', handlers, deps);
  return {
    created,
    timers,
    topics,
    statuses,
    stream,
    get refreshCalls() {
      return refreshCalls;
    },
    runLastTimer() {
      const t = timers[timers.length - 1];
      if (t && !t.cleared) t.fn();
    },
  };
}

test('routes valid topics to onTopic and ignores anything else', () => {
  const h = harness();
  assert.equal(h.created.length, 1);
  h.created[0].emit('runs');
  h.created[0].emit('not-a-topic');
  h.created[0].emit('active');
  assert.deepEqual(h.topics, ['runs', 'active']);
  h.stream.close();
});

test('an externally published change surfaces on the open stream (no reconnect)', () => {
  const h = harness();
  h.created[0].open();
  h.created[0].emit('axioms');
  assert.deepEqual(h.topics, ['axioms']);
  assert.equal(h.created.length, 1, 'must not open a second connection for a normal event');
  h.stream.close();
});

test('reconnects with capped exponential backoff after an interruption', () => {
  const h = harness();
  const first = h.created[0];
  first.fail();
  assert.equal(first.closed, true, 'the dead connection is closed');
  assert.equal(h.timers[0].ms, 1000, 'first retry after the minimum backoff');

  h.runLastTimer(); // reconnect
  assert.equal(h.created.length, 2, 'a fresh connection is opened');

  h.created[1].fail();
  assert.equal(h.timers[1].ms, 2000, 'backoff doubles on the next consecutive failure');
  h.stream.close();
});

test('a healthy open resets the backoff', () => {
  const h = harness();
  h.created[0].fail();
  assert.equal(h.timers[0].ms, 1000);
  h.runLastTimer();
  h.created[1].open(); // healthy again
  h.created[1].fail();
  assert.equal(h.timers[1].ms, 1000, 'backoff reset to the minimum after a good connection');
  h.stream.close();
});

test('refreshes the session before reconnecting (recovers an expired token)', async () => {
  const h = harness({ withRefresh: true });
  h.created[0].fail();
  h.runLastTimer(); // fires refresh().then(connect)
  await new Promise((r) => setImmediate(r)); // flush the refresh microtask chain
  assert.equal(h.refreshCalls, 1);
  assert.equal(h.created.length, 2, 'reconnects after the refresh');
  h.stream.close();
});

test('close() closes the active connection and stops the reconnect loop', () => {
  const h = harness();
  h.created[0].fail(); // schedules a reconnect
  h.stream.close();
  assert.equal(h.timers[0].cleared, true, 'pending reconnect timer is cleared');
  const before = h.created.length;
  h.runLastTimer(); // must not resurrect the stream
  assert.equal(h.created.length, before, 'no reconnect after close');
});

test('close() with a live connection closes it (a closed view causes no load)', () => {
  const h = harness();
  h.stream.close();
  assert.equal(h.created[0].closed, true);
});

test('classifyExternalChange names updated/deleted and ignores an untracked draft', () => {
  assert.equal(classifyExternalChange(undefined, { updatedAt: 'z' }), 'none', 'creating new → never a conflict');
  assert.equal(classifyExternalChange('t1', { updatedAt: 't1' }), 'none', 'unchanged → no conflict');
  assert.equal(classifyExternalChange('t1', { updatedAt: 't2' }), 'updated', 'changed elsewhere');
  assert.equal(classifyExternalChange('t1', null), 'deleted', 'removed elsewhere');
  assert.equal(classifyExternalChange('t1', undefined), 'deleted');
});
