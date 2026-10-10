// Detail-rail "Context hogs" section: per-tool attribution of a session's
// context growth, parsed on demand from the transcript (no schema, no ingest).
// Collapsed by default; expanding fetches GET /api/sessions/{id}/context-hogs.
// Token figures are ESTIMATES (~4 bytes/token from tool-result sizes) — the
// caveat is rendered, not implied.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useMemo, useState } from 'react';
import { fetchSessionContextHogs } from '../../api';
import type { ContextHogsReport } from '../../api/types';

const TOP_COLLAPSED = 10;

function fmtTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

export function ContextHogsCard({ sessionId }: { sessionId: number }): JSX.Element {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const [data, setData] = useState<ContextHogsReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggle = useCallback(() => {
    const next = !open;
    setOpen(next);
    if (next && data === null && !loading) {
      setLoading(true);
      setError(null);
      fetchSessionContextHogs(sessionId)
        .then((d) => setData(d))
        .catch((e: unknown) =>
          setError(e instanceof Error ? e.message : t`failed to analyze transcript`),
        )
        .finally(() => setLoading(false));
    }
  }, [open, data, loading, sessionId, t]);

  const rows = useMemo(() => {
    if (data === null) return [];
    return showAll ? data.tools : data.tools.slice(0, TOP_COLLAPSED);
  }, [data, showAll]);

  const maxWrite = useMemo(
    () => (data === null ? 0 : Math.max(1, ...data.turns.map((turn) => turn.cacheWrite))),
    [data],
  );

  const toolCount = data?.tools.length ?? 0;
  const totalEst = data !== null ? fmtTokens(data.totalEst) : '';

  return (
    <div className="rounded-xl border border-edge bg-surface px-4 py-3.5">
      <button
        type="button"
        onClick={toggle}
        className="flex w-full items-baseline justify-between text-left"
      >
        <span className="font-mono text-[10.5px] tracking-[0.08em] text-amber/70 uppercase">
          <Trans>context hogs</Trans>
        </span>
        <span className="font-mono text-[11px] text-ink-dim">{open ? t`hide` : t`show`}</span>
      </button>

      {open && (
        <div className="mt-3">
          {loading && (
            <p className="font-mono text-[11px] text-ink-dim">
              <Trans>analyzing transcript…</Trans>
            </p>
          )}
          {error !== null && <p className="font-mono text-[11px] text-red">{error}</p>}

          {data !== null && (
            <>
              <table className="w-full border-collapse">
                <thead>
                  <tr className="text-left font-mono text-[10px] text-ink-dim uppercase">
                    <th className="pb-1 font-normal">
                      <Trans>tool</Trans>
                    </th>
                    <th className="pb-1 text-right font-normal">
                      <Trans>calls</Trans>
                    </th>
                    <th className="pb-1 text-right font-normal">
                      <Trans>~tokens</Trans>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((tool) => (
                    <tr key={tool.name} className="font-mono text-[11px] text-ink-2">
                      <td className="max-w-[160px] truncate py-0.5 pr-2" data-tip-mono data-tip={tool.name}>
                        {tool.name}
                      </td>
                      <td className="py-0.5 text-right tabular-nums">{tool.calls}</td>
                      <td className="py-0.5 text-right tabular-nums">{fmtTokens(tool.estTokens)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>

              {data.tools.length > TOP_COLLAPSED && (
                <button
                  type="button"
                  onClick={() => setShowAll((v) => !v)}
                  className="mt-1.5 font-mono text-[10.5px] text-ink-dim hover:text-ink-2"
                >
                  {showAll ? t`show top 10` : t`show all ${toolCount}`}
                </button>
              )}

              {data.turns.length > 1 && (
                <div className="mt-3">
                  <div className="mb-1 font-mono text-[10px] text-ink-dim uppercase">
                    <Trans>cache-write per turn</Trans>
                  </div>
                  <div className="flex h-8 items-end gap-px">
                    {data.turns.map((turn) => {
                      const seq = turn.seq;
                      const written = fmtTokens(turn.cacheWrite);
                      return (
                        <div
                          key={seq}
                          data-tip={t`turn ${seq}: ${written}`}
                          className="min-w-[2px] flex-1 rounded-t-[1px] bg-amber/50"
                          style={{ height: `${Math.max(6, (turn.cacheWrite / maxWrite) * 100)}%` }}
                        />
                      );
                    })}
                  </div>
                </div>
              )}

              <p className="mt-2.5 font-mono text-[10px] leading-snug text-ink-dim">
                <Trans>~{totalEst} total, estimated at ~4 bytes/token from tool-result sizes</Trans>
                {data.uninspected > 0 && (
                  <>
                    {' · '}
                    {plural(data.uninspected, {
                      one: '# unattributed result',
                      few: '# unattributed results',
                      many: '# unattributed results',
                      other: '# unattributed results',
                    })}
                  </>
                )}
                {data.malformed > 0 && (
                  <>
                    {' · '}
                    {plural(data.malformed, {
                      one: '# malformed line skipped',
                      few: '# malformed lines skipped',
                      many: '# malformed lines skipped',
                      other: '# malformed lines skipped',
                    })}
                  </>
                )}
              </p>
            </>
          )}
        </div>
      )}
    </div>
  );
}
