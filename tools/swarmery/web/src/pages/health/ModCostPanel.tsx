// Insights → Cost: "cost source: pricing table | mod" (mods-swarmery-link
// phase 3). The page's charts come from /api/stats/*, which only know the
// pricing-table recomputation (turns.cost_usd); rewriting those aggregates
// would invent a lifetime number the mod does not have — its cost is PER
// PROCESS (docs/mods-format.md §M2). So the toggle is scoped to what has both
// values: the sessions in range that carry a link-mod report, one row each,
// with the two costs side by side and every pair that differs by more than
// 10 % highlighted — the input for calibrating config/pricing.json.
//
// Renders nothing until at least one session in range carries `link`, so a
// fleet without the mod sees the page exactly as before.

import { useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { fetchSessions } from '../../api';
import type { Session } from '../../api/types';
import { Segmented } from '../../components/Segmented';
import { fmtCost, projectLabel } from '../../lib/format';
import { costDiverges, costGap } from '../../lib/sessionLink';
import { useSessionHref } from '../../lib/sessionHref';

export type CostSource = 'mod' | 'pricing';

/** Rows shown at most — the panel is a calibration sample, not a ledger. */
const MAX_ROWS = 20;
/** Sessions fetched to find the ones with a link (server max is 500). */
const FETCH_LIMIT = 200;

/** Sessions with a link-mod report and at least one cost, started in [from, to]
 * (calendar days, compared on the ISO date prefix). */
export function modCostRows(sessions: readonly Session[], from: string, to: string): Session[] {
  return sessions.filter((s) => {
    const day = s.startedAt.slice(0, 10);
    return (
      s.link != null && (s.link.costUsd !== null || s.costUsd != null) && day >= from && day <= to
    );
  });
}

/** The value a row shows for `source`; the mod falls back to the pricing table
 * where the mod never reported a cost. */
export function rowCost(s: Session, source: CostSource): { usd: number | null; fallback: boolean } {
  if (source === 'mod' && s.link?.costUsd != null) return { usd: s.link.costUsd, fallback: false };
  return { usd: s.costUsd ?? null, fallback: source === 'mod' };
}

function Row({ s, source }: { s: Session; source: CostSource }): JSX.Element {
  const { t } = useLingui();
  const sessionHref = useSessionHref();
  const shown = rowCost(s, source);
  const other = source === 'mod' ? (s.costUsd ?? null) : (s.link?.costUsd ?? null);
  const diverges = costDiverges(s.link?.costUsd, s.costUsd);
  const gap = costGap(s.link?.costUsd, s.costUsd);
  const gapPct = gap === null ? '' : `${gap > 0 ? '+' : ''}${String(Math.round(gap * 100))}`;
  const otherLabel = source === 'mod' ? t`pricing table` : t`mod`;
  const otherCost = fmtCost(other);
  return (
    <li
      data-testid="mod-cost-row"
      data-diverges={diverges ? 'true' : 'false'}
      className={`flex items-center gap-3 border-b border-line-soft px-1 py-2 last:border-b-0 ${
        diverges ? 'bg-amber/5' : ''
      }`}
    >
      <Link to={sessionHref(s.id)} className="min-w-0 flex-1 truncate text-[12.5px] text-ink hover:text-brand">
        {s.title ?? s.why ?? s.sessionUuid.slice(0, 8)}
      </Link>
      <span className="hidden shrink-0 truncate font-mono text-[10.5px] text-ink-faint sm:inline">
        {projectLabel(s.projectName, s.projectSlug)}
      </span>
      <span className="shrink-0 font-mono text-[10.5px] text-ink-faint">
        {`${otherLabel} ${otherCost}`}
      </span>
      {diverges && (
        <span
          data-testid="mod-cost-divergence"
          className="shrink-0 rounded-full border border-amber/40 bg-amber/10 px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap text-amber"
        >
          {t`Δ ${gapPct} %`}
          <span className="sr-only"> {t`mod and pricing table differ by more than 10 %`}</span>
        </span>
      )}
      <span
        data-testid="mod-cost-value"
        data-tip={shown.fallback ? t`no mod cost for this session — pricing table shown` : undefined}
        className={`w-[64px] shrink-0 text-right font-mono text-[12px] ${shown.fallback ? 'text-ink-dim' : 'text-ink'}`}
      >
        {fmtCost(shown.usd)}
        {shown.fallback ? '*' : ''}
      </span>
    </li>
  );
}

export function ModCostPanel({
  from,
  to,
  scope,
}: {
  from: string;
  to: string;
  scope: string | null;
}): JSX.Element | null {
  const { t } = useLingui();
  const [sessions, setSessions] = useState<Session[] | null>(null);
  // null = the default: mod where any row has a mod cost, otherwise pricing.
  const [picked, setPicked] = useState<CostSource | null>(null);

  useEffect(() => {
    let live = true;
    fetchSessions(scope !== null ? { project: scope } : {}, { limit: FETCH_LIMIT })
      .then((page) => {
        if (live) setSessions(page.sessions);
      })
      // A failed read hides the panel: it is a calibration aid, never a blocker.
      .catch(() => {
        if (live) setSessions([]);
      });
    return () => {
      live = false;
    };
  }, [scope]);

  const rows = modCostRows(sessions ?? [], from, to);
  if (rows.length === 0) return null;
  const source: CostSource = picked ?? (rows.some((s) => s.link?.costUsd != null) ? 'mod' : 'pricing');
  const shown = rows.slice(0, MAX_ROWS);
  const diverging = shown.filter((s) => costDiverges(s.link?.costUsd, s.costUsd)).length;
  const total = fmtCost(shown.reduce((sum, s) => sum + (rowCost(s, source).usd ?? 0), 0));
  const count = String(shown.length);
  const divergingCount = String(diverging);

  return (
    <section className="mt-6" data-testid="mod-cost-panel">
      <div className="mb-2.5 flex flex-wrap items-center gap-3">
        <h2 className="font-mono text-[11px] font-medium tracking-[0.16em] text-ink-dim uppercase">
          {t`Cost source`}
        </h2>
        <Segmented
          label={t`cost source`}
          options={[
            { v: 'pricing', label: t`pricing table` },
            { v: 'mod', label: t`mod` },
          ]}
          value={source}
          onChange={setPicked}
        />
        <span className="font-mono text-[10.5px] text-ink-faint">
          {t`${count} sessions with the session mod · ${total} · ${divergingCount} differ by more than 10 %`}
        </span>
      </div>
      <p className="mb-2 text-[11.5px] text-ink-dim">
        {t`The mod reports what Claude Code counted for its current process, so a resumed session restarts from 0; the pricing table recomputes every turn. Charts above use the pricing table.`}
      </p>
      <ul className="m-0 list-none rounded-[14px] border border-line px-3.5 py-1.5">
        {shown.map((s) => (
          <Row key={s.id} s={s} source={source} />
        ))}
      </ul>
    </section>
  );
}
