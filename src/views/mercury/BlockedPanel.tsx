// BlockedPanel — the Blocked surface (REQ: autonomy-level questions as blockers). Everything that
// waits for a decision of the user stands here at ONE place: the question in full, which run and
// which repository it comes from, what the run did so far, and its own recommendation. The user
// answers here and the run continues with the answer — it does not start over. An unanswered
// question names since when it has been waiting (REQ point 6).
//
// Every question also carries the co-equal "no": Decline rejects it. Rejecting ENDS the run (marked
// failed, its slot freed), leaves no open question and no hanging delivery, and installs or changes
// nothing — the state before the question stands. It is always available, never gated by the approval
// checkbox, because the heavier an approval weighs the more its refusal must be within reach; it is
// also the way a stack tip a question blocked is released. A question whose run no longer exists is
// gegenstandslos: the backend retires it on its own, so it neither shows here nor holds a repository.
//
// Three kinds are special: a GUARDED question moves a security boundary (or names a step the chain
// cannot take itself), so it is presented as an explicit approval rather than a free-text answer. A
// wrapper-renewal carries the exact difference to the installed root scripts; a prod-host-key carries
// the fingerprint of a production host's new ssh key; a prod-receiver carries the command an operator
// with root runs on the production host to bring the root receiver scripts current and the checksums
// they must reach. Approving never writes anything on its own — the chain re-checks the exact content
// (the scripts still match / the host still presents the approved key / the host now carries the
// receiver scripts) before it acts or settles a delivery live; this surface only carries the user's
// decision.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useLiveTopic } from '@/state/live';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { fmtDateTime } from '@/lib/format';
import type { RunQuestion } from '@/types';
import { TaskSurface } from './tasks/Surface';

const errMsg = (e: unknown): string => String((e as Error)?.message ?? e);

/** A question still waits while it carries no answer AND has not been withdrawn — a withdrawn
 *  question (resolved without an answer, superseded by a fresh one from a restart) is no longer a
 *  blocker and drops off the surface. */
function isOpen(q: RunQuestion): boolean {
  return !q.answeredAt && !q.resolved;
}

/** "waiting 2h 5m" — the human duration since a question was raised, so the user sees how long it
 *  has stood without hunting for a timestamp. */
