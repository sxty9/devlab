import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  humanizeKind,
  isUnread,
  noticeAxioms,
  noticeLabel,
  noticeText,
  noticeTone,
  repeatSpan,
  reportStatusLine,
  sortNotices,
  unreadCount,
  type NoticeRecord,
} from './notices.ts';
import type { ReportDelivery } from '../../types';

function notice(over: Partial<NoticeRecord> = {}): NoticeRecord {
  return {
    id: 'ntc_1',
    at: '2026-07-26T08:00:00Z',
    kind: 'delivery-alarm',
    axiomIds: [],
    axioms: [],
    ...over,
  };
}

// Every finding the pool can hold renders a label, a text and a tone — including a kind this build
// has never heard of. No record produces a black screen.
test('every notice kind renders a defined state', () => {
  const kinds = [
    'delivery-alarm',
    'delivery-blocked',
    'delivery-selfcheck',
    'code-structure-violation',
    'protection-deviation',
    'admin-override',
    'startup-reconcile',
    'restart-requested',
    'restart-completed',
    'restart-queued-start-failed',
    'execution-abandoned',
    'assigned',
    'failed',
    'a-kind-from-the-future',
    '',
  ];
  for (const kind of kinds) {
    const n = notice({ kind, text: kind === '' ? '' : 'something happened' });
    assert.ok(noticeLabel(n).length > 0, `${kind}: label`);
    assert.ok(noticeText(n).length > 0, `${kind}: text`);
    assert.ok(['alarm', 'note'].includes(noticeTone(n)), `${kind}: tone`);
  }
});

test('an unknown kind is humanized rather than shown raw', () => {
  assert.equal(humanizeKind('some-new-kind'), 'Some new kind');
  assert.equal(humanizeKind('orphan_rescued'), 'Orphan rescued');
  assert.equal(humanizeKind(''), 'Notice');
  assert.equal(noticeLabel(notice({ kind: 'orphan-rescued' })), 'Orphan rescued');
});

test('findings read as alarms, operating events as notes', () => {
  assert.equal(noticeTone(notice({ kind: 'delivery-alarm' })), 'alarm');
  assert.equal(noticeTone(notice({ kind: 'admin-override' })), 'alarm');
  assert.equal(noticeTone(notice({ kind: 'protection-deviation' })), 'alarm');
  assert.equal(noticeTone(notice({ kind: 'restart-completed' })), 'note');
  assert.equal(noticeTone(notice({ kind: 'assigned' })), 'note');
});

// The wording comes from whichever field the raising package filled — and an assignment, which
// fills neither, gets its facts turned into a sentence.
test('the text falls back from text to reason to the record’s own facts', () => {
  assert.equal(noticeText(notice({ text: 'implemented without dev delivery' })), 'implemented without dev delivery');
  assert.equal(noticeText(notice({ text: '', reason: 'older wording' })), 'older wording');
  assert.equal(
    noticeText(notice({ kind: 'assigned', runName: 'Architecture', newRun: true, axioms: ['Alpha', 'Beta'] })),
    '2 axioms started the new run “Architecture”',
  );
  assert.equal(
    noticeText(notice({ kind: 'assigned', runName: 'Architecture', axioms: ['Alpha'] })),
    '1 axiom joined “Architecture”',
  );
  assert.deepEqual(noticeAxioms(notice({ kind: 'assigned', axioms: ['Alpha'] })), ['Alpha']);
  assert.deepEqual(noticeAxioms(notice()), []);
});

// REQ-032.5: a bundled notice states its count and its period; a single occurrence states nothing.
test('a bundled notice carries count and period', () => {
  const span = repeatSpan(notice({ count: 7, firstAt: '2026-07-26T08:00:00Z', lastAt: '2026-07-26T11:00:00Z' }));
  assert.deepEqual(span, { count: 7, firstAt: '2026-07-26T08:00:00Z', lastAt: '2026-07-26T11:00:00Z' });
  // A record written before the period existed still spans something renderable.
  assert.deepEqual(repeatSpan(notice({ count: 3 })), {
    count: 3,
    firstAt: '2026-07-26T08:00:00Z',
    lastAt: '2026-07-26T08:00:00Z',
  });
  assert.equal(repeatSpan(notice({ count: 1 })), null);
  assert.equal(repeatSpan(notice()), null);
});

