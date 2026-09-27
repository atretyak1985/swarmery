// Routing report — what the complexity router picked in shadow, against what
// ran and how it went (complexity-routing phase 3). Sits beside forecast
// calibration and follows the same rule: a group with fewer than minSamples
// settled runs is never drawn, only counted.

import { useEffect, useState } from 'react';
import {
  fetchRouteReport,
  type RouteGroup,
  type RouteReport,
  type RouteSection,
  type RouteSurface,
} from '../api/route';

const SURFACES: { value: '' | RouteSurface; label: string }[] = [
  { value: '', label: 'both' },
  { value: 'dispatch', label: 'board cards' },
  { value: 'phaserun', label: 'phase runs' },
];

const WINDOW_DAYS = 30;

function pct(v: number): string {
  return `${String(Math.round(v * 100))}%`;
}

function usd(v: number | null): string {
  return v === null ? '—' : `$${v.toFixed(2)}`;
}

/** The gate, re-checked client-side: the API omits small groups already. */
export function shownGroups(section: RouteSection, minSamples: number): RouteGroup[] {
  return section.groups.filter((g) => g.n >= minSamples);
}

/** True when not one group in any section clears the gate. */
export function allHidden(rep: RouteReport): boolean {
  return [rep.byTier, rep.byModel, rep.divergent].every(
    (s) => shownGroups(s, rep.minSamples).length === 0,
  );
}

/** "43 runs, 2% fail, $0.21 mean" — one cell in words. */
export function cellPhrase(g: RouteGroup): string {
  return `${String(g.n)} runs, ${pct(g.failRate)} fail, ${usd(g.meanCost)} mean`;
}

/** The "would it have been cheaper/safer" line for one divergent cell. */
export function divergentPhrase(g: RouteGroup): string {
  return `tier ${g.tier ?? '?'}, picked ${g.pick ?? '?'}, ran ${g.model ?? '?'}: ${cellPhrase(g)}`;
}

