// Desktop (≥1280px) right rail of the session detail (Redesign): the CALL TREE
// on top (who called what: skills → tools → subagents, recursive — subsumes
// the old agents/skills chips), then FILES CHANGED aggregated per path (one
// row per file, +/− summed across all its edits, sorted by churn). Everything
// is derived client-side from the already-loaded detail — no extra API calls.
// Mobile keeps the SummaryChips strip instead.

import { useMemo } from 'react';
import type { Event, FileChange } from '../../api/types';
import { buildCallTree } from '../../lib/calltree';
import { CallTreeCard } from './CallTree';

interface FileRow {
  path: string;
  additions: number;
  deletions: number;
}

/** One row per file path: +/− summed over all its file_change rows, sorted by total churn desc. */
function aggregateFileChanges(changes: FileChange[]): FileRow[] {
  const byPath = new Map<string, FileRow>();
  for (const change of changes) {
    const row = byPath.get(change.filePath) ?? {
      path: change.filePath,
      additions: 0,
      deletions: 0,
    };
    row.additions += change.additions ?? 0;
    row.deletions += change.deletions ?? 0;
    byPath.set(change.filePath, row);
  }
  return [...byPath.values()].sort(
    (a, b) => b.additions + b.deletions - (a.additions + a.deletions),
  );
}

export function DetailRail({
  events,
  fileChanges,
  onShowDiffs,
}: {
  events: Event[];
  fileChanges: FileChange[];
  onShowDiffs: (path?: string) => void;
}): JSX.Element | null {
  const tree = useMemo(() => buildCallTree(events), [events]);
  const files = useMemo(() => aggregateFileChanges(fileChanges), [fileChanges]);

  if (tree.length === 0 && files.length === 0) return null;

  return (
    <div className="min-w-0">
      <CallTreeCard nodes={tree} />

      {files.length > 0 && (
        <div
          className={`rounded-xl border border-line bg-surface px-4 py-3.5 ${tree.length > 0 ? 'mt-2.5' : ''}`}
        >
          <div className="mb-1 flex items-baseline justify-between">
            <span className="font-mono text-[10.5px] tracking-[0.08em] text-ink-dim uppercase">
              files changed
            </span>
            <span className="font-mono text-[12px] font-bold text-ink">{files.length}</span>
          </div>
          {files.map((file) => (
            <button
              key={file.path}
              type="button"
              onClick={() => onShowDiffs(file.path)}
              className="flex w-full items-center gap-2 border-b border-line-soft py-1.5 text-left font-mono text-[11px] transition-colors last:border-b-0 hover:bg-surface2/50"
            >
              <span className="min-w-0 flex-1 truncate text-left text-ink-3 [direction:rtl]">
                {file.path}
              </span>
              <span className="text-green">+{file.additions}</span>
              <span className="text-red">−{file.deletions}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
