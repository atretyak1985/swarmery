// Worktrees panel (Settings): what the janitor sees and what it decided.
//
// The janitor removes agent worktrees without being asked, so the operator's
// question is never "may I clean up" but "what did it do, and to what". This
// panel answers exactly that and offers no controls — see internal/api/
// worktrees.go for why the endpoint has no write path.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { WorktreeRow, WorktreeSweep, WorktreesResponse } from '../../api/types';
import { fetchWorktrees } from '../../api/worktrees';
import { fmtAgo } from '../../lib/format';
import { Empty, ErrorBox } from '../../components/ui';

/** Verdict tone: only a real failure is loud. "kept" is the janitor working. */
function verdictClass(verdict: string): string {
  switch (verdict) {
    case 'redundant':
      return 'border-line-strong text-ink-dim';
    case 'salvage':
      return 'border-brand/40 text-brand';
    case 'keep-unmerged':
      return 'border-amber/45 text-amber';
    default: // skip
      return 'border-line text-ink-faint';
  }
}

function VerdictChip({ verdict }: { verdict: string }): JSX.Element {
  return (
    <span
      className={`shrink-0 rounded-full border px-2 py-px font-mono text-[10px] whitespace-nowrap ${verdictClass(verdict)}`}
    >
      {verdict}
    </span>
  );
}

function LiveRow({ wt }: { wt: WorktreeRow }): JSX.Element {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-line px-3 py-2 last:border-b-0">
      <span className="font-mono text-[11px] break-all text-ink">{wt.path}</span>
      {wt.isMain && (
        <span className="rounded-full border border-line px-2 py-px font-mono text-[10px] text-ink-faint">
          <Trans>main</Trans>
        </span>
      )}
      {wt.branch !== null && (
        <span className="font-mono text-[10px] text-ink-dim">{wt.branch}</span>
      )}
      <span className="ml-auto flex items-center gap-2">
        {wt.dirtyFiles > 0 && (
          <span className="font-mono text-[10px] text-ink-faint">
            {plural(wt.dirtyFiles, { one: '# dirty', few: '# dirty', many: '# dirty', other: '# dirty' })}
          </span>
        )}
        {wt.lastVerdict !== null && <VerdictChip verdict={wt.lastVerdict} />}
        {wt.lastSweptAt !== null && (
          <span className="font-mono text-[10px] text-ink-faint">{fmtAgo(wt.lastSweptAt)}</span>
        )}
      </span>
    </div>
  );
}

function SweepRow({ s }: { s: WorktreeSweep }): JSX.Element {
  const { t } = useLingui();
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-line px-3 py-2 last:border-b-0">
      <span className="w-[52px] shrink-0 font-mono text-[10px] text-ink-faint">
        {fmtAgo(s.ts)}
      </span>
      <VerdictChip verdict={s.verdict} />
      <span className="font-mono text-[11px] break-all text-ink-dim">{s.path}</span>
      <span className="w-full pl-[52px] font-mono text-[10px] text-ink-faint">
        {s.reason}
        {s.salvageBranch !== null && (
          <>
            {' → '}
            <span className="text-brand">{s.salvageBranch}</span>
          </>
        )}
        {s.removed && t` · removed`}
        {s.error !== null && <span className="text-red"> · {s.error}</span>}
      </span>
    </div>
  );
}

export function WorktreesPanel(): JSX.Element {
  const [data, setData] = useState<WorktreesResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    fetchWorktrees()
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setError(null);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [attempt]);

  if (error !== null) return <ErrorBox message={error} onRetry={() => setAttempt((a) => a + 1)} />;
  if (data === null) {
    return (
      <div className="rounded-xl border border-line bg-surface px-3.5 py-4 font-mono text-[11.5px] text-ink-dim">
        <Trans>loading worktrees…</Trans>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      {!data.enabled && (
        <div className="rounded-xl border border-dashed border-amber/45 px-3.5 py-3 font-mono text-[11px] text-amber">
          <Trans>the janitor is disabled by SWARMERY_WTJANITOR — the history below is historical, not current</Trans>
        </div>
      )}

      <div className="overflow-hidden rounded-xl border border-line bg-surface">
        <div className="border-b border-line px-3 py-2 font-mono text-[10px] tracking-wide text-ink-faint uppercase">
          <Trans>live worktrees</Trans>
        </div>
        {data.live.length === 0 ? (
          <div className="px-3 py-4">
            <Empty>
              <Trans>no worktrees on this machine</Trans>
            </Empty>
          </div>
        ) : (
          data.live.map((wt) => <LiveRow key={wt.path} wt={wt} />)
        )}
      </div>

      <div className="overflow-hidden rounded-xl border border-line bg-surface">
        <div className="border-b border-line px-3 py-2 font-mono text-[10px] tracking-wide text-ink-faint uppercase">
          <Trans>recent decisions</Trans>
        </div>
        {data.sweeps.length === 0 ? (
          <div className="px-3 py-4">
            <Empty>
              <Trans>the janitor has not swept yet</Trans>
            </Empty>
          </div>
        ) : (
          data.sweeps.map((s) => <SweepRow key={`${s.ts}-${s.path}`} s={s} />)
        )}
      </div>
    </div>
  );
}
