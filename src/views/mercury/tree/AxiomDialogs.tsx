// Add/Edit/Move/Conformance surfaces over the ONE axiom access point (S5). Every write goes
// through the DataSource's mercury* methods — the AI steps (optimize, classify, conformance)
// never write behind the user's back: their output pre-fills a form the user reviews and saves.
import { useEffect, useMemo, useState } from 'react';
import { getDataSource } from '@/data';
import { renderMarkdown } from '@/lib/markdown';
import { useToast } from '@/ui/Toast';
import { Button } from '@/ui/Button';
import { Authorship } from '@/ui/Authorship';
import { cn } from '@/lib/cn';
import { basename, dirname } from '@/lib/lang';
import { CheckIcon, PlusIcon, SitemapIcon } from '@/ui/icons';
import type { Axiom, Conformance, MercuryNode, MetaViolation } from '@/types';
import { slugify, type SchemeNs } from './ops';

const msg = (e: unknown) => String((e as Error)?.message ?? e);

/** The record noun per namespace — the only wording that differs across the symmetric forms. */
export const NS_NOUN: Record<SchemeNs, string> = {
  axiome: 'Axiom',
  regeln: 'Rule',
  laeufe: 'Run rule',
  meta: 'Meta-axiom',
};

const ADD_PLACEHOLDER: Record<SchemeNs, string> = {
  axiome: 'The axiom, stated atomically and concisely.',
  regeln: 'The implementation rule, stated concisely.',
  laeufe: 'The run rule every automatic run must follow.',
  meta: 'The binding requirement every axiom must satisfy.',
};

/** Add a record to the given namespace: the user provides title and text; the AI classifies it
 *  into that namespace's tree (a NEW record never asks for a category — that is the point).
 *  Duplicate and meta-conformance findings write nothing: the user decides. */
