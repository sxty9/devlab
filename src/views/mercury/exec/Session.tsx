// SessionPane — the agent session of one execution, OPEN: one watches it while it happens and one
// can write into it, the way one could if one had started it in a shell oneself.
//
// This is the pane the stage detail used to render as a dead block of text (REQ-036/F11). It is
// the SAME access point, not a second window: it stands where the execution's result stands, and
// it shows a running conversation and a finished one alike — a session that has ended stays
// readable exactly where it was watched.
//
// Three properties it must have, and how they are kept:
//
//   PORTIONED   Only a portion of the record is ever fetched: the pane opens on the newest lines,
//               follows forward from where it stopped, and loads earlier lines when they are asked
//               for. The whole record since the beginning is never pulled.
//   LIVE        New lines arrive over the ONE live stream — a tick on the `session` topic, then a
//               read of what came since. No polling, and no second channel.
//   HONEST      An input appears only where it can actually be used: writing is its own right, and
//               a session that has ended says so instead of offering a dead box.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { getDataSource } from '@/data';
import { cn } from '@/lib/cn';
import { fmtTime } from '@/lib/format';
import { useLiveTopic } from '@/state/live';
import { useSession } from '@/state/session';
import { Button } from '@/ui/Button';
import { Person } from '@/ui/Person';
import type { Intervention, SessionLine } from '@/types';
import { EMPTY_SESSION, errMsg, foldPortion, speakableHere, type HeldSession } from './logic';

/** How many lines one portion holds — the same size for the opening read and for every follow. */
const PORTION = 200;

export interface SessionPaneProps {
  runId: string;
  /** The execution whose session this is — its result document and its journal share one name. */
  resultId: string;
  /** Which repository's conversation is meant; also what a message is addressed to. */
  repo: string;
  className?: string;
}

export function SessionPane({ runId, resultId, repo, className }: SessionPaneProps) {
  const source = useMemo(() => getDataSource(), []);
  const { user } = useSession();
  const [held, setHeld] = useState<HeldSession>(EMPTY_SESSION);
  const [failed, setFailed] = useState<string | null>(null);
  const [loadingOlder, setLoadingOlder] = useState(false);

  // nextRef mirrors the follow anchor for the live handler, which must not be re-subscribed on
  // every new line.
  const nextRef = useRef(0);
  const openedRef = useRef(false);

  const readOpening = useCallback(async () => {
    try {
      const got = await source.mercuryRunSession(runId, resultId, undefined, undefined, PORTION);
      nextRef.current = got.next;
      openedRef.current = true;
      setHeld(foldPortion(EMPTY_SESSION, got, false));
      setFailed(null);
    } catch (e) {
      setFailed(errMsg(e));
    }
  }, [source, runId, resultId]);

  // follow reads only what came SINCE — the whole point of the two anchors.
  const follow = useCallback(async () => {
    if (!openedRef.current) return;
    try {
      const got = await source.mercuryRunSession(runId, resultId, nextRef.current, false, PORTION);
      nextRef.current = got.next;
      setHeld((h) => foldPortion(h, got, false));
    } catch {
      // A single missed tick is harmless: the next one reads from the same anchor and catches up.
    }
  }, [source, runId, resultId]);

  const loadOlder = useCallback(async () => {
    setLoadingOlder(true);
    try {
      const got = await source.mercuryRunSession(runId, resultId, held.from, true, PORTION);
      setHeld((h) => foldPortion(h, got, true));
    } catch (e) {
      setFailed(errMsg(e));
    } finally {
      setLoadingOlder(false);
    }
  }, [source, runId, resultId, held.from]);

  useEffect(() => {
    openedRef.current = false;
    nextRef.current = 0;
    setHeld(EMPTY_SESSION);
    setFailed(null);
    void readOpening();
  }, [readOpening]);

  // The ONE live stream carries the session too. A tick says "it spoke"; WHAT it said is read
  // through the normal path, from where this pane stopped.
  useLiveTopic('session', follow);

  const speak = useCallback(
    async (text: string) => {
      const got = await source.mercuryRunSessionSpeak(runId, resultId, text, repo);
      // The answer is the newest portion: the writer sees its own words land. Folding it onto an
      // empty hold keeps the anchors consistent whatever else arrived in between.
      nextRef.current = got.next;
      setHeld(foldPortion(EMPTY_SESSION, got, false));
    },
    [source, runId, resultId, repo],
  );

  if (failed) {
    return <p className={cn('text-caption text-text-secondary', className)}>This session could not be read: {failed}</p>;
  }

  const openHere = speakableHere(held, repo);
  const mine = held.lines.filter((l) => !l.repo || !repo || l.repo === repo);

  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <SteppedIn interventions={held.interventions} />
      <SessionLines
        lines={mine}
        live={openHere}
        older={held.older}
        loadingOlder={loadingOlder}
        onLoadOlder={() => void loadOlder()}
      />
      {openHere ? (
        user.canSpeakSession ? (
          <SpeakBox onSpeak={speak} />
        ) : (
          <p className="text-caption text-text-tertiary">This session is running. You are watching it; writing into it is a separate right.</p>
        )
      ) : (
        held.lines.length > 0 && <p className="text-caption text-text-tertiary">This session has ended. It stays readable.</p>
      )}
    </div>
  );
}

