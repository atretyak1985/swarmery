// Stale facts panel (memory-engineering phase 2): the lint report rendered
// where the operator edits the memory. Presentational — the page owns the
// fetch and hands over the report (or its loading/error state) so a lint
// failure can never take the file list down with it. Each finding is a
// button: clicking it opens the file in the editor beside the list.

import type { MemoryLintFinding, MemoryLintReport } from '../../api/types';
import { ErrorBox, Loading } from '../../components/ui';
import { fmtAgo } from '../../lib/format';

export const STALE_FACTS_EMPTY = 'No stale facts found';

function basename(path: string): string {
  const cut = path.lastIndexOf('/');
  return cut >= 0 ? path.slice(cut + 1) : path;
}

export function StaleFactsPanel({
  report,
  loading = false,
  error = null,
  onOpen,
  onRetry,
}: {
  report: MemoryLintReport | null;
  loading?: boolean;
  error?: string | null;
  /** Called with the finding's absolute file path — the same handle as MemoryFile.path. */
  onOpen: (file: string) => void;
  onRetry?: () => void;
}): JSX.Element {
  const findings: MemoryLintFinding[] = report?.findings ?? [];
  const count = findings.length;

  return (
    <section className="mt-4 rounded-xl border border-line bg-surface" aria-label="Stale facts">
      <div className="flex flex-wrap items-center gap-2 px-3 py-2">
        <span className="font-mono text-[11px] text-ink-dim">Stale facts</span>
        <span
          className={`rounded-[6px] border px-1.5 py-[1px] font-mono text-[10px] ${
            count > 0 ? 'border-amber/40 bg-amber/10 text-amber' : 'border-line text-ink-faint'
          }`}
        >
          {count}
        </span>
        <span className="min-w-0 flex-1 truncate text-[12px] text-ink-faint">
          {report !== null
            ? `${String(report.claims)} PR claims across ${String(report.files)} files`
            : 'memory lines that still call a merged PR open'}
        </span>
      </div>

      <div className="border-t border-line px-3 py-2">
        {error !== null ? (
          <ErrorBox message={error} {...(onRetry !== undefined ? { onRetry } : {})} />
        ) : loading || report === null ? (
          <Loading label="lint…" />
        ) : count === 0 ? (
          <p className="text-[12px] text-ink-faint">{STALE_FACTS_EMPTY}</p>
        ) : (
          <ul className="m-0 flex list-none flex-col gap-0.5 p-0">
            {findings.map((f) => (
              <li key={`${f.file}:${String(f.lineNo)}:${String(f.pr)}`}>
                <button
                  type="button"
                  onClick={() => onOpen(f.file)}
                  title={f.file}
                  className="flex w-full flex-col gap-0.5 rounded-[10px] border border-transparent px-2.5 py-1.5 text-left transition-colors hover:bg-surface2/50"
                >
                  <span className="flex flex-wrap items-baseline gap-x-2 font-mono text-[10.5px]">
                    <span className="text-ink">
                      {basename(f.file)}:{f.lineNo}
                    </span>
                    <span className="text-amber">PR #{f.pr}</span>
                    <span className="text-ink-faint">merged {fmtAgo(f.mergedAt)}</span>
                  </span>
                  <span className="block w-full truncate font-mono text-[11px] text-ink-dim">
                    {f.claim}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
