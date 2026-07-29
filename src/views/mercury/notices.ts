// Pure presentation logic of the notice surface (S14). No React, no data source — everything here
// is unit-tested with node --test (notices.test.ts). The rule it exists to keep: EVERY record the
// pool can hold renders a defined state. A kind this build has never heard of still gets a label,
// a text and a tone, so an unknown notice is never a black screen.
// Dates are NOT formatted here: this module decides WHAT is said (bundled or not, delivered or
// failed) and hands the moments on as ISO strings; how a moment looks is the one shared formatter's
// job (lib/format), applied by the component.
import type { ReportDelivery, RunNotice, ServiceNotice } from '../../types';

/** A notice as the pool actually serves it: the assignment facts of `RunNotice` PLUS the finding
 *  shape of `ServiceNotice` (repo/text/nextStep and the bundling count+period+read). The two wire
 *  types describe the same record from two sides; the union is what the panel reads, and the
 *  optionality keeps older rows — written before a field existed — renderable. */
export type NoticeRecord = Omit<RunNotice, 'kind'> &
  Partial<Omit<ServiceNotice, 'id' | 'kind'>> & { kind: string };

/** How a notice reads: an alarm asks for action, a note only informs. This is a RENDERING
 *  decision (which colour, which order) — the service itself reports without judging. */
export type NoticeTone = 'alarm' | 'note';

/** The kinds this build knows, with their label and tone. An absent kind is not an error: it falls
 *  through to `humanizeKind` below. */
const KINDS: Record<string, { label: string; tone: NoticeTone }> = {
  // Delivery findings — work that did not reach the user.
  'delivery-alarm': { label: 'Delivery alarm', tone: 'alarm' },
  'delivery-blocked': { label: 'Delivery blocked', tone: 'alarm' },
  'delivery-selfcheck': { label: 'Self-check', tone: 'alarm' },
  'code-structure-violation': { label: 'Code structure', tone: 'alarm' },
  // Delivery-origin findings.
  'protection-deviation': { label: 'Protection deviation', tone: 'alarm' },
  'admin-override': { label: 'Emergency override', tone: 'alarm' },
  // Operating events.
  'execution-abandoned': { label: 'Execution abandoned', tone: 'alarm' },
  'restart-queued-start-failed': { label: 'Queued start failed', tone: 'alarm' },
  'restart-requested': { label: 'Restart requested', tone: 'note' },
  'restart-completed': { label: 'Restart completed', tone: 'note' },
  'startup-reconcile': { label: 'Startup reconcile', tone: 'note' },
  // The automatic axiom→run assignment.
  assigned: { label: 'Axioms assigned', tone: 'note' },
  failed: { label: 'Assignment failed', tone: 'alarm' },
};

/** Turns any kind into a readable label: "some-new-kind" → "Some new kind". */
export function humanizeKind(kind: string): string {
  const words = kind.replace(/[-_]+/g, ' ').trim();
  if (!words) return 'Notice';
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function noticeLabel(n: NoticeRecord): string {
  return KINDS[n.kind]?.label ?? humanizeKind(n.kind);
}

export function noticeTone(n: NoticeRecord): NoticeTone {
  return KINDS[n.kind]?.tone ?? 'note';
}

/** The notice's wording. `text` is the field the finding writes, `reason` the one the older
 *  callers write, and an assignment carries neither — its facts ARE the sentence. Nothing renders
 *  empty: the last resort names the kind. */
export function noticeText(n: NoticeRecord): string {
  const written = (n.text ?? '').trim() || (n.reason ?? '').trim();
  if (written) return written;
  if (n.kind === 'assigned') return assignmentSentence(n);
  return noticeLabel(n);
}

function assignmentSentence(n: NoticeRecord): string {
  const count = n.axioms?.length ?? n.axiomIds?.length ?? 0;
  const subject = count === 1 ? '1 axiom' : `${count} axioms`;
  const run = n.runName ? `“${n.runName}”` : 'a run';
  return n.newRun ? `${subject} started the new run ${run}` : `${subject} joined ${run}`;
}

/** The axiom titles an assignment notice names (empty for every other kind). */
export function noticeAxioms(n: NoticeRecord): string[] {
  return n.axioms ?? [];
}

/** A bundled notice states how often it happened over which period (REQ-032.5); a single
 *  occurrence has nothing to bundle and returns null. */
export function repeatSpan(n: NoticeRecord): { count: number; firstAt: string; lastAt: string } | null {
  const count = n.count ?? 1;
  if (count <= 1) return null;
  return { count, firstAt: n.firstAt ?? n.at, lastAt: n.lastAt ?? n.at };
}

/** When the notice last occurred — the moment the list sorts and displays by. */
export function noticeAt(n: NoticeRecord): string {
  return n.lastAt ?? n.at;
}

/** A notice is unread until it is dismissed. A record from before the read flag existed counts as
 *  unread: it is safer to show a hint once too often than to hide one. */
export function isUnread(n: NoticeRecord): boolean {
  return n.read !== true;
}

export function unreadCount(list: readonly NoticeRecord[]): number {
  return list.filter(isUnread).length;
}

/** Unread first, then newest first — what still needs attention leads, and within each group the
 *  most recent occurrence does. Stable: never mutates the input. */
export function sortNotices(list: readonly NoticeRecord[]): NoticeRecord[] {
  return [...list].sort((a, b) => {
    if (isUnread(a) !== isUnread(b)) return isUnread(a) ? -1 : 1;
    return noticeAt(b).localeCompare(noticeAt(a));
  });
}

/** What to say about the daily report's delivery (REQ-042.5): a failed send must be visible, with
 *  its reason and its attempts, instead of vanishing silently. `at`, when set, is the moment the
 *  component renders through the shared formatter. null when no report has run yet. */
export interface ReportStatusLine {
  text: string;
  tone: NoticeTone;
  at?: string;
}

export function reportStatusLine(records: readonly ReportDelivery[]): ReportStatusLine | null {
  if (records.length === 0) return null;
  const latest = [...records].sort((a, b) => b.day.localeCompare(a.day))[0];
  if (latest.status === 'failed') {
    const attempts = latest.attempts === 1 ? '1 attempt' : `${latest.attempts} attempts`;
    const why = latest.lastError ? `: ${latest.lastError}` : '';
    return {
      text: `Daily report for ${latest.day} could not be delivered after ${attempts}${why}`,
      tone: 'alarm',
      at: latest.lastAttempt,
    };
  }
  const covered = latest.executions === 1 ? '1 execution' : `${latest.executions} executions`;
  return { text: `Daily report for ${latest.day} delivered — ${covered}`, tone: 'note', at: latest.sentAt };
}