function GroupTable({
  caption,
  keyLabel,
  keyOf,
  groups,
}: {
  caption: string;
  keyLabel: string;
  keyOf: (g: RouteGroup) => string;
  groups: RouteGroup[];
}): JSX.Element {
  return (
    <table className="mt-2 w-full border-collapse font-mono text-[11px]">
      <caption className="text-left text-[12px] text-ink">{caption}</caption>
      <thead>
        <tr className="border-b border-line text-left text-[10px] text-ink-faint">
          <th scope="col" className="py-1 pr-2 font-normal">surface</th>
          <th scope="col" className="py-1 pr-2 font-normal">{keyLabel}</th>
          <th scope="col" className="py-1 pr-2 text-right font-normal">runs</th>
          <th scope="col" className="py-1 pr-2 text-right font-normal">fail</th>
          <th scope="col" className="py-1 pr-2 text-right font-normal">mean cost</th>
          <th scope="col" className="py-1 pr-2 text-right font-normal">p90 cost</th>
          <th scope="col" className="py-1 text-right font-normal">agree</th>
        </tr>
      </thead>
      <tbody>
        {groups.map((g) => (
          <tr key={`${g.surface}/${keyOf(g)}`} className="border-b border-line text-ink-2">
            <td className="py-1 pr-2 text-ink-dim">{g.surface}</td>
            <td className="py-1 pr-2 text-ink">{keyOf(g)}</td>
            <td className="py-1 pr-2 text-right">{String(g.n)}</td>
            <td className="py-1 pr-2 text-right">{pct(g.failRate)}</td>
            <td className="py-1 pr-2 text-right" title={`${String(g.costN)} run(s) with a known cost`}>
              {usd(g.meanCost)}
            </td>
            <td className="py-1 pr-2 text-right">{usd(g.p90Cost)}</td>
            <td className="py-1 text-right">{pct(g.agree)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Hidden({ section, minSamples }: { section: RouteSection; minSamples: number }): JSX.Element | null {
  if (section.hiddenGroups === 0) return null;
  return (
    <div className="mt-1 text-[11px] text-ink-dim">
      {String(section.hiddenGroups)} group(s) with {String(section.hiddenRuns)} run(s) hidden: fewer than{' '}
      {String(minSamples)} each.
    </div>
  );
}

/** The report body for one fetched report. Pure; exported for the test. */
export function RoutingTables({ rep }: { rep: RouteReport }): JSX.Element {
  if (allHidden(rep)) {
    return (
      <div className="mt-2 text-[12px] text-ink-dim">
        Not enough runs yet (n &lt; {String(rep.minSamples)} per group).
        {rep.rows + rep.unsettled > 0 &&
          ` ${String(rep.rows)} settled, ${String(rep.unsettled)} still running in the last ${String(rep.days)} days.`}
      </div>
    );
  }
  const tiers = shownGroups(rep.byTier, rep.minSamples);
  const models = shownGroups(rep.byModel, rep.minSamples);
  const divergent = shownGroups(rep.divergent, rep.minSamples);
  return (
    <>
      <div className="mt-1 font-mono text-[10px] text-ink-faint">
        {String(rep.rows)} settled run(s), {String(rep.unsettled)} still running · last {String(rep.days)} days
      </div>
      {tiers.length > 0 && (
        <GroupTable caption="By tier" keyLabel="tier" keyOf={(g) => g.tier ?? '—'} groups={tiers} />
      )}
      <Hidden section={rep.byTier} minSamples={rep.minSamples} />
      {models.length > 0 && (
        <GroupTable caption="By model that ran" keyLabel="model" keyOf={(g) => g.model ?? '—'} groups={models} />
      )}
      <Hidden section={rep.byModel} minSamples={rep.minSamples} />
      {divergent.length > 0 && (
        <div className="mt-3">
          <h3 className="text-[12px] text-ink">Where the pick differed</h3>
          <ul className="mt-1 grid gap-1 font-mono text-[10.5px] text-ink-2">
            {divergent.map((g) => (
              <li key={`${g.surface}/${g.tier ?? ''}/${g.pick ?? ''}/${g.model ?? ''}`}>
                <span className="text-ink-dim">{g.surface} · </span>
                {divergentPhrase(g)}
                <span className="text-ink-dim">
                  {' — '}
                  {g.pickRan
                    ? `when ${g.pick ?? '?'} ran: ${cellPhrase(g.pickRan)}`
                    : `no ${g.pick ?? '?'} runs in tier ${g.tier ?? '?'} above the gate yet`}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
      <Hidden section={rep.divergent} minSamples={rep.minSamples} />
    </>
  );
}

/** Routing — the shadow router's picks against what ran (phase 3). */
export function RoutingReport(): JSX.Element {
  const [surface, setSurface] = useState<'' | RouteSurface>('');
  const [rep, setRep] = useState<RouteReport | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    fetchRouteReport(surface, WINDOW_DAYS)
      .then((r) => {
        if (!live) return;
        setRep(r);
        setErr(null);
      })
      .catch((e: unknown) => {
        if (live) setErr(String(e));
      });
    return () => {
      live = false;
    };
  }, [surface]);

  return (
    <section className="mt-2 max-w-3xl" aria-labelledby="routing-heading">
      <h2 id="routing-heading" className="text-sm text-ink">
        Complexity routing
      </h2>
      <p className="mt-1 text-[12px] text-ink-dim">
        What the router would have picked for each run, recorded in shadow beside what actually ran. Fail
        counts a failed, blocked or partial run or a failed verification; agree is how often the pick and the
        model that ran were the same; cost is known-cost runs only.
      </p>
      <fieldset className="mt-2 inline-flex gap-1" aria-label="routing surface">
        {SURFACES.map((s) => (
          <button
            key={s.label}
            type="button"
            aria-pressed={surface === s.value}
            onClick={() => setSurface(s.value)}
            className={`rounded border px-1.5 py-px font-mono text-[10px] ${
              surface === s.value ? 'border-brand text-brand' : 'border-line text-ink-dim hover:text-ink'
            }`}
          >
            {s.label}
          </button>
        ))}
      </fieldset>
      {err !== null && (
        <div role="alert" className="mt-2 text-[12px] text-red">
          {err}
        </div>
      )}
      {rep !== null && <RoutingTables rep={rep} />}
    </section>
  );
}
