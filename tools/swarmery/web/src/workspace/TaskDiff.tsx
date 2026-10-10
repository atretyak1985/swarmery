// The evidence panel of the review loop (board redesign phase 3, §3.3): what
// the agent actually committed on this card's run branch.
//
// Fetched LAZILY — the request shells out to three git commands in the project
// repo, and the modal opens far more often for editing a prompt than for
// reviewing a result. Nothing loads until the panel is expanded.
//
// This file is the fetch + collapse wrapper only; the rendering (commits, file
// counts, the per-file patch) is components/DiffView.tsx, shared with a plan
// phase's Review tab.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import { getBoardTaskDiff } from '../api';
import type { TaskDiff as TaskDiffData } from '../api/types';
import { DiffView } from '../components/DiffView';

export { splitPatch } from '../components/DiffView';

export function TaskDiff({ taskId }: { taskId: number }): JSX.Element {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  const [diff, setDiff] = useState<TaskDiffData | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback((): void => {
    setLoading(true);
    setError(null);
    getBoardTaskDiff(taskId)
      .then(setDiff)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, [taskId]);

  // Fetch on first expand, and again whenever a DIFFERENT card is opened while
  // the panel is already expanded — otherwise the second card would render the
  // first one's commits.
  useEffect(() => {
    if (open) load();
  }, [open, load]);

  // Collapse and drop the previous card's data on a task swap: a stale diff
  // under a new card's title is worse than no diff at all.
  useEffect(() => {
    setOpen(false);
    setDiff(null);
    setError(null);
  }, [taskId]);

  const commitCount = diff?.commits.length ?? 0;
  const fileCount = diff?.files.length ?? 0;

  return (
    <div className="rounded-lg border border-line bg-surface/40">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left font-mono text-[10.5px] tracking-[0.08em] text-ink-dim uppercase transition-colors hover:text-ink"
      >
        <span aria-hidden="true" className="w-2 shrink-0">
          {open ? '▾' : '▸'}
        </span>
        <Trans>diff</Trans>
        {diff !== null && (
          <span className="ml-auto normal-case tracking-normal text-ink-faint">
            {t`${plural(commitCount, {
              one: '# commit',
              few: '# commits',
              many: '# commits',
              other: '# commits',
            })} · ${plural(fileCount, { one: '# file', few: '# files', many: '# files', other: '# files' })}`}
          </span>
        )}
      </button>

      {open && (
        <div className="border-t border-line px-2.5 py-2">
          {loading && (
            <div className="font-mono text-[10.5px] text-ink-faint">
              <Trans>loading diff…</Trans>
            </div>
          )}

          {error !== null && (
            <div className="rounded-md border border-red/30 bg-red/5 px-2 py-1.5 font-mono text-[10.5px] whitespace-pre-wrap text-red">
              {error}
            </div>
          )}

          {diff !== null && !loading && error === null && <DiffView diff={diff} />}
        </div>
      )}
    </div>
  );
}