export function AddAxiomForm({
  section,
  onClose,
  onAdded,
  onResolveDuplicate,
}: {
  section: SchemeNs;
  onClose: () => void;
  onAdded: (path: string) => void;
  /** The user resolved a duplicate: open THIS record in the editor pre-filled with this text. */
  onResolveDuplicate: (path: string, titel: string, body: string) => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [titel, setTitel] = useState('');
  const [body, setBody] = useState('');
  const [busy, setBusy] = useState(false);
  const [dup, setDup] = useState<{ path: string; axiom: Axiom; proposedTitel: string; proposedBody: string } | null>(null);
  const [nonconform, setNonconform] = useState<{ violations: MetaViolation[]; proposedTitel: string; proposedBody: string } | null>(
    null,
  );
  const noun = NS_NOUN[section];

  const submit = async (force = false) => {
    if (!titel.trim() || !body.trim() || busy) return;
    setBusy(true);
    try {
      // Initial AI polish (orthography, generalization, brevity) BEFORE filing.
      const opt = await source.mercuryOptimize(titel.trim(), body.trim(), section);
      const res = await source.mercuryAddAxiom(opt.titel, opt.body, section, force);
      if (res.duplicate) {
        setDup({
          path: res.duplicate.path,
          axiom: res.duplicate.axiom,
          proposedTitel: res.proposed?.titel ?? opt.titel,
          proposedBody: res.proposed?.body ?? opt.body,
        });
        return; // nothing written — the choice is the user's
      }
      if (res.nonconform) {
        setNonconform({
          violations: res.nonconform.violations,
          proposedTitel: res.nonconform.proposed.titel,
          proposedBody: res.nonconform.proposed.body,
        });
        return; // nothing written — a meta-axiom is violated; the user decides
      }
      toast({
        title: res.classified ? `${noun} polished & filed` : `${noun} polished & parked (unsorted)`,
        description: res.path,
        variant: res.classified ? 'success' : 'default',
      });
      if (res.path) onAdded(res.path);
    } catch (e) {
      toast({ title: `Could not add the ${noun.toLowerCase()}`, description: msg(e), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  // The duplicate fork: extend the existing record, adjust it as it stands, or file anyway.
  if (dup) {
    return (
      <div className="flex flex-col gap-2 p-1">
        <p className="text-footnote font-medium text-text-primary">A similar record already exists</p>
        <div className="rounded-md border border-separator bg-surface px-2.5 py-2">
          <p className="text-footnote text-text-primary">{dup.axiom.titel}</p>
          <p className="mt-0.5 font-mono text-caption text-text-tertiary">{dup.path}</p>
          <p className="mt-1 line-clamp-3 text-caption text-text-secondary">{dup.axiom.body}</p>
        </div>
        <p className="text-caption text-text-tertiary">Nothing was saved. What should happen?</p>
        <div className="flex flex-col gap-1.5">
          <Button
            variant="primary"
            size="sm"
            onClick={() => onResolveDuplicate(dup.path, dup.axiom.titel, `${dup.axiom.body}\n\n${dup.proposedBody}`)}
          >
            Extend the existing one
          </Button>
          <Button variant="secondary" size="sm" onClick={() => onResolveDuplicate(dup.path, dup.axiom.titel, dup.axiom.body)}>
            Adjust the existing one
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={() => void submit(true)}>
            {busy ? 'Creating…' : 'Create anyway'}
          </Button>
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => setDup(null)}>
            Back
          </Button>
        </div>
      </div>
    );
  }

  // The conformance fork: take the AI correction, adjust by hand, or write it anyway.
  if (nonconform) {
    return (
      <div className="flex flex-col gap-2 p-1">
        <p className="text-footnote font-medium text-text-primary">Violates meta-axioms</p>
        <ul className="flex flex-col gap-1.5">
          {nonconform.violations.map((v, i) => (
            <li key={i} className="rounded-md border border-warning/30 bg-warning/[0.06] px-2.5 py-1.5">
              <p className="text-caption font-medium text-warning">{v.meta}</p>
              <p className="mt-0.5 text-caption text-text-secondary">{v.issue}</p>
            </li>
          ))}
        </ul>
        <div className="rounded-md border border-separator bg-surface px-2.5 py-2">
          <p className="text-caption uppercase tracking-wide text-text-tertiary">AI correction</p>
          <p className="mt-0.5 text-footnote text-text-primary">{nonconform.proposedTitel}</p>
          <p className="mt-1 line-clamp-4 text-caption text-text-secondary">{nonconform.proposedBody}</p>
        </div>
        <p className="text-caption text-text-tertiary">Nothing was saved. What should happen?</p>
        <div className="flex flex-col gap-1.5">
          <Button
            variant="primary"
            size="sm"
            disabled={busy}
            onClick={() => {
              setTitel(nonconform.proposedTitel);
              setBody(nonconform.proposedBody);
              setNonconform(null);
              void submit();
            }}
          >
            {busy ? 'Creating…' : 'Take the AI correction'}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() => {
              setTitel(nonconform.proposedTitel);
              setBody(nonconform.proposedBody);
              setNonconform(null);
            }}
          >
            Adjust by hand
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={() => void submit(true)}>
            {busy ? 'Creating…' : 'Create anyway'}
          </Button>
          <Button variant="ghost" size="sm" disabled={busy} onClick={() => setNonconform(null)}>
            Back
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2 p-1">
      <input
        autoFocus
        value={titel}
        onChange={(e) => setTitel(e.target.value)}
        placeholder="Title"
        className="rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
      />
      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder={ADD_PLACEHOLDER[section]}
        rows={4}
        className="dl-scroll resize-none rounded-md border border-separator bg-surface px-2.5 py-1.5 text-footnote text-text-primary outline-none focus:border-accent/50"
      />
      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={busy || !titel.trim() || !body.trim()} onClick={() => void submit()}>
          {busy ? 'Polishing & filing…' : 'File it'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={onClose}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

/** The reading pane for a selected record: view, edit, move/rename or delete. Remount it (via
 *  key={path}) on selection change so local state resets cleanly. */
export function AxiomPane({
  path,
  categories,
  namespace,
  initialDraft,
  onChanged,
}: {
  path: string;
  categories: MercuryNode[];
  namespace: SchemeNs;
  /** Pre-fill the editor and open it straight away (duplicate resolution / AI optimization). */
  initialDraft?: { titel: string; body: string };
  onChanged: (nextPath: string | null) => void;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [axiom, setAxiom] = useState<Axiom | null>(null);
  const [err, setErr] = useState(false);
  const [mode, setMode] = useState<'view' | 'edit' | 'move'>(initialDraft ? 'edit' : 'view');
  const [busy, setBusy] = useState(false);
  const [optimizing, setOptimizing] = useState(false);
  // An AI optimization (or a duplicate resolution) is never applied silently: it pre-fills the
  // edit form so the user reviews and saves — cancelling leaves the stored record untouched.
  const [draft, setDraft] = useState<{ titel: string; body: string } | null>(initialDraft ?? null);
  // Conformance against the meta-axioms — checked on demand (an AI call), never automatically.
  const [conf, setConf] = useState<Conformance | null>(null);
  const [checking, setChecking] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setAxiom(null);
    setErr(false);
    source
      .mercuryItem(path)
      .then((a) => !cancelled && setAxiom(a))
      .catch(() => !cancelled && setErr(true));
    return () => {
      cancelled = true;
    };
  }, [source, path]);

  const optimize = async () => {
    if (optimizing || busy || !axiom) return;
    setOptimizing(true);
    try {
      const opt = await source.mercuryOptimize(axiom.titel, axiom.body, namespace);
      setDraft({ titel: opt.titel, body: opt.body });
      setMode('edit');
      toast({ title: 'Polished with AI — review and save', variant: 'success' });
    } catch (e) {
      toast({ title: 'AI polish failed', description: msg(e), variant: 'danger' });
    } finally {
      setOptimizing(false);
    }
  };

  const checkConform = async () => {
    if (checking || busy || !axiom) return;
    setChecking(true);
    try {
      setConf(await source.mercuryConform(axiom.titel, axiom.body));
    } catch (e) {
      toast({ title: 'Conformance check failed', description: msg(e), variant: 'danger' });
    } finally {
      setChecking(false);
    }
  };

  const del = async () => {
    if (busy || !window.confirm(`Delete "${axiom?.titel ?? path}"?`)) return;
    setBusy(true);
    try {
      await source.mercuryDeleteAxiom(path);
      toast({ title: `${NS_NOUN[namespace]} deleted`, variant: 'default' });
      onChanged(null);
    } catch (e) {
      toast({ title: 'Delete failed', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  if (err) return <p className="px-8 py-7 text-footnote text-text-secondary">This record could not be loaded.</p>;
  if (!axiom) return <p className="px-8 py-7 text-footnote text-text-tertiary">Loading…</p>;

  if (mode === 'edit') {
    return (
      <EditAxiomForm
        axiom={axiom}
        section={namespace}
        initial={draft ?? undefined}
        onCancel={() => {
          setDraft(null); // discard an un-saved AI optimization
          setMode('view');
        }}
        onSave={async (titel, body) => {
          const res = await source.mercuryEditAxiom(path, titel, body);
          setDraft(null);
          toast({ title: `${NS_NOUN[namespace]} saved`, variant: 'success' });
          if (res.path !== path) {
            // The title changed the slug, so the record was renamed to keep heading and path
            // matched; follow the new path (reloads the tree and re-selects it).
            onChanged(res.path);
            return;
          }
          // Same path: refresh from the server so the reading view reflects the edit (and its
          // refreshed authorship) without a full reload.
          setAxiom(await source.mercuryItem(path).catch(() => res.axiom));
          setMode('view');
        }}
      />
    );
  }

  if (mode === 'move') {
    return (
      <MoveAxiomForm
        path={path}
        categories={categories}
        namespace={namespace}
        onCancel={() => setMode('view')}
        onMove={async (to) => {
          const res = await source.mercuryMoveAxiom(path, to);
          toast({ title: `${NS_NOUN[namespace]} moved`, description: res.path, variant: 'success' });
          onChanged(res.path);
        }}
      />
    );
  }

  return (
    <article className="mx-auto max-w-3xl px-8 py-7">
      <div className="flex items-start justify-between gap-4">
        <h1 className="text-title3 font-semibold tracking-tight text-text-primary">{axiom.titel || basename(path)}</h1>
        <div className="flex shrink-0 items-center gap-1.5">
          <Button variant="secondary" size="sm" onClick={() => setMode('edit')}>
            Edit
          </Button>
          <Button variant="secondary" size="sm" disabled={optimizing || busy} onClick={() => void optimize()}>
            {optimizing ? 'Polishing…' : 'Polish with AI'}
          </Button>
          {namespace === 'axiome' && (
            <Button variant="secondary" size="sm" disabled={checking || busy} onClick={() => void checkConform()}>
              {checking ? 'Checking…' : 'Check conformance'}
            </Button>
          )}
          <Button variant="secondary" size="sm" onClick={() => setMode('move')}>
            Move
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={() => void del()}>
            Delete
          </Button>
        </div>
      </div>
      <p className="mt-1 font-mono text-caption text-text-tertiary">{path}</p>
      <Authorship className="mt-2" createdBy={axiom.author?.createdBy} updatedBy={axiom.author?.updatedBy} />
      <div className="dl-markdown mt-5" dangerouslySetInnerHTML={{ __html: renderMarkdown(axiom.body) }} />
      {conf && (
        <ConformancePanel
          conf={conf}
          onFix={() => {
            if (conf.proposed) {
              setDraft({ titel: conf.proposed.titel, body: conf.proposed.body });
              setMode('edit');
            }
          }}
        />
      )}
      {axiom.quelle && (
        <p className="mt-8 border-t border-separator pt-3 text-caption text-text-tertiary">
          Source: <span className="font-mono">{axiom.quelle}</span>
        </p>
      )}
    </article>
  );
}

/** The result of a conformance check: conforming (a short confirmation), or the violated
 *  meta-axioms with a one-click AI fix that opens the corrected version in the editor. */
function ConformancePanel({ conf, onFix }: { conf: Conformance; onFix: () => void }) {
  if (conf.metaCount === 0) {
    return (
      <div className="mt-6 rounded-md border border-separator bg-surface px-3 py-2.5 text-caption text-text-tertiary">
        No meta-axioms defined yet — there are no binding requirements to check against.
      </div>
    );
  }
  if (conf.unavailable) {
    return (
      <div className="mt-6 rounded-md border border-separator bg-surface px-3 py-2.5 text-caption text-text-tertiary">
        The conformance check is currently unreachable.
      </div>
    );
  }
  if (conf.conforms) {
    return (
      <div className="mt-6 rounded-md border border-success/30 bg-success/[0.06] px-3 py-2.5">
        <p className="flex items-center gap-1.5 text-caption font-medium text-success">
          <CheckIcon className="h-3.5 w-3.5" /> Satisfies all {conf.metaCount} meta-axioms
        </p>
      </div>
    );
  }
  return (
    <div className="mt-6 rounded-md border border-warning/30 bg-warning/[0.06] px-3 py-2.5">
      <p className="text-caption font-medium text-warning">Violates meta-axioms</p>
      <ul className="mt-1.5 flex flex-col gap-1.5">
        {conf.violations.map((v, i) => (
          <li key={i}>
            <p className="text-caption font-medium text-text-primary">{v.meta}</p>
            <p className="text-caption text-text-secondary">{v.issue}</p>
          </li>
        ))}
      </ul>
      {conf.proposed && (
        <Button variant="primary" size="sm" className="mt-2.5" onClick={onFix}>
          Open the AI correction in the editor
        </Button>
      )}
    </div>
  );
}

/** Edit a record's title and body. Its id, quelle and path are preserved (a title change renames
 *  the record within its category — the backend keeps heading and path matched). */
function EditAxiomForm({
  axiom,
  section,
  initial,
  onCancel,
  onSave,
}: {
  axiom: Axiom;
  section: SchemeNs;
  initial?: { titel: string; body: string };
  onCancel: () => void;
  onSave: (titel: string, body: string) => Promise<void>;
}) {
  const source = useMemo(() => getDataSource(), []);
  const { toast } = useToast();
  const [titel, setTitel] = useState(initial?.titel ?? axiom.titel);
  const [body, setBody] = useState(initial?.body ?? axiom.body);
  const [busy, setBusy] = useState(false);
  const [optimizing, setOptimizing] = useState(false);

  const save = async () => {
    if (!titel.trim() || !body.trim() || busy) return;
    setBusy(true);
    try {
      await onSave(titel.trim(), body.trim());
    } catch (e) {
      toast({ title: 'Save failed', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  const optimize = async () => {
    if (!titel.trim() || !body.trim() || busy || optimizing) return;
    setOptimizing(true);
    try {
      const opt = await source.mercuryOptimize(titel.trim(), body.trim(), section);
      setTitel(opt.titel);
      setBody(opt.body);
      toast({ title: 'Polished with AI — review and save', variant: 'success' });
    } catch (e) {
      toast({ title: 'AI polish failed', description: msg(e), variant: 'danger' });
    } finally {
      setOptimizing(false);
    }
  };

  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-3 px-8 py-7">
      <input
        value={titel}
        onChange={(e) => setTitel(e.target.value)}
        placeholder="Title"
        className="rounded-md border border-separator bg-surface px-3 py-2 text-title3 font-semibold text-text-primary outline-none focus:border-accent/50"
      />
      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        rows={10}
        className="dl-scroll resize-y rounded-md border border-separator bg-surface px-3 py-2 text-body text-text-primary outline-none focus:border-accent/50"
      />
      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={busy || optimizing || !titel.trim() || !body.trim()} onClick={() => void save()}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={busy || optimizing || !titel.trim() || !body.trim()}
          onClick={() => void optimize()}
        >
          {optimizing ? 'Polishing…' : 'Polish with AI'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy || optimizing} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

/** Move or rename a record by BROWSING to a target category and naming the leaf — no path
 *  typing. New categories spring into existence when the record lands under them (the store has
 *  no empty folders). */
function MoveAxiomForm({
  path,
  categories,
  namespace,
  onCancel,
  onMove,
}: {
  path: string;
  categories: MercuryNode[];
  namespace: string;
  onCancel: () => void;
  onMove: (to: string) => Promise<void>;
}) {
  const { toast } = useToast();
  const currentCat = dirname(path);
  const currentSlug = basename(path).replace(/\.md$/, '');
  const [target, setTarget] = useState(currentCat);
  const [name, setName] = useState(currentSlug);
  const [busy, setBusy] = useState(false);

  const slug = slugify(name) || currentSlug;
  const to = `${target}/${slug}.md`;
  const unchanged = to === path;

  const move = async () => {
    if (busy || unchanged) return onCancel();
    setBusy(true);
    try {
      await onMove(to);
    } catch (e) {
      toast({ title: 'Move failed', description: msg(e), variant: 'danger' });
      setBusy(false);
    }
  };

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-4 px-8 py-7">
      <h1 className="text-title3 font-semibold tracking-tight text-text-primary">Move / rename</h1>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Target category</p>
        <div className="dl-scroll max-h-72 overflow-y-auto rounded-card border border-separator bg-surface p-1.5">
          <CategoryPicker nodes={categories} namespace={namespace} selected={target} onSelect={setTarget} />
        </div>
      </div>

      <div>
        <p className="mb-1.5 text-caption font-semibold uppercase tracking-wide text-text-tertiary">Name</p>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && void move()}
          spellCheck={false}
          className="w-full rounded-md border border-separator bg-surface px-3 py-2 text-footnote text-text-primary outline-none focus:border-accent/50"
        />
      </div>

      <p className="font-mono text-caption text-text-tertiary">
        Target: <span className="text-text-secondary">{to}</span>
      </p>

      <div className="flex items-center gap-2">
        <Button variant="primary" size="sm" disabled={busy || unchanged} onClick={() => void move()}>
          {busy ? 'Moving…' : 'Move'}
        </Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

/** A browsable category tree for the move dialog: select an existing category, or create a new
 *  subcategory anywhere. Leaves are not shown — only the folder structure. */
function CategoryPicker({
  nodes,
  namespace,
  selected,
  onSelect,
  depth = 0,
}: {
  nodes: MercuryNode[];
  namespace: string;
  selected: string;
  onSelect: (path: string) => void;
  depth?: number;
}) {
  const cats = nodes.filter((n) => !n.isAxiom);

  return (
    <>
      {depth === 0 && (
        <CategoryRow
          label={namespace}
          path={namespace}
          depth={0}
          selected={selected}
          onSelect={onSelect}
          onCreate={(name) => onSelect(`${namespace}/${slugify(name)}`)}
        />
      )}
      {cats.map((c) => (
        <div key={c.path}>
          <CategoryRow
            label={c.name}
            path={c.path}
            depth={depth + 1}
            selected={selected}
            onSelect={onSelect}
            onCreate={(name) => onSelect(`${c.path}/${slugify(name)}`)}
          />
          {c.children && (
            <CategoryPicker nodes={c.children} namespace={namespace} selected={selected} onSelect={onSelect} depth={depth + 1} />
          )}
        </div>
      ))}
    </>
  );
}

/** One selectable category row in the picker, with a "new subcategory" affordance. */
function CategoryRow({
  label,
  path,
  depth,
  selected,
  onSelect,
  onCreate,
}: {
  label: string;
  path: string;
  depth: number;
  selected: string;
  onSelect: (path: string) => void;
  onCreate: (name: string) => void;
}) {
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const active = selected === path || selected.startsWith(path + '/');
  const exact = selected === path;

  return (
    <div>
      <div className="group flex items-center gap-1" style={{ paddingLeft: `${depth * 14}px` }}>
        <button
          type="button"
          onClick={() => onSelect(path)}
          className={cn(
            'flex min-w-0 flex-1 items-center gap-1.5 rounded-md px-2 py-1 text-left text-footnote transition duration-fast',
            exact
              ? 'bg-accent/15 font-medium text-text-primary'
              : active
                ? 'text-text-primary'
                : 'text-text-secondary hover:bg-fill/10 hover:text-text-primary',
          )}
        >
          <SitemapIcon className={cn('h-3.5 w-3.5 shrink-0', exact ? 'text-accent' : 'text-text-tertiary')} />
          <span className="truncate">{label}</span>
        </button>
        <button
          type="button"
          title="Create subcategory"
          onClick={() => setCreating(true)}
          className="mr-1 shrink-0 rounded px-1 text-text-tertiary opacity-0 transition group-hover:opacity-100 hover:text-accent"
        >
          <PlusIcon className="h-3.5 w-3.5" />
        </button>
      </div>
      {creating && (
        <div className="flex items-center py-0.5" style={{ paddingLeft: `${(depth + 1) * 14 + 8}px` }}>
          <input
            autoFocus
            value={name}
            placeholder="new subcategory"
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && slugify(name)) {
                onCreate(name);
                setCreating(false);
                setName('');
              } else if (e.key === 'Escape') {
                setCreating(false);
                setName('');
              }
            }}
            onBlur={() => {
              if (slugify(name)) onCreate(name);
              setCreating(false);
              setName('');
            }}
            className="min-w-0 flex-1 rounded border border-accent/50 bg-surface px-1.5 py-0.5 text-caption text-text-primary outline-none"
          />
        </div>
      )}
    </div>
  );
}
