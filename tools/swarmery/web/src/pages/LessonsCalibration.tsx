import type { MessageDescriptor } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import {
  type CalibrationDim,
  type CalibrationGroup,
  type CalibrationReport,
  fetchCalibration,
} from '../api/calibration';
import { UI_TERMS } from '../lib/glossary';
import { RoutingReport } from './RoutingReport';

/** "How honest forecasts are" — the dictionary word, sentence-cased. */
const HEADING = `${UI_TERMS.calibration.ui.charAt(0).toUpperCase()}${UI_TERMS.calibration.ui.slice(1)}`;

const DIM_SETS: { dims: CalibrationDim[]; label: MessageDescriptor }[] = [
  { dims: ['model', 'effort'], label: msg`model / effort` },
  { dims: ['agent'], label: msg`agent` },
  { dims: ['project'], label: msg`project` },
  { dims: ['agent', 'model', 'effort', 'project'], label: msg`all four` },
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
      {group.buckets.map((b) => {
        const lo = b.lo.toFixed(1);
        const hi = b.hi.toFixed(1);
        const said = pct(b.meanConfidence);
        const held = pct(b.heldRate);
        return (
          <span key={b.lo} title={plural(b.n, { one: '# run', few: '# runs', many: '# runs', other: '# runs' })}>
            <Trans>
              {lo}–{hi}: said {said}, held {held}
            </Trans>
          </span>
        );
      })}
    </div>
  );
}

const VIEWS: readonly (readonly ['forecast' | 'routing', MessageDescriptor])[] = [
  ['forecast', msg`forecast calibration`],
  ['routing', msg`routing`],
];

/** Calibration and its sibling, the routing report: two answers to "did the
 *  up-front guess match what happened", behind one pair of tabs. Both apply
 *  the same n<20 gate. */
export function CalibrationPanel(): JSX.Element {
  const { i18n, t } = useLingui();
  const [view, setView] = useState<'forecast' | 'routing'>('forecast');
  return (
    <div className="mt-8">
      <div role="tablist" aria-label={t`calibration views`} className="flex gap-1 border-b border-line">
        {VIEWS.map(([id, label]) => (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={view === id}
            onClick={() => setView(id)}
            className={`border-b px-2.5 py-1 font-mono text-[10.5px] tracking-[0.08em] uppercase transition-colors ${
              view === id ? 'border-brand text-brand' : 'border-transparent text-ink-faint hover:text-ink-dim'
            }`}
          >
            {i18n._(label)}
          </button>
        ))}
      </div>
      {view === 'forecast' ? <ForecastCalibration /> : <RoutingReport />}
    </div>
  );
}

/** Calibration — how well forecasts predicted, per agent/model/effort/project
 *  (learning-loop phase 16.4). Groups under the sample gate are never drawn. */
function ForecastCalibration(): JSX.Element {
  const { i18n, t } = useLingui();
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
  const hiddenGroups = String(rep?.hiddenGroups ?? 0);
  const hiddenRuns = String(rep?.hiddenRuns ?? 0);
  const minSamples = String(rep?.minSamples ?? 0);
  return (
    <section className="mt-2 max-w-3xl" aria-labelledby="calibration-heading">
      <h2 id="calibration-heading" className="flex items-baseline gap-2 text-sm font-normal text-ink">
        {HEADING}
        <span className="font-mono text-[10px] text-ink-faint">{UI_TERMS.calibration.code}</span>
      </h2>
      <p className="mt-1 text-[12px] text-ink-dim">
        <Trans>
          How often forecasts held, by group. Area hit is matched areas over every area either side
          named; bands and outcome are exact-match rates; the curve compares declared confidence with
          how often the forecast held. Post-hoc forecasts are excluded.
        </Trans>
      </p>
      <fieldset className="mt-2 inline-flex gap-1" aria-label={t`group calibration by`}>
        {DIM_SETS.map((d, k) => (
          <button
            key={d.dims.join('/')}
            type="button"
            aria-pressed={set === k}
            onClick={() => setSet(k)}
            className={`rounded border px-1.5 py-px font-mono text-[10px] ${
              set === k ? 'border-brand text-brand' : 'border-line text-ink-dim hover:text-ink'
            }`}
          >
            {i18n._(d.label)}
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
              {plural(rep.minSamples, {
                one: 'No group has # scored run yet.',
                few: 'No group has # scored runs yet.',
                many: 'No group has # scored runs yet.',
                other: 'No group has # scored runs yet.',
              })}
            </div>
          ) : (
            <ul className="mt-2 grid gap-2">
              {shown.map((g) => {
                const surprise = g.meanSurprise.toFixed(2);
                const areaHit = pct(g.areaHitRate);
                const bands = pct(g.bandAccuracy);
                const outcome = pct(g.outcomeAccuracy);
                return (
                  <li key={JSON.stringify(g.key)} className="rounded border border-line p-2">
                    <div className="flex items-baseline justify-between gap-3 text-[12px]">
                      <span className="text-ink">
                        {rep.dims.map((d) => g.key[d] ?? t`unknown`).join(' / ')}
                      </span>
                      <span className="font-mono text-[10px] text-ink-dim">
                        {plural(g.samples, {
                          one: `# run · mean surprise ${surprise}`,
                          few: `# runs · mean surprise ${surprise}`,
                          many: `# runs · mean surprise ${surprise}`,
                          other: `# runs · mean surprise ${surprise}`,
                        })}
                      </span>
                    </div>
                    <div className="mt-1 font-mono text-[10px] text-ink-dim">
                      <Trans>
                        area hit {areaHit} · bands {bands} · outcome {outcome}
                      </Trans>
                    </div>
                    <Curve group={g} />
                  </li>
                );
              })}
            </ul>
          )}
          {rep.hiddenGroups > 0 && (
            <div className="mt-2 text-[11px] text-ink-dim">
              <Trans>
                {hiddenGroups} group(s) with {hiddenRuns} run(s) hidden: fewer than {minSamples} samples each.
              </Trans>
            </div>
          )}
        </>
      )}
    </section>
  );
}