/** That a PERSON wrote into this run — who, and when they first did. A run that was steered is
 *  not a self-acting run, and it says so here whether it is still working or long finished. */
function SteppedIn({ interventions }: { interventions: Intervention[] }) {
  if (interventions.length === 0) return null;
  const first = interventions[0];
  const who = [...new Set(interventions.map((iv) => iv.by?.user || ''))];
  return (
    <p className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 rounded-md bg-accent/10 px-2.5 py-1.5 text-caption text-text-secondary">
      <span>A person stepped into this run</span>
      {who.map((u) => (
        <Person key={u || 'unknown'} username={u} size="sm" />
      ))}
      <span className="text-text-tertiary">
        · first at {fmtTime(first.at)} · {interventions.length} message{interventions.length === 1 ? '' : 's'}
      </span>
    </p>
  );
}

/** The recorded lines. Follows the newest one while the session runs; once it has ended the viewer
 *  scrolls freely and is never yanked back to the bottom. */
function SessionLines({
  lines,
  live,
  older,
  loadingOlder,
  onLoadOlder,
}: {
  lines: SessionLine[];
  live: boolean;
  older: boolean;
  loadingOlder: boolean;
  onLoadOlder: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const count = lines.length;

  useEffect(() => {
    if (live && ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [live, count]);

  return (
    <div
      ref={ref}
      className="dl-scroll max-h-80 overflow-auto rounded-md border border-separator bg-bg-base p-3 font-mono text-caption text-text-secondary"
    >
      {older && (
        <div className="mb-2 flex justify-center">
          <Button variant="ghost" size="sm" disabled={loadingOlder} onClick={onLoadOlder}>
            {loadingOlder ? 'Loading…' : 'Earlier lines'}
          </Button>
        </div>
      )}
      {count === 0 ? (
        <span className="text-text-tertiary">{live ? 'The agent is working…' : 'Nothing was recorded.'}</span>
      ) : (
        <div className="flex flex-col gap-2">
          {lines.map((l, i) => (
            <Line key={`${l.at}:${i}`} line={l} />
          ))}
        </div>
      )}
    </div>
  );
}

/** One line. A person's words are marked as theirs — the agent's output and an intervention live
 *  in the same record and must never be mistaken for one another. */
function Line({ line }: { line: SessionLine }) {
  const spoken = !!line.from;
  return (
    <div className={cn('whitespace-pre-wrap', spoken && 'rounded-md border-l-2 border-accent bg-accent/10 px-2 py-1')}>
      <span className="mr-2 select-none text-text-tertiary">{fmtTime(line.at)}</span>
      {spoken && (
        <span className="mr-2 inline-flex align-middle">
          <Person username={line.from} size="sm" />
        </span>
      )}
      <span className={cn(spoken && 'text-text-primary')}>{line.text}</span>
    </div>
  );
}

/** The input into a running session. Enter sends, Shift+Enter breaks the line — the way one writes
 *  into a session one started oneself. */
function SpeakBox({ onSpeak }: { onSpeak: (text: string) => Promise<void> }) {
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const [refused, setRefused] = useState<string | null>(null);

  const send = async () => {
    const said = text.trim();
    if (!said || sending) return;
    setSending(true);
    setRefused(null);
    try {
      await onSpeak(said);
      setText('');
    } catch (e) {
      // A message that did NOT arrive says so and stays in the box, so nothing typed is lost.
      setRefused(errMsg(e));
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-end gap-2">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
          rows={2}
          placeholder="Write into the running session…"
          className="dl-scroll min-h-[2.5rem] flex-1 resize-y rounded-md border border-separator bg-bg-base px-2.5 py-1.5 font-mono text-caption text-text-primary placeholder:text-text-tertiary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50"
        />
        <Button variant="primary" size="sm" disabled={sending || !text.trim()} onClick={() => void send()}>
          {sending ? 'Sending…' : 'Send'}
        </Button>
      </div>
      {refused && <p className="text-caption text-danger">{refused}</p>}
    </div>
  );
}
