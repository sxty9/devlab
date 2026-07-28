// ExecutionDetail — Welle-0 stub (B9 fills it). ONE detail component shared by the history
// AND the calendar (REQ-012): stages (the server array only), sanitized-Markdown report,
// usage. The props are the frozen seam.
export function ExecutionDetail({ runId, resultId }: { runId: string; resultId: string; hideHeader?: boolean }) {
  return (
    <div className="flex h-full min-h-0 items-center justify-center p-8">
      <p className="max-w-md text-center text-footnote text-text-tertiary">
        Execution detail for {runId}/{resultId} arrives with its building block (B9).
      </p>
    </div>
  );
}
