// The pure logic behind following a session: how a freshly read portion joins what is already
// held, when an input belongs on the screen, and how a steered run is named. Pure DI logic — no
// DOM, no network (frontend tests carry no jsdom).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EMPTY_SESSION, foldPortion, speakableHere, steeredBy, type HeldSession } from './logic.ts';
import type { RunResult, SessionPortion } from '@/types';

function portion(p: Partial<SessionPortion>): SessionPortion {
  return { lines: [], from: 0, next: 0, older: false, open: false, ...p };
}
const said = (text: string) => ({ at: '2026-08-09T12:00:00Z', repo: 'alpha', text });
const spoke = (from: string, text: string) => ({ at: '2026-08-09T12:01:00Z', repo: 'alpha', from, text });

// The opening read takes BOTH anchors from the portion it got: a viewer opens on the newest lines
// and knows both where to continue and where to look for earlier ones.
test('the opening portion sets both anchors', () => {
  const held = foldPortion(EMPTY_SESSION, portion({ lines: [said('a'), said('b')], from: 40, next: 120, older: true, open: true }), false);
  assert.deepEqual(
    held.lines.map((l) => l.text),
    ['a', 'b'],
  );
  assert.equal(held.from, 40);
  assert.equal(held.next, 120);
  assert.equal(held.older, true);
  assert.equal(held.open, true);
});

// Following appends and moves ONLY the end anchor — the start must stay where it is, or "earlier
// lines" would ask for the wrong place and skip everything in between.
test('following appends and moves only the end anchor', () => {
  const opened = foldPortion(EMPTY_SESSION, portion({ lines: [said('a')], from: 40, next: 80, older: true }), false);
  const followed = foldPortion(opened, portion({ lines: [said('b')], from: 80, next: 130 }), false);
  assert.deepEqual(
    followed.lines.map((l) => l.text),
    ['a', 'b'],
  );
  assert.equal(followed.from, 40, 'the start anchor moved — earlier lines would be fetched from the wrong place');
  assert.equal(followed.next, 130);
  assert.equal(followed.older, true, 'the knowledge that earlier lines exist was lost');
});

// A tick with nothing new must not disturb what is held: the anchor never goes backwards.
test('an empty follow-up changes nothing and never rewinds the anchor', () => {
  const opened = foldPortion(EMPTY_SESSION, portion({ lines: [said('a')], from: 40, next: 80 }), false);
  const same = foldPortion(opened, portion({ from: 80, next: 80 }), false);
  assert.equal(same.lines.length, 1);
  assert.equal(same.next, 80);
  const stale = foldPortion(same, portion({ from: 0, next: 0 }), false);
  assert.equal(stale.next, 80, 'a stale answer rewound the anchor — the same lines would arrive twice');
});

// Earlier lines go in FRONT and move only the start anchor.
test('earlier lines are prepended and move only the start anchor', () => {
  const opened = foldPortion(EMPTY_SESSION, portion({ lines: [said('c')], from: 80, next: 120, older: true }), false);
  const older = foldPortion(opened, portion({ lines: [said('a'), said('b')], from: 0, next: 80, older: false }), true);
  assert.deepEqual(
    older.lines.map((l) => l.text),
    ['a', 'b', 'c'],
  );
  assert.equal(older.from, 0);
  assert.equal(older.next, 120, 'the end anchor moved backwards — following would replay old lines');
  assert.equal(older.older, false, 'the start of the session was reached and the pane still offers to load more');
});

// Reload lands in the same session AT THE SAME PLACE: reading afresh puts the viewer back on the
// newest lines with working anchors — the same state the pane had before, not the top of the record.
test('a fresh read puts the viewer back at the current end of the session', () => {
  const before = foldPortion(EMPTY_SESSION, portion({ lines: [said('a'), said('b')], from: 40, next: 120, older: true, open: true }), false);
  const afterReload = foldPortion(EMPTY_SESSION, portion({ lines: [said('a'), said('b')], from: 40, next: 120, older: true, open: true }), false);
  assert.deepEqual(afterReload, before);
});

// An input belongs on the screen only where a message could actually go: this repository's own
// conversation must be open, not merely some conversation of the execution.
test('an input belongs only where this repository is working', () => {
  const closed: HeldSession = { ...EMPTY_SESSION, open: false, repos: [] };
  assert.equal(speakableHere(closed, 'alpha'), false);

  const other: HeldSession = { ...EMPTY_SESSION, open: true, repos: ['beta'] };
  assert.equal(speakableHere(other, 'alpha'), false, 'an input was offered for a repository that is not working');

  const here: HeldSession = { ...EMPTY_SESSION, open: true, repos: ['alpha', 'beta'] };
  assert.equal(speakableHere(here, 'alpha'), true);

  // An open session that names no repositories takes a message either way.
  const unnamed: HeldSession = { ...EMPTY_SESSION, open: true, repos: [] };
  assert.equal(speakableHere(unnamed, 'alpha'), true);
});

// A person's words and the agent's own output live in the same record and stay distinguishable.
test('a person’s line is marked as theirs', () => {
  const held = foldPortion(EMPTY_SESSION, portion({ lines: [said('reading the repository'), spoke('ada', 'stop')] }), false);
  assert.equal(held.lines[0].from, undefined);
  assert.equal(held.lines[1].from, 'ada');
});

// A run a person wrote into names them, each once, and never reads as purely self-acting.
test('a steered run names who stepped in', () => {
  const base = { id: 'exec_1', runId: 'run_x', kind: 'todo', startedAt: '', repos: [], usage: { inputTokens: 0, outputTokens: 0, costUsd: 0 }, requested: { created: { user: 'ada' }, createdAt: '', updated: { user: 'ada' }, updatedAt: '' } } as unknown as RunResult;
  assert.deepEqual(steeredBy(base), []);

  const steered = {
    ...base,
    interventions: [
      { by: { user: 'ada' }, at: '2026-08-09T12:00:00Z' },
      { by: { user: 'ada' }, at: '2026-08-09T12:01:00Z' },
      { by: { user: 'bo' }, at: '2026-08-09T12:02:00Z' },
    ],
  } satisfies RunResult;
  assert.deepEqual(steeredBy(steered), ['ada', 'bo']);

  // A record that cannot say who still says that somebody did.
  const anonymous = { ...base, interventions: [{ by: { user: '' }, at: '2026-08-09T12:00:00Z' }] } satisfies RunResult;
  assert.deepEqual(steeredBy(anonymous), ['someone']);
});
