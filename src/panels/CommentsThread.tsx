import { useCallback, useEffect, useMemo, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { SendIcon, XIcon } from '@/ui/icons';
import { Person } from '@/ui/Person';
import { renderMarkdown } from '@/lib/markdown';
import { cn } from '@/lib/cn';
import type { Comment } from '@/types';

function when(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

/** A compact comment composer (top-level or reply). */
function Composer({ onSubmit, onCancel, placeholder }: { onSubmit: (body: string) => Promise<void>; onCancel?: () => void; placeholder: string }) {
  const [body, setBody] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    const text = body.trim();
    if (!text || busy) return;
    setBusy(true);
    try {
      await onSubmit(text);
      setBody('');
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mt-1.5">
      <div className="flex items-end gap-1.5 rounded-md bg-fill/10 p-1.5 focus-within:ring-2 focus-within:ring-accent/40">
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              void submit();
            }
          }}
          rows={1}
          placeholder={placeholder}
          className="dl-scroll max-h-28 min-h-[24px] w-full resize-none bg-transparent px-1.5 py-1 text-footnote text-text-primary placeholder:text-text-tertiary focus:outline-none"
        />
        <button
          type="button"
          onClick={() => void submit()}
          disabled={!body.trim() || busy}
          aria-label="Post comment"
          className={cn('flex h-7 w-7 shrink-0 items-center justify-center rounded-md transition', body.trim() ? 'bg-accent text-accent-fg hover:bg-accent-hover' : 'bg-fill/10 text-text-tertiary')}
        >
          <SendIcon className="h-4 w-4" />
        </button>
      </div>
      {onCancel && (
        <button type="button" onClick={onCancel} className="mt-1 text-caption text-text-tertiary hover:text-text-secondary">
          Cancel
        </button>
      )}
    </div>
  );
}

/** Threaded, DevLab-stored discussion for one vision file. */
export function CommentsThread({ repoId, path }: { repoId: string; path: string }) {
  const { user } = useWorkspace();
  const { toast } = useToast();
  const source = useMemo(() => getDataSource(), []);
  const [comments, setComments] = useState<Comment[]>([]);
  const [loading, setLoading] = useState(true);
  const [replyTo, setReplyTo] = useState<string | null>(null);

  const reload = useCallback(() => {
    source
      .listComments(repoId, path)
      .then((cs) => setComments(cs))
      .catch(() => {})
      .finally(() => setLoading(false));
  }, [source, repoId, path]);

  useEffect(() => {
    setLoading(true);
    reload();
  }, [reload]);

  const byParent = useMemo(() => {
    const m: Record<string, Comment[]> = {};
    for (const c of comments) (m[c.parentId || ''] ??= []).push(c);
    return m;
  }, [comments]);

  const post = useCallback(
    async (body: string, parentId: string) => {
      try {
        await source.addComment(repoId, path, body, parentId || undefined);
        setReplyTo(null);
        reload();
      } catch (e) {
        toast({ title: 'Comment failed', description: String((e as Error)?.message ?? e), variant: 'danger' });
      }
    },
    [source, repoId, path, reload, toast],
  );

  const del = useCallback(
    async (id: string) => {
      if (!window.confirm('Delete this comment (and its replies)?')) return;
      try {
        await source.deleteComment(repoId, id);
        reload();
      } catch (e) {
        toast({ title: 'Delete failed', description: String((e as Error)?.message ?? e), variant: 'danger' });
      }
    },
    [source, repoId, reload, toast],
  );

  const renderNode = (c: Comment, depth: number) => {
    const mine = c.author === user.username || user.isAdmin;
    return (
      <div key={c.id} className={cn(depth > 0 && 'ml-4 border-l border-separator pl-3')}>
        <div className="flex gap-2 py-2">
          <span className="mt-0.5">
            <Person avatarOnly username={c.author} displayName={c.authorName} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="text-footnote font-medium text-text-primary">{c.authorName || c.author}</span>
              <span className="text-caption text-text-tertiary">{when(c.createdAt)}</span>
              {mine && (
                <button type="button" onClick={() => void del(c.id)} aria-label="Delete comment" className="ml-auto flex h-5 w-5 items-center justify-center rounded-sm text-text-tertiary opacity-0 transition hover:bg-fill/15 hover:text-danger group-hover:opacity-100">
                  <XIcon className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
            <div className="dl-markdown mt-0.5 text-footnote leading-relaxed text-text-secondary" dangerouslySetInnerHTML={{ __html: renderMarkdown(c.body) }} />
            {replyTo === c.id ? (
              <Composer placeholder="Reply…  (⌘/Ctrl+Enter)" onSubmit={(b) => post(b, c.id)} onCancel={() => setReplyTo(null)} />
            ) : (
              <button type="button" onClick={() => setReplyTo(c.id)} className="mt-0.5 text-caption text-text-tertiary hover:text-accent">
                Reply
              </button>
            )}
          </div>
        </div>
        {(byParent[c.id] ?? []).map((child) => renderNode(child, depth + 1))}
      </div>
    );
  };

  const roots = byParent[''] ?? [];

  return (
    <div className="group border-t border-separator px-4 py-3">
      <h3 className="mb-1 text-caption font-semibold uppercase tracking-wide text-text-tertiary">
        Discussion {comments.length > 0 && <span className="ml-1 rounded-full bg-fill/10 px-1.5 text-[10px]">{comments.length}</span>}
      </h3>
      {loading ? (
        <p className="py-2 text-caption text-text-tertiary">Loading…</p>
      ) : roots.length === 0 ? (
        <p className="py-1 text-caption text-text-tertiary">No comments yet — start the discussion.</p>
      ) : (
        roots.map((c) => renderNode(c, 0))
      )}
      <Composer placeholder="Add a comment…  (⌘/Ctrl+Enter)" onSubmit={(b) => post(b, '')} />
    </div>
  );
}
