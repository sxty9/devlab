// Tests for the pure live-update core (S12, REQ-034): transport self-healing (reconnect
// with session refresh BEFORE retry), topic vocabulary, the provider's fan-out registry,
// the null-stream poll fallback, the resting-view zero-request property, and edit-conflict
// classification. Everything runs under `node --test` — no DOM, no React.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  FALLBACK_POLL_MS,
  LIVE_TOPICS,
  classifyExternalChange,
  connectLive,
  createTopicRegistry,
  isLiveTopic,
  openLiveStream,
  type LiveTiming,
} from './live.ts';

/** A controllable fake EventSource; cast to EventSource at the factory boundary. */
class FakeES {
  onopen: ((ev?: unknown) => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: ((ev?: unknown) => void) | null = null;
  closed = false;
  close() {
    this.closed = true;
  }
}

/** Captures scheduled timers/intervals so tests drive reconnects and the poll fallback
 *  deterministically (no real clock). */
function harness() {
  const created: FakeES[] = [];
  let timer: { fn: () => void; ms: number } | null = null;
  let repeat: { fn: () => void; ms: number } | null = null;
  let repeatsScheduled = 0;
  const timing: LiveTiming = {
    setTimer: (fn, ms) => {
      timer = { fn, ms };
      return timer;
    },
    clearTimer: () => {
      timer = null;
    },
    setRepeat: (fn, ms) => {
      repeatsScheduled++;
      repeat = { fn, ms };
      return repeat;
    },
    clearRepeat: () => {
      repeat = null;
    },
  };
  return {
    timing,
    created,
    open: () => {
      const es = new FakeES();
      created.push(es);
      return es as unknown as EventSource;
    },
    fireTimer: () => {
      const t = timer;
      timer = null;
      t?.fn();
    },
    fireRepeat: () => repeat?.fn(),
    pendingTimer: () => timer,
    pendingRepeat: () => repeat,
    repeatsScheduled: () => repeatsScheduled,
  };
}

/** Flushes pending microtasks (the refresh-then-reconnect chain). */
const settle = () => new Promise<void>((r) => setImmediate(r));

const noRefresh = async () => true;

// ── Vocabulary ───────────────────────────────────────────────────────────────────────────

test('LIVE_TOPICS pins exactly the backend topic set, in order', () => {
  assert.deepEqual(
    [...LIVE_TOPICS],
    ['axioms', 'runs', 'active', 'progress', 'deliveries', 'notices', 'slots', 'restart', 'questions', 'session'],
  );
});

test('isLiveTopic guards the closed set', () => {
  for (const t of LIVE_TOPICS) assert.equal(isLiveTopic(t), true);
  assert.equal(isLiveTopic('nope'), false);
  assert.equal(isLiveTopic(''), false);
  assert.equal(isLiveTopic('Runs'), false);
});

// ── Transport: openLiveStream ────────────────────────────────────────────────────────────

test('null source ⇒ null stream (the provider then falls back to a gentle poll)', () => {
  const h = harness();
  const stream = openLiveStream(() => null, noRefresh, h.timing);
  assert.equal(stream, null);
  assert.equal(h.pendingTimer(), null, 'a dead source schedules nothing');
});

test('routes every valid topic to subscribers and ignores junk', () => {
  const h = harness();
  const stream = openLiveStream(h.open, noRefresh, h.timing)!;
  const got: string[] = [];
  stream.subscribe((t) => got.push(t));
  const es = h.created[0];
  for (const t of LIVE_TOPICS) es.onmessage!({ data: t });
  es.onmessage!({ data: 'not-a-topic' });
  es.onmessage!({ data: ' runs ' }); // whitespace is trimmed, still only a topic name
  assert.deepEqual(got, [...LIVE_TOPICS, 'runs']);
});

test('unsubscribe stops delivery; a throwing subscriber does not silence the others', () => {
  const h = harness();
  const stream = openLiveStream(h.open, noRefresh, h.timing)!;
  const a: string[] = [];
  const b: string[] = [];
  stream.subscribe(() => {
    throw new Error('refetch failed');
  });
  const offA = stream.subscribe((t) => a.push(t));
  stream.subscribe((t) => b.push(t));
  h.created[0].onmessage!({ data: 'runs' });
  assert.deepEqual(a, ['runs']);
  assert.deepEqual(b, ['runs']);
  offA();
  h.created[0].onmessage!({ data: 'active' });
  assert.deepEqual(a, ['runs'], 'unsubscribed handler no longer called');
  assert.deepEqual(b, ['runs', 'active']);
});

test('reconnects with capped exponential backoff, reset by a healthy open', async () => {
  const h = harness();
  openLiveStream(h.open, noRefresh, { ...h.timing, minBackoffMs: 1000, maxBackoffMs: 2000 });
  assert.equal(h.created.length, 1);

  h.created[0].onerror!(); // first failure
  assert.equal(h.pendingTimer()!.ms, 1000);
  h.fireTimer();
  await settle();
  assert.equal(h.created.length, 2);

  h.created[1].onerror!(); // second consecutive failure → doubles
  assert.equal(h.pendingTimer()!.ms, 2000);
  h.fireTimer();
  await settle();
  assert.equal(h.created.length, 3);

  h.created[2].onerror!(); // third — capped at maxBackoffMs
  assert.equal(h.pendingTimer()!.ms, 2000);
  h.fireTimer();
  await settle();
  assert.equal(h.created.length, 4);

  h.created[3].onopen!(); // healthy → resets backoff
  h.created[3].onerror!();
  assert.equal(h.pendingTimer()!.ms, 1000);
});

test('refreshes the session BEFORE reconnecting (and reconnects either way)', async () => {
  const h = harness();
  let refreshCalls = 0;
  let release: (ok: boolean) => void = () => {};
  const gate = new Promise<boolean>((r) => {
    release = r;
  });
  openLiveStream(
    h.open,
    () => {
      refreshCalls++;
      return gate;
    },
    h.timing,
  );
  h.created[0].onerror!();
  h.fireTimer();
  await settle();
  assert.equal(refreshCalls, 1, 'the retry asks for a session refresh');
  assert.equal(h.created.length, 1, 'no reconnect before the refresh settles');
  release(true);
  await settle();
  assert.equal(h.created.length, 2, 'reconnects after the refresh resolves');

  // A REJECTING refresh still reconnects (refresh is best-effort).
  h.created[1].onerror!();
  const rejecting = harness();
  openLiveStream(rejecting.open, () => Promise.reject(new Error('expired')), rejecting.timing);
  rejecting.created[0].onerror!();
  rejecting.fireTimer();
  await settle();
  assert.equal(rejecting.created.length, 2, 'reconnects even when the refresh rejects');
});

test('open() returning null during a retry keeps the loop alive', async () => {
  const h = harness();
  let dead = false;
  const flaky = () => (dead ? null : h.open());
  openLiveStream(flaky, noRefresh, h.timing);
  assert.equal(h.created.length, 1);

  h.created[0].onerror!();
  dead = true; // the source vanishes mid-retry
  h.fireTimer();
  await settle();
  assert.equal(h.created.length, 1, 'no connection could be made');
  assert.ok(h.pendingTimer(), 'the loop schedules another retry instead of going dead');

  dead = false; // the source recovers
  h.fireTimer();
  await settle();
  assert.equal(h.created.length, 2, 'the recovered source reconnects');
});

test('a stale error from a superseded connection is ignored', async () => {
  const h = harness();
  openLiveStream(h.open, noRefresh, h.timing);
  h.created[0].onerror!();
  h.fireTimer();
  await settle();
  assert.equal(h.created.length, 2);
  h.created[0].onerror!(); // the OLD connection fires again
  assert.equal(h.pendingTimer(), null, 'a stale handler must not schedule a reconnect');
});

test('close stops the loop and shuts the connection', () => {
  const h = harness();
  const stream = openLiveStream(h.open, noRefresh, h.timing)!;
  const got: string[] = [];
  stream.subscribe((t) => got.push(t));
  h.created[0].onerror!();
  assert.ok(h.pendingTimer(), 'a reconnect is pending');
  stream.close();
  assert.equal(h.pendingTimer(), null, 'close clears the pending timer');
  assert.equal(h.created[0].closed, true);
  h.created[0].onmessage?.({ data: 'runs' });
  assert.deepEqual(got, [], 'a closed stream delivers nothing');
});

// ── The provider core: registry + connectLive ────────────────────────────────────────────

test('registry: ticks reach only the matching topic; throws are isolated; unsubscribe works', () => {
  const r = createTopicRegistry();
  let runs = 0;
  let active = 0;
  r.subscribe('runs', () => {
    throw new Error('one refetch failing must not stop the others');
  });
  const offRuns = r.subscribe('runs', () => runs++);
  r.subscribe('active', () => active++);
  r.emit('runs');
  assert.equal(runs, 1);
  assert.equal(active, 0, 'a tick reaches only its own topic');
  r.emitAll();
  assert.equal(runs, 2);
  assert.equal(active, 1);
  offRuns();
  r.emit('runs');
  assert.equal(runs, 2, 'unsubscribed refetch no longer runs');
});

test('connectLive with a push stream schedules NOTHING client-side (a resting surface causes zero requests)', () => {
  const h = harness();
  const registry = createTopicRegistry();
  let refetches = 0;
  registry.subscribe('deliveries', () => refetches++);
  const cleanup = connectLive(h.open, registry, noRefresh, h.timing);
  assert.equal(h.repeatsScheduled(), 0, 'no interval — the server pushes');
  assert.equal(h.pendingTimer(), null, 'no timer either');
  h.created[0].onmessage!({ data: 'deliveries' });
  assert.equal(refetches, 1, 'a pushed tick triggers the refetch');
  cleanup();
  assert.equal(h.created[0].closed, true, 'cleanup closes the one stream');
  h.created[0].onmessage?.({ data: 'deliveries' });
  assert.equal(refetches, 1, 'after cleanup nothing flows');
});

test('connectLive with a null stream falls back to a gentle poll — but a resting view still causes zero requests', () => {
  const h = harness();
  const registry = createTopicRegistry();
  let refetches = 0;
  const cleanup = connectLive(() => null, registry, noRefresh, h.timing);
  assert.ok(h.pendingRepeat(), 'null stream ⇒ poll fallback');
  assert.equal(h.pendingRepeat()!.ms, FALLBACK_POLL_MS, 'the fallback is gentle (seconds)');

  // Resting: nobody subscribed — beats fire, zero refetches (= zero requests).
  h.fireRepeat();
  h.fireRepeat();
  assert.equal(refetches, 0, 'a resting view causes no periodic requests');

  // An active view refetches on the beat.
  registry.subscribe('runs', () => refetches++);
  h.fireRepeat();
  assert.equal(refetches, 1);

  cleanup();
  assert.equal(h.pendingRepeat(), null, 'cleanup clears the fallback poll');
});

// ── Edit-conflict naming (4e931c8) ───────────────────────────────────────────────────────

test('classifyExternalChange truth table', () => {
  assert.equal(classifyExternalChange(undefined, { updatedAt: '2' }), 'none'); // composing new
  assert.equal(classifyExternalChange('1', { updatedAt: '1' }), 'none'); // unchanged
  assert.equal(classifyExternalChange('1', { updatedAt: '2' }), 'updated'); // changed elsewhere
  assert.equal(classifyExternalChange('1', null), 'deleted'); // removed elsewhere
  assert.equal(classifyExternalChange('1', undefined), 'deleted');
  assert.equal(classifyExternalChange('1', {}), 'none'); // server version without a stamp — never a false alarm
});
