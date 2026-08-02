// BlockedPanel — the Blocked surface (REQ: autonomy-level questions as blockers). Everything that
// waits for a decision of the user stands here at ONE place: the question in full, which run and
// which repository it comes from, what the run did so far, and its own recommendation. The user
// answers here and the run continues with the answer — it does not start over. An unanswered
// question never vanishes and names since when it has been waiting (REQ point 6).
//
// One kind is special: a wrapper-renewal question moves a security boundary, so it is presented as
// an explicit approval with the exact difference to the installed root scripts. The approval never
// writes anything — the run re-checks the scripts actually match before it installs (see the
// executor); this surface only carries the user's decision.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { useLiveTopic } from '@/state/live';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { cn } from '@/lib/cn';
import { fmtDateTime } from '@/lib/format';
import type { RunQuestion } from '@/types';

const errMsg = (e: unknown): string => String((e as Error)?.message ?? e);

/** A question still waits while it carries no answer. */
function isOpen(q: RunQuestion): boolean {
  return !q.answeredAt;
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

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        {questions === null ? (
          <p className="px-2.5 py-3 text-caption text-text-tertiary">Loading…</p>
        ) : open.length === 0 ? (
          <p className="px-2.5 py-3 text-caption text-text-tertiary">
            Nothing is blocked. When a run stops to ask a decision, it waits here until you answer.
          </p>
        ) : (
          <ul className="flex flex-col gap-3">
            {open.map((q) => (
              <QuestionRow key={q.id} question={q} busy={busy} onAnswer={answer} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function QuestionRow({
  question: q,
  busy,
  onAnswer,
}: {
  question: RunQuestion;
  busy: boolean;
  onAnswer: (q: RunQuestion, text: string, approve: boolean) => void | Promise<void>;
}) {
  const wrapper = q.qKind === 'wrapper-renewal';
  const [text, setText] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const waited = waitingSince(q.askedAt, Date.now());

  const canSend = wrapper ? confirmed : text.trim().length > 0;
  const send = () => {
    if (!canSend) return;
    const body = wrapper ? text.trim() || 'Root wrapper scripts renewed with sudo — approved.' : text.trim();
    void onAnswer(q, body, wrapper);
  };

  return (
    <li className={cn('rounded-md border px-3 py-2.5', wrapper ? 'border-danger/40 bg-danger/[0.05]' : 'border-separator bg-fill/[0.04]')}>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className={cn('rounded px-1.5 py-0.5 text-caption', wrapper ? 'bg-danger/[0.12] text-danger' : 'bg-fill/10 text-text-secondary')}>
          {wrapper ? 'Approval required' : 'Question'}
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

      {wrapper && q.detail && (
        <details className="mt-2" open>
          <summary className="cursor-pointer text-caption text-danger">Exact difference to the installed root scripts</summary>
          <pre className="mt-1 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded bg-fill/[0.04] px-2.5 py-1.5 text-caption text-text-secondary">
            {q.detail}
          </pre>
        </details>
      )}

      <div className="mt-2.5 flex flex-col gap-2">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={wrapper ? 2 : 3}
          placeholder={wrapper ? 'Optional note' : 'Your answer — the run continues with it'}
          className="w-full resize-y rounded border border-separator bg-bg-raised px-2.5 py-1.5 text-footnote text-text-primary placeholder:text-text-tertiary"
        />
        {wrapper && (
          <label className="flex items-start gap-2 text-caption text-text-secondary">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(e) => setConfirmed(e.target.checked)}
              className="mt-0.5"
            />
            <span>
              I have renewed the root scripts myself with sudo (the one-line script named above). This approval is
              single-use; the run re-checks the scripts before it installs.
            </span>
          </label>
        )}
        <div className="flex items-center gap-2">
          <Button variant={wrapper ? 'secondary' : 'primary'} size="sm" disabled={busy || !canSend} onClick={send}>
            {wrapper ? 'Approve & continue' : 'Answer & continue'}
          </Button>
        </div>
      </div>
    </li>
  );
}
