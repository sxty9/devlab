import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useWorkspace } from '@/state/workspace';
import { getDataSource } from '@/data';
import { useToast } from '@/ui/Toast';
import { PanelHeader } from './PanelHeader';
import { IconButton } from '@/ui/Button';
import { FileIcon, FileTextIcon, PlusIcon, RefreshIcon, XIcon } from '@/ui/icons';
import { cn } from '@/lib/cn';
import { humanSize, toBase64 } from '@/lib/file';
import type { VisionFile } from '@/types';

/** The Vision Catalog: idea deposits (images, PDFs, specs, notes) from the repo's /vision folder,
 *  attachable and discussable by collaborators. */
export function VisionPanel() {
  const { activeRepo, canWrite, openVision } = useWorkspace();
  const { toast } = useToast();
  const source = useMemo(() => getDataSource(), []);
  const [files, setFiles] = useState<VisionFile[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  const reload = useCallback(() => {
    source
      .vision(activeRepo.id)
      .then((fs) => setFiles(fs))
      .catch(() => setFiles([]))
      .finally(() => setLoading(false));
  }, [source, activeRepo.id]);

  useEffect(() => {
    setLoading(true);
    reload();
  }, [reload]);

  const removeFile = async (f: VisionFile) => {
    if (!window.confirm(`Delete ${f.name}? Commit & push to share the removal.`)) return;
    setBusy(true);
    try {
      const updated = await source.deleteVision(activeRepo.id, f.path);
      setFiles(updated);
      toast({ title: 'Deleted', description: `${f.name} — commit & push to share the removal`, variant: 'success' });
    } catch (err) {
      toast({ title: 'Delete failed', description: String((err as Error)?.message ?? err), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  const onPick = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = ''; // allow re-picking the same file
    if (!file) return;
    setBusy(true);
    try {
      const b64 = await toBase64(file);
      const updated = await source.uploadVision(activeRepo.id, `vision/${file.name}`, b64);
      setFiles(updated);
      toast({ title: 'Attached', description: `vision/${file.name} — commit & push to share it`, variant: 'success' });
    } catch (err) {
      toast({ title: 'Upload failed', description: String((err as Error)?.message ?? err), variant: 'danger' });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PanelHeader
        title="Vision"
        actions={
          <>
            <IconButton label="Attach file" title={canWrite ? 'Attach a file to /vision' : 'Read-only repository'} disabled={!canWrite || busy} onClick={() => fileInput.current?.click()}>
              <PlusIcon className="h-4 w-4" />
            </IconButton>
            <IconButton label="Refresh" onClick={() => void reload()}>
              <RefreshIcon className="h-4 w-4" />
            </IconButton>
          </>
        }
      />
      <input ref={fileInput} type="file" className="hidden" onChange={onPick} />

      <p className="dl-no-select px-3 pb-1 pt-0.5 text-caption text-text-tertiary">
        {loading ? 'Loading…' : `${files.length} in ${activeRepo.name}/vision`}
      </p>

      <div className="dl-scroll min-h-0 flex-1 overflow-y-auto px-2 pb-3">
        {!loading && files.length === 0 && (
          <p className="px-2 py-3 text-caption text-text-tertiary">
            No vision files yet. {canWrite ? 'Attach an image, PDF or spec to start the catalog.' : 'Ideas will appear here once collaborators add them.'}
          </p>
        )}
        {files.map((f) => (
          <div key={f.path} className="group mb-1.5 flex items-center gap-2.5 rounded-md p-2 transition-colors hover:bg-fill/10">
            <button type="button" onClick={() => openVision(f.path)} className="flex min-w-0 flex-1 items-center gap-2.5 text-left">
              <span className="flex h-9 w-9 shrink-0 items-center justify-center overflow-hidden rounded-md bg-fill/10 ring-1 ring-separator">
                {f.kind === 'image' ? (
                  <img src={source.rawUrl(activeRepo.id, f.path)} alt="" className="h-full w-full object-cover" />
                ) : f.kind === 'markdown' || f.kind === 'text' ? (
                  <FileTextIcon className="h-4 w-4 text-text-tertiary" />
                ) : (
                  <FileIcon className="h-4 w-4 text-text-tertiary" />
                )}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-footnote text-text-primary">{f.name}</span>
                <span className="block truncate text-caption text-text-tertiary">
                  {f.kind} · {humanSize(f.size)}
                  {f.status && <span className={cn('ml-1', f.status === 'untracked' || f.status === 'added' ? 'text-success' : 'text-warning')}>· {f.status}</span>}
                </span>
              </span>
            </button>
            {canWrite && (
              <button
                type="button"
                onClick={() => void removeFile(f)}
                disabled={busy}
                aria-label={`Delete ${f.name}`}
                title="Delete vision file"
                className="flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-text-tertiary opacity-0 transition hover:bg-fill/15 hover:text-danger focus-visible:opacity-100 group-hover:opacity-100 disabled:opacity-30"
              >
                <XIcon className="h-3.5 w-3.5" />
              </button>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
