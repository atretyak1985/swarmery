// Diff tab (handoff §5): the changed files by risk, deletions first, each with
// +N −M and a 5-block share of added to removed. A row opens the unified diff, so
// nothing the old tab showed is gone; the rail's file links scroll to a row here
// through `data-diff-path`, and opening is theirs to do too.

import { Trans, useLingui } from '@lingui/react/macro';
import { useMemo, useState } from 'react';
import type { FileChange, FileChangeType } from '../../api/types';
import { Empty } from '../../components/ui';

/** The handoff's D → M → A. A rename moves code without removing it: with M. */
const RISK: Record<FileChangeType, number> = { delete: 0, edit: 1, rename: 1, create: 2 };

const KIND: Record<FileChangeType, { letter: string; tone: string }> = {
  delete: { letter: 'D', tone: 'text-red' },
  edit: { letter: 'M', tone: 'text-ink-3' },
  rename: { letter: 'R', tone: 'text-ink-3' },
  create: { letter: 'A', tone: 'text-green' },
};

function DiffBlock({ diff }: { diff: string }): JSX.Element {
  const lines = diff.split('\n');
  return (
    <div className="overflow-x-auto py-2 font-mono text-[11px] leading-[1.6]">
      {lines.map((line, i) => {
        let tone = 'text-ink/80';
        if (line.startsWith('@@')) tone = 'text-blue';
        else if (line.startsWith('+++') || line.startsWith('---')) tone = 'text-ink-dim';
        else if (line.startsWith('+')) tone = 'bg-green/10 text-green';
        else if (line.startsWith('-')) tone = 'bg-red/10 text-red';
        return (
          // eslint-disable-next-line react/no-array-index-key -- static diff text
          <div key={i} className={`px-3.5 whitespace-pre ${tone}`}>
            {line === '' ? ' ' : line}
          </div>
        );
      })}
    </div>
  );
}

export interface FileGroup {
  filePath: string;
  changes: FileChange[];
  additions: number;
  deletions: number;
  changeType: FileChangeType;
}

/** One group per path, sorted by risk; the order is stable inside a risk. */
export function groupByRisk(changes: readonly FileChange[]): FileGroup[] {
  const groups = new Map<string, FileGroup>();
  for (const change of changes) {
    const existing = groups.get(change.filePath);
    if (existing) {
      existing.changes.push(change);
      existing.additions += change.additions ?? 0;
      existing.deletions += change.deletions ?? 0;
      // A file both edited and deleted in one session is a deletion.
      if (RISK[change.changeType] < RISK[existing.changeType]) existing.changeType = change.changeType;
    } else {
      groups.set(change.filePath, {
        filePath: change.filePath,
        changes: [change],
        additions: change.additions ?? 0,
        deletions: change.deletions ?? 0,
        changeType: change.changeType,
      });
    }
  }
  return [...groups.values()].sort((a, b) => RISK[a.changeType] - RISK[b.changeType]);
}

export type DiffBlockTone = 'add' | 'del' | 'none';

/** Five blocks: the green share is the added lines, the rest red when any were
 * removed, and an untouched block when nothing was. */
export function diffBlocks(additions: number, deletions: number): DiffBlockTone[] {
  const total = additions + deletions || 1;
  const green = Math.round((additions / total) * 5);
  return Array.from({ length: 5 }, (_, i) => (i < green ? 'add' : deletions > 0 ? 'del' : 'none'));
}

const BLOCK_TONE: Record<DiffBlockTone, string> = { add: 'bg-green', del: 'bg-red', none: 'bg-line' };

function FileRow({ group, open, onToggle }: { group: FileGroup; open: boolean; onToggle: () => void }): JSX.Element {
  const kind = KIND[group.changeType];
  const outOfScope = group.changes.some((c) => c.outOfScope);
  return (
    <div data-diff-path={group.filePath} className="border-b border-line">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="grid w-full grid-cols-[minmax(0,1fr)_70px_110px] items-center gap-[14px] py-[7px] text-left font-mono text-[11.5px] leading-[normal] transition-colors hover:bg-surface2/50"
      >
        <span className="min-w-0 truncate text-ink-2">
          <span className={kind.tone}>{kind.letter}</span> {group.filePath}
          {outOfScope && (
            <span className="ml-2 text-brand">
              <Trans>out of scope</Trans>
            </span>
          )}
        </span>
        <span className="text-right text-ink-3">
          <span className="text-green">+{group.additions}</span> <span className="text-red">−{group.deletions}</span>
        </span>
        <span className="flex justify-end gap-px" aria-hidden="true">
          {diffBlocks(group.additions, group.deletions).map((tone, i) => (
            // eslint-disable-next-line react/no-array-index-key -- five fixed positions
            <span key={i} className={`inline-block h-2 w-2 rounded-[1px] ${BLOCK_TONE[tone]}`} />
          ))}
        </span>
      </button>
      {open &&
        group.changes.map((change) =>
          change.diff !== null && change.diff !== '' ? (
            <DiffBlock key={change.id} diff={change.diff} />
          ) : (
            <div key={change.id} className="my-2 font-mono text-[11px] text-ink-dim">
              <Trans>no diff captured for this change</Trans>
            </div>
          ),
        )}
    </div>
  );
}

export function Diffs({ changes, openPath }: { changes: FileChange[]; openPath?: string | null }): JSX.Element {
  const { t } = useLingui();
  const groups = useMemo(() => groupByRisk(changes), [changes]);
  const [opened, setOpened] = useState<ReadonlySet<string>>(new Set());
  const toggle = (path: string): void => {
    setOpened((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  };

  if (groups.length === 0) {
    return (
      <Empty>
        <Trans>No files changed yet.</Trans>
      </Empty>
    );
  }
  return (
    <div className="mt-[14px]">
      <div className="font-mono text-[10.5px] leading-[normal] text-ink-faint">{t`sorted by risk · deletions first`}</div>
      <div className="mt-2 flex flex-col">
        {groups.map((group) => (
          <FileRow
            key={group.filePath}
            group={group}
            open={opened.has(group.filePath) || openPath === group.filePath}
            onToggle={() => toggle(group.filePath)}
          />
        ))}
      </div>
    </div>
  );
}
