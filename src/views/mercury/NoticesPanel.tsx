// NoticesPanel — the surface on which the service speaks up about itself (S14): persistent hints
// that stay until they are read (REQ-031.2), bundled with a count and a period when the same
// finding keeps happening (REQ-032.5), plus the delivery state of the daily report so a failed send
// is visible instead of vanishing (REQ-042.5). Every presentation decision lives in notices.ts and
// is unit-tested there; this file is the rendering.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useLiveTopic } from '@/state/live';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { fmtDateTime } from '@/lib/format';
import { CheckIcon, DotIcon } from '@/ui/icons';
import type { ReportDelivery } from '@/types';
import {
  isUnread,
  noticeAt,
  noticeAxioms,
  noticeLabel,
  noticeText,
  noticeTone,
  repeatSpan,
  reportStatusLine,
  sortNotices,
  unreadCount,
  type NoticeRecord,
  type NoticeTone,
} from './notices';

const errMsg = (e: unknown): string => String((e as Error)?.message ?? e);

export function NoticesPanel() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [notices, setNotices] = useState<NoticeRecord[] | null>(null);
  const [reports, setReports] = useState<ReportDelivery[]>([]);
  const [failed, setFailed] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const reload = useCallback(async () => {
    try {
      const { notices: list } = await source.mercuryRunNotices();
      setNotices(list);
      setFailed(null);
    } catch (e) {
      setFailed((prev) => prev ?? errMsg(e));
    }
    try {
      setReports((await source.mercuryReportStatus()).records);
    } catch {
      /* the report state is an addition — keep the last good one */
    }
  }, [source]);

  useEffect(() => {
    void reload();
  }, [reload]);
  // The feed refreshes on the one live stream; a resting panel makes no request (REQ-034).
  useLiveTopic('notices', () => void reload());

  // How many deliveries stand blocked right now — read off the notices the pool already carries, so
  // the panel opens no second data path for a number it is already being told.
  const blockedDeliveries = (notices ?? []).filter((n) => n.kind === 'delivery-blocked' && !n.read).length;

  // The explicit release a blocked delivery waits for (K-5). The blocked state was there from the
  // start; the way out of it was not, so a delivery blocked by a passing outage stayed blocked for
  // ever. It merges nothing — the maintenance re-evaluates and blocks again whatever really fails,
  // which is why pressing it twice is harmless.
  const release = async () => {
    setBusy(true);
    try {
      const out = await source.mercuryResumeDelivery();
      toast({
        title: out.resumed > 0 ? `${out.resumed} blocked deliveries resumed` : 'Nothing was blocked',
        description: out.resumed > 0 ? 'The next maintenance pass evaluates them again.' : undefined,
      });
      await reload();
    } catch (e) {
      toast({ title: 'The blockade could not be resumed', description: errMsg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  const dismiss = async (id: string) => {
    if (busy) return;
    setBusy(true);
    try {
      await source.mercuryDismissRunNotice(id);
      await reload();
    } catch (e) {
      toast({ title: 'Could not mark the hint as read', description: errMsg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  // K-5: a BLOCKED report delivery is resumed EXPLICITLY — it never retries on its own.
  const resumeReport = async (day: string) => {
    if (busy) return;
    setBusy(true);
    try {
      await source.mercuryResumeReportDelivery(day);
      await reload();
    } catch (e) {
      toast({ title: 'Could not resume the report delivery', description: errMsg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  const clearAll = async () => {
    if (busy) return;
    setBusy(true);
    try {
      await source.mercuryClearRunNotices();
      await reload();
    } catch (e) {
      toast({ title: 'Could not clear the hints', description: errMsg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  if (failed) {
    return (
      <div className="flex h-full min-h-0 items-center justify-center p-8">
        <p className="max-w-md text-center text-footnote text-danger">The hints could not be read: {failed}</p>
      </div>
    );
  }

  const list = notices ? sortNotices(notices) : [];
  const open = notices ? unreadCount(notices) : 0;
  const status = reportStatusLine(reports);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex shrink-0 flex-wrap items-center gap-2 border-b border-separator px-3 py-2">
        <h2 className="text-subhead font-medium text-text-primary">Notices</h2>
        <span className="text-caption text-text-tertiary">
          {open > 0 ? `${open} unread` : notices && notices.length > 0 ? 'all read' : ''}
        </span>
        <div className="ml-auto">
          <Button
            variant="ghost"
            size="sm"
            disabled={busy || !notices || notices.length === 0}
            onClick={() => void clearAll()}
          >
            Clear all
          </Button>
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        {/* A blocked delivery waits for a person to say "try again" — that is what replaces endless
            retrying (K-5/REQ-016.3). Until this control existed the state had no way out at all,
            and one passing outage could halt every delivery for good. It merges nothing: the next
            maintenance pass re-evaluates and blocks again whatever really fails. */}
        {blockedDeliveries > 0 && (
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-md bg-danger/[0.10] px-3 py-2 text-caption text-danger">
            <span>
              {blockedDeliveries} {blockedDeliveries === 1 ? 'delivery is' : 'deliveries are'} blocked and wait for a release.
            </span>
            <Button variant="secondary" size="sm" className="ml-auto" disabled={busy} onClick={() => void release()}>
              Release
            </Button>
          </div>
        )}
        {status && (
          <div className={cn('mb-3 flex flex-wrap items-center gap-2 rounded-md px-3 py-2 text-caption', toneBox(status.tone))}>
            <span>
              {status.text}
              {status.at ? ` · ${fmtDateTime(status.at)}` : ''}
            </span>
            {status.blockedDay && (
              <Button
                variant="secondary"
                size="sm"
                className="ml-auto"
                disabled={busy}
                onClick={() => void resumeReport(status.blockedDay!)}
              >
                Resume
              </Button>
            )}
          </div>
        )}

        {notices === null ? (
          <p className="px-2.5 py-3 text-caption text-text-tertiary">Loading…</p>
        ) : list.length === 0 ? (
          <p className="px-2.5 py-3 text-caption text-text-tertiary">
            No hints. The service reports here when something needs attention.
          </p>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {list.map((n) => (
              <NoticeRow key={n.id} notice={n} busy={busy} onDismiss={() => void dismiss(n.id)} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function NoticeRow({ notice, busy, onDismiss }: { notice: NoticeRecord; busy: boolean; onDismiss: () => void }) {
  const unread = isUnread(notice);
  const tone = noticeTone(notice);
  const span = repeatSpan(notice);
  const axioms = noticeAxioms(notice);

  return (
    <li
      className={cn(
        'rounded-md border px-3 py-2',
        unread ? 'border-separator bg-fill/[0.04]' : 'border-separator opacity-60',
      )}
    >
      <div className="flex items-start gap-2">
        <span className={cn('mt-0.5 shrink-0', toneText(tone))}>
          {unread ? <DotIcon className="h-3.5 w-3.5" /> : <CheckIcon className="h-3.5 w-3.5" />}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className={cn('rounded px-1.5 py-0.5 text-caption', toneBox(tone))}>{noticeLabel(notice)}</span>
            {notice.repo && <span className="text-caption text-text-secondary">{notice.repo}</span>}
            <span className="text-caption text-text-tertiary">{fmtDateTime(noticeAt(notice))}</span>
          </div>
          <p className="mt-1 break-words text-footnote text-text-primary">{noticeText(notice)}</p>
          {notice.nextStep && <p className="mt-0.5 text-caption text-text-secondary">Next: {notice.nextStep}</p>}
          {span && (
            <p className="mt-0.5 text-caption text-text-tertiary">
              {span.count}× · {fmtDateTime(span.firstAt)} – {fmtDateTime(span.lastAt)}
            </p>
          )}
          {axioms.length > 0 && <p className="mt-0.5 text-caption text-text-tertiary">{axioms.join(' · ')}</p>}
        </div>
        {unread && (
          <Button variant="ghost" size="sm" disabled={busy} onClick={onDismiss}>
            Mark read
          </Button>
        )}
      </div>
    </li>
  );
}

function toneBox(tone: NoticeTone): string {
  return tone === 'alarm' ? 'bg-danger/[0.10] text-danger' : 'bg-fill/10 text-text-secondary';
}

function toneText(tone: NoticeTone): string {
  return tone === 'alarm' ? 'text-danger' : 'text-text-tertiary';
}
