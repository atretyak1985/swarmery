// Diffs tab: file_changes grouped by file, unified diff with highlighting
// (lightweight custom renderer — mockup .diff language), +/- counters.

import { useMemo } from 'react';
import type { FileChange, FileChangeType } from '../../api/types';
import { Empty } from '../../components/ui';

const CHANGE_TONES: Record<FileChangeType, string> = {
  create: 'text-green border-green/40',
  edit: 'text-ink-dim border-line',
  delete: 'text-red border-red/40',
  rename: 'text-blue border-blue/40',
};

function DiffBlock({ diff }: { diff: string }): JSX.Element {
  const lines = diff.split('\n');
  return (
    <div className="my-2 overflow-x-auto rounded-lg border border-line bg-bg py-2 font-mono text-[11px] leading-[1.6]">
      {lines.map((line, i) => {
        let tone = 'text-ink/85';
        if (line.startsWith('@@')) tone = 'text-blue';
        else if (line.startsWith('+++') || line.startsWith('---')) tone = 'text-ink-dim';
        else if (line.startsWith('+')) tone = 'bg-green/10 text-green';
        else if (line.startsWith('-')) tone = 'bg-red/10 text-red';
        return (
          // eslint-disable-next-line react/no-array-index-key -- static diff text
          <div key={i} className={`px-3 whitespace-pre ${tone}`}>
            {line === '' ? ' ' : line}
          </div>
        );
      })}
    </div>
  );
}

interface FileGroup {
  filePath: string;
  changes: FileChange[];
  additions: number;
  deletions: number;
}

function groupByFile(changes: FileChange[]): FileGroup[] {
  const groups = new Map<string, FileGroup>();
  for (const change of changes) {
    const existing = groups.get(change.filePath);
    if (existing) {
      existing.changes.push(change);
      existing.additions += change.additions ?? 0;
      existing.deletions += change.deletions ?? 0;
    } else {
      groups.set(change.filePath, {
        filePath: change.filePath,
        changes: [change],
        additions: change.additions ?? 0,
        deletions: change.deletions ?? 0,
      });
    }
  }
  return [...groups.values()];
}

export function Diffs({ changes }: { changes: FileChange[] }): JSX.Element {
  const groups = useMemo(() => groupByFile(changes), [changes]);

  if (groups.length === 0) {
    return <Empty>no file changes in this session</Empty>;
  }
  return (
    <div className="mt-2">
      {groups.map((group) => {
        const first = group.changes[0];
        const outOfScope = group.changes.some((c) => c.outOfScope);
        return (
          <div key={group.filePath} className="border-b border-line py-2 last:border-b-0">
            <div className="flex flex-wrap items-center gap-2 font-mono text-[11.5px]">
              <span className="min-w-0 flex-1 truncate">{group.filePath}</span>
              {first !== undefined && (
                <span
                  className={`rounded-full border px-2 py-px text-[10px] ${CHANGE_TONES[first.changeType]}`}
                >
                  {first.changeType}
                </span>
              )}
              {outOfScope && (
                <span className="rounded-full border border-amber/45 px-2 py-px text-[10px] text-amber">
                  out of scope
                </span>
              )}
              <span className="text-green">+{group.additions}</span>
              <span className="text-red">−{group.deletions}</span>
            </div>
            {group.changes.map((change) =>
              change.diff !== null ? (
                <DiffBlock key={change.id} diff={change.diff} />
              ) : (
                <div key={change.id} className="my-2 font-mono text-[11px] text-ink-dim">
                  no diff captured for this change
                </div>
              ),
            )}
          </div>
        );
      })}
    </div>
  );
}