// REQ-031.2: unread hints lead the list, and a record from before the read flag existed counts as
// unread — showing a hint once too often beats hiding one.
test('unread hints come first, then the newest', () => {
  const list: NoticeRecord[] = [
    notice({ id: 'read-new', read: true, lastAt: '2026-07-26T12:00:00Z' }),
    notice({ id: 'unread-old', read: false, lastAt: '2026-07-20T09:00:00Z' }),
    notice({ id: 'unread-new', read: false, lastAt: '2026-07-26T09:00:00Z' }),
    notice({ id: 'legacy', at: '2026-07-25T09:00:00Z' }),
  ];
  const sorted = sortNotices(list);
  assert.deepEqual(
    sorted.map((n) => n.id),
    ['unread-new', 'legacy', 'unread-old', 'read-new'],
  );
  assert.equal(unreadCount(list), 3);
  assert.equal(isUnread(notice({ read: true })), false);
  assert.equal(isUnread(notice()), true);
  // Sorting never mutates the input.
  assert.equal(list[0].id, 'read-new');
});

// REQ-042.5: a failed send is visible with its reason and its attempts; a successful one states
// what it covered; nothing yet states nothing.
test('the report status line is honest about a failed send', () => {
  const failed: ReportDelivery[] = [
    { recipient: 'ada', day: '2026-07-25', status: 'sent', executions: 2, attempts: 1, sentAt: '2026-07-26T02:00:00Z' },
    { recipient: 'ada', day: '2026-07-26', status: 'failed', executions: 1, attempts: 3, lastError: 'mail service down' },
  ];
  const line = reportStatusLine(failed);
  assert.ok(line);
  assert.equal(line!.tone, 'alarm');
  assert.ok(line!.text.includes('2026-07-26'), line!.text);
  assert.ok(line!.text.includes('3 attempts'), line!.text);
  assert.ok(line!.text.includes('mail service down'), line!.text);

  const sent = reportStatusLine([failed[0]]);
  assert.equal(sent!.tone, 'note');
  assert.ok(sent!.text.includes('2 executions'), sent!.text);
  assert.equal(sent!.at, '2026-07-26T02:00:00Z');

  assert.equal(reportStatusLine([]), null);
});

// K-5: a BLOCKED delivery reads as blocked — with its reason, its attempts and the day that waits.
// Rendering it as a delivered day (or as a failure still being retried) would state something that is
// not happening: nothing is attempted again until it is resumed.
test('the report status line names a blocked delivery and the day it waits on', () => {
  const blocked: ReportDelivery[] = [
    { recipient: 'ada', day: '2026-07-25', status: 'sent', executions: 2, attempts: 1, sentAt: '2026-07-26T02:00:00Z' },
    {
      recipient: 'ada',
      day: '2026-07-26',
      status: 'blocked',
      executions: 1,
      attempts: 1,
      lastAttempt: '2026-07-27T00:05:00Z',
      lastError: 'mailer: no internal secret configured',
      backoff: {
        reason: 'mailer: no internal secret configured',
        class: 'permanent',
        attempts: 1,
        firstAt: '2026-07-27T00:05:00Z',
        lastAt: '2026-07-27T00:05:00Z',
        nextAt: '0001-01-01T00:00:00Z',
      },
    },
  ];
  const line = reportStatusLine(blocked);
  assert.ok(line);
  assert.equal(line!.tone, 'alarm');
  assert.equal(line!.blockedDay, '2026-07-26', 'the blocked day must be named so it can be resumed');
  assert.ok(line!.text.includes('2026-07-26'), line!.text);
  assert.ok(line!.text.includes('blocked'), line!.text);
  assert.ok(line!.text.includes('1 attempt'), line!.text);
  assert.ok(line!.text.includes('no internal secret'), line!.text);
  assert.equal(line!.at, '2026-07-27T00:05:00Z');
  assert.ok(!line!.text.includes('delivered'), line!.text);

  // A merely failed day is still being retried on its own: it does not read as blocked and offers
  // nothing to resume.
  const retrying = reportStatusLine([{ ...blocked[1], status: 'failed' }]);
  assert.ok(!retrying!.text.includes('blocked'), retrying!.text);
  assert.equal(retrying!.blockedDay, undefined);
});
