import { useEffect, useState } from 'react';
import {
  type CalibrationDim,
  type CalibrationGroup,
  type CalibrationReport,
  fetchCalibration,
} from '../api/calibration';

const DIM_SETS: { dims: CalibrationDim[]; label: string }[] = [
  { dims: ['model', 'effort'], label: 'model / effort' },
  { dims: ['agent'], label: 'agent' },
  { dims: ['project'], label: 'project' },
  { dims: ['agent', 'model', 'effort', 'project'], label: 'all four' },
];

function pct(v: number | null): string {
  return v === null ? '—' : `${String(Math.round(v * 100))}%`;
}

/** Only groups at or above the gate are ever drawn. The API already omits
 *  smaller ones; this re-check keeps the UI honest if it ever did not. */
export function visibleGroups(rep: CalibrationReport): CalibrationGroup[] {
  return rep.groups.filter((g) => g.samples >= rep.minSamples);
}

function Curve({ group }: { group: CalibrationGroup }): JSX.Element {
  return (
    <div className="flex gap-2 font-mono text-[10px] text-ink-dim">
      {group.buckets.map((b) => (
        <span key={b.lo} title={`${String(b.n)} runs`}>
          {b.lo.toFixed(1)}–{b.hi.toFixed(1)}: said {pct(b.meanConfidence)}, held {pct(b.heldRate)}
        </span>
      ))}
    </div>
  );
}

/** Calibration — how well forecasts predicted, per agent/model/effort/project
 *  (learning-loop phase 16.4). Groups under the sample gate are never drawn. */
export function CalibrationPanel(): JSX.Element {
  const [set, setSet] = useState(0);
  const [rep, setRep] = useState<CalibrationReport | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const dims = DIM_SETS[set]?.dims ?? ['model', 'effort'];
    fetchCalibration(dims)
      .then((r) => {
        setRep(r);
        setErr(null);
      })
      .catch((e: unknown) => setErr(String(e)));
  }, [set]);

  const shown = rep === null ? [] : visibleGroups(rep);
  return (
    <section className="mt-8 max-w-3xl" aria-labelledby="calibration-heading">
      <h2 id="calibration-heading" className="text-sm text-ink">
        Forecast calibration
      </h2>
      <p className="mt-1 text-[12px] text-ink-dim">
        How often forecasts held, by group. Area hit is matched areas over every area either side
        named; bands and outcome are exact-match rates; the curve compares declared confidence with
        how often the forecast held. Post-hoc forecasts are excluded.
      </p>
      <fieldset className="mt-2 inline-flex gap-1" aria-label="group calibration by">
        {DIM_SETS.map((d, k) => (
          <button
            key={d.label}
            type="button"
            aria-pressed={set === k}
            onClick={() => setSet(k)}
            className={`rounded border px-1.5 py-px font-mono text-[10px] ${
              set === k ? 'border-brand text-brand' : 'border-line text-ink-dim hover:text-ink'
            }`}
          >
            {d.label}
          </button>
        ))}
      </fieldset>
      {err !== null && (
        <div role="alert" className="mt-2 text-[12px] text-red">
          {err}
        </div>
      )}
      {rep !== null && (
        <>
          {shown.length === 0 ? (
            <div className="mt-2 text-[12px] text-ink-dim">
              No group has {String(rep.minSamples)} scored runs yet.
            </div>
          ) : (
            <ul className="mt-2 grid gap-2">
              {shown.map((g) => (
                <li key={JSON.stringify(g.key)} className="rounded border border-line p-2">
                  <div className="flex items-baseline justify-between gap-3 text-[12px]">
                    <span className="text-ink">
                      {rep.dims.map((d) => g.key[d] ?? 'unknown').join(' / ')}
                    </span>
                    <span className="font-mono text-[10px] text-ink-dim">
                      {String(g.samples)} runs · mean surprise {g.meanSurprise.toFixed(2)}
                    </span>
                  </div>
                  <div className="mt-1 font-mono text-[10px] text-ink-dim">
                    area hit {pct(g.areaHitRate)} · bands {pct(g.bandAccuracy)} · outcome{' '}
                    {pct(g.outcomeAccuracy)}
                  </div>
                  <Curve group={g} />
                </li>
              ))}
            </ul>
          )}
          {rep.hiddenGroups > 0 && (
            <div className="mt-2 text-[11px] text-ink-dim">
              {String(rep.hiddenGroups)} group(s) with {String(rep.hiddenRuns)} run(s) hidden: fewer
              than {String(rep.minSamples)} samples each.
            </div>
          )}
        </>
      )}
    </section>
  );
}