export function waitingSince(askedAt: string, now: number): string {
  const ms = now - new Date(askedAt).getTime();
  if (!Number.isFinite(ms) || ms < 0) return 'just now';
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m`;
  const hrs = Math.floor(mins / 60);
  const remMin = mins % 60;
  if (hrs < 24) return remMin ? `${hrs}h ${remMin}m` : `${hrs}h`;
  const days = Math.floor(hrs / 24);
  const remHr = hrs % 24;
  return remHr ? `${days}d ${remHr}h` : `${days}d`;
}

export function BlockedPanel() {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [questions, setQuestions] = useState<RunQuestion[] | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const reload = useCallback(async () => {
    try {
      const { questions: list } = await source.mercuryRunQuestions();
      setQuestions(list);
      setFailed(null);
    } catch (e) {
      setFailed((prev) => prev ?? errMsg(e));
    }
  }, [source]);

  useEffect(() => {
    void reload();
  }, [reload]);
  // Refreshes on the one live stream; a resting panel makes no request (REQ-034).
  useLiveTopic('questions', () => void reload());

  const answer = async (q: RunQuestion, text: string, approve: boolean) => {
    if (busy) return;
    setBusy(true);
    try {
      const out = await source.mercuryAnswerRunQuestion(q.id, text, approve);
      toast({
        title: out.resumed ? 'Answered — the run continues' : 'Answer recorded',
        description: out.resumed
          ? 'The run picks up where it stopped, with your answer.'
          : 'The run resumes with your answer when it is next admitted.',
      });
      await reload();
    } catch (e) {
      toast({ title: 'Could not send the answer', description: errMsg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  // Reject a question — the co-equal "no". It ends the run and frees its slot; nothing is installed
  // or changed, and no question or delivery is left holding the repository.
  const decline = async (q: RunQuestion, note: string) => {
    if (busy) return;
    setBusy(true);
    try {
      await source.mercuryDeclineRunQuestion(q.id, note || undefined);
      toast({
        title: 'Declined — the run was ended',
        description: 'The run is marked failed and its slot freed. Nothing was installed or changed.',
      });
      await reload();
    } catch (e) {
      toast({ title: 'Could not decline the question', description: errMsg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  if (failed) {
    return (
      <div className="flex h-full min-h-0 items-center justify-center p-8">
        <p className="max-w-md text-center text-footnote text-danger">The blocked questions could not be read: {failed}</p>
      </div>
    );
  }

  const open = (questions ?? []).filter(isOpen);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex shrink-0 flex-wrap items-center gap-2 border-b border-separator px-3 py-2">
        <h2 className="text-subhead font-medium text-text-primary">Blocked</h2>
        <span className="text-caption text-text-tertiary">
          {open.length > 0 ? `${open.length} waiting for you` : questions ? 'nothing waiting' : ''}
        </span>
      </header>

      {/* The Rückfragen — questions that wait for a DECISION of the user. They stand ABOVE the task
          list: a question is a different thing from a failed layer and does not replace it. The card
          hides itself when nothing waits. A wrapper-renewal is presented as an explicit approval. */}
      {questions === null ? (
        <p className="shrink-0 px-3 py-3 text-caption text-text-tertiary">Loading…</p>
      ) : open.length > 0 ? (
        <section className="max-h-[45%] shrink-0 overflow-y-auto border-b border-separator px-3 py-3">
          <h3 className="mb-2 text-caption font-medium uppercase tracking-wide text-text-tertiary">Questions</h3>
          <ul className="flex flex-col gap-3">
            {open.map((q) => (
              <QuestionRow key={q.id} question={q} busy={busy} onAnswer={answer} onDecline={decline} />
            ))}
          </ul>
        </section>
      ) : null}

      {/* The failed / stuck TODOS — the SAME TaskSurface every neighbour lifecycle tab uses (Todos,
          Pending), narrowed to the 'blocked' bucket, with the same rows and the same handles: open
          the task, run it again. A failed layer is a blocker too (WHAT-4), so it stands here — but as
          a work list like its neighbours, not as a compact read-only log beside them. */}
      <div className="min-h-0 flex-1">
        <TaskSurface
          kind="todo"
          newLabel="New todo"
          buckets={['blocked']}
          emptyText="Nothing is stuck. A run that failed, left a target uncovered, or stopped at a broken delivery tip waits here until it is run again or rolled back."
        />
      </div>
    </div>
  );
}

function QuestionRow({
  question: q,
  busy,
  onAnswer,
  onDecline,
}: {
  question: RunQuestion;
  busy: boolean;
  onAnswer: (q: RunQuestion, text: string, approve: boolean) => void | Promise<void>;
  onDecline: (q: RunQuestion, note: string) => void | Promise<void>;
}) {
  const wrapper = q.qKind === 'wrapper-renewal';
  const hostKey = q.qKind === 'prod-host-key';
  const receiver = q.qKind === 'prod-receiver';
  const guarded = wrapper || hostKey || receiver;
  const [text, setText] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const waited = waitingSince(q.askedAt, Date.now());

  // The consent the user affirms is the ONE sentence the backend derived from THIS question's subject
  // (which version, which files and checksums, or which host key) — never a text formulated here beside
  // it. The backend records the same sentence as the ledger answer, so the note is only the user's
  // optional addition; the affirmation itself is not typed here and cannot drift from what is booked.
  const consent = q.approvalStatement ?? '';
  const canSend = guarded ? confirmed : text.trim().length > 0;
  const send = () => {
    if (!canSend) return;
    void onAnswer(q, text.trim(), guarded);
  };

  return (
    <li className={cn('rounded-md border px-3 py-2.5', guarded ? 'border-danger/40 bg-danger/[0.05]' : 'border-separator bg-fill/[0.04]')}>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className={cn('rounded px-1.5 py-0.5 text-caption', guarded ? 'bg-danger/[0.12] text-danger' : 'bg-fill/10 text-text-secondary')}>
          {guarded ? 'Approval required' : 'Question'}
        </span>
        <span className="text-caption font-medium text-text-primary">{q.runTitle || q.runId}</span>
        <span className="text-caption text-text-secondary">{q.repo}</span>
        <span className="ml-auto text-caption text-text-tertiary" title={fmtDateTime(q.askedAt)}>
          waiting {waited}
        </span>
      </div>

      <p className="mt-2 whitespace-pre-wrap break-words text-footnote text-text-primary">{q.question}</p>

      {q.recommendation && (
        <div className="mt-2 rounded bg-fill/[0.06] px-2.5 py-1.5">
          <p className="text-caption font-medium text-text-secondary">Its recommendation</p>
          <p className="mt-0.5 whitespace-pre-wrap break-words text-caption text-text-secondary">{q.recommendation}</p>
        </div>
      )}

      {q.progress && (
        <details className="mt-2">
          <summary className="cursor-pointer text-caption text-text-tertiary">What the run did so far</summary>
          <pre className="mt-1 max-h-60 overflow-auto whitespace-pre-wrap break-words rounded bg-fill/[0.04] px-2.5 py-1.5 text-caption text-text-secondary">
            {q.progress}
          </pre>
        </details>
      )}

      {guarded && q.detail && (
        <details className="mt-2" open>
          <summary className="cursor-pointer text-caption text-danger">
            {wrapper
              ? 'What would be installed (the version this delivery ships)'
              : receiver
                ? 'The command to run on the production host, and the checksums it must reach'
                : 'The new host key that would be trusted'}
          </summary>
          <pre className="mt-1 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded bg-fill/[0.04] px-2.5 py-1.5 text-caption text-text-secondary">
            {q.detail}
          </pre>
        </details>
      )}

      <div className="mt-2.5 flex flex-col gap-2">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={guarded ? 2 : 3}
          placeholder={guarded ? 'Optional note' : 'Your answer — the run continues with it'}
          className="w-full resize-y rounded border border-separator bg-bg-raised px-2.5 py-1.5 text-footnote text-text-primary placeholder:text-text-tertiary"
        />
        {guarded && (
          <label className="flex items-start gap-2 text-caption text-text-secondary">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(e) => setConfirmed(e.target.checked)}
              className="mt-0.5"
            />
            <span>{consent}</span>
          </label>
        )}
        {/* The two co-equal outcomes stand side by side. Decline is ALWAYS available — never gated by
            the approval checkbox — because the heavier the approval, the more its refusal must be
            within reach. It ends the run; the state before the question stands unchanged. */}
        <div className="flex flex-wrap items-center gap-2">
          <Button variant={guarded ? 'secondary' : 'primary'} size="sm" disabled={busy || !canSend} onClick={send}>
            {guarded ? 'Approve & continue' : 'Answer & continue'}
          </Button>
          <Button variant="danger" size="sm" disabled={busy} onClick={() => void onDecline(q, text.trim())}>
            Decline & end run
          </Button>
        </div>
      </div>
    </li>
  );
}
