// Forecast vs actual (learning loop phase 13), moved out of pages/Plans.tsx by
// Canvas v3 phase 7: the surprise chip, the score breakdown, and the phase's
// `## Forecast` columns. Bands come from lib/offPlan.ts only — this file holds no
// threshold of its own. The breakdown keeps the code's vocabulary (surprise
// vector, prior → posterior): it is the folded "show the score breakdown" layer
// under ForecastStory, where the terms stay as labels.

import type { ReactNode } from 'react';
import type { EpicPhase, PhaseForecast } from '../../api/types';
import { Markdown } from '../../lib/markdown';
import { offPlanBand, type OffPlanBand } from '../../lib/offPlan';

/** One labeled section inside a detail body. */
export function RailSection({ label, children }: { label: string; children: ReactNode }): JSX.Element {
  return (
    <section className="mb-4 last:mb-0">
      <div className="mb-1.5 font-mono text-[10px] uppercase tracking-wider text-ink-faint">
        {label}
      </div>
      {children}
    </section>
  );
}

/** A phase's `## Forecast` blocks and their lints. A DTO (or fixture) that predates
 * migrations 0079/0082 omits the fields entirely — read that as "none", the same
 * way `surprise` absent reads as unscored. */
export function forecastsOf(p: EpicPhase): PhaseForecast[] {
  return (p.forecasts as PhaseForecast[] | undefined) ?? [];
}
function forecastLintsOf(p: EpicPhase): EpicPhase['forecastLints'] {
  return (p.forecastLints as EpicPhase['forecastLints'] | undefined) ?? [];
}

/** Human label for a surprise component key: `outcome_miss` → `outcome miss`. */
export function surpriseLabel(top: string): string {
  return top === '' ? 'as forecast' : top.replace(/_/g, ' ');
}

const OFF_PLAN_CLS: Record<OffPlanBand, string> = {
  'far off plan': 'border-red/40 bg-red/10 text-red',
  'off plan': 'border-amber/40 bg-amber/10 text-amber',
  'on plan': 'border-green/40 bg-green/10 text-green',
};

/** Colour by off-plan band: calm on plan, needs-a-look off plan, attention far off. */
export function surpriseCls(index: number): string {
  return OFF_PLAN_CLS[offPlanBand(index)];
}

/** The learning loop's surprise chip (phase 13): how far the current run landed
 * from its forecast, labelled with its off-plan band. ADVISORY — it sits beside
 * the run chips and never changes what they say. A run with no forecast reads
 * "no forecast", never a zero score; a phase that never ran shows nothing.
 *
 * `== null`, not `=== null`: a phase built without the field (older fixtures,
 * a partial DTO) is unscored exactly like an explicit null. */
export function SurpriseChip({ phase, onOpen }: { phase: EpicPhase; onOpen?: () => void }): JSX.Element | null {
  const s = phase.surprise;
  if (s == null) {
    if (phase.runEndedAt === null || forecastsOf(phase).length > 0) return null;
    return (
      <span
        className="rounded border border-line px-1.5 py-px font-mono text-[9.5px] text-ink-faint"
        data-tip="this phase declares no ## Forecast, so its run has nothing to be scored against"
      >
        no forecast
      </span>
    );
  }
  const label = `off-plan ${s.index.toFixed(2)} · ${offPlanBand(s.index)}`;
  const cls = `rounded border px-1.5 py-px font-mono text-[9.5px] ${surpriseCls(s.index)}`;
  if (onOpen === undefined)
    return (
      <span className={cls} data-tip={s.summary}>
        {label}
      </span>
    );
  return (
    <button
      type="button"
      className={`${cls} transition-opacity hover:opacity-80`}
      data-tip={`${s.summary} — click for forecast vs actual`}
      onClick={(e) => {
        e.stopPropagation();
        onOpen();
      }}
    >
      {label}
    </button>
  );
}

/** The Completion Report's "Where reality diverged" paragraph, when the executor
 * wrote one: the heading/label line (with any text after the label) plus the
 * lines up to the next blank line or heading. null when absent. */
export function extractDivergence(report: string | null): string | null {
  if (report === null) return null;
  const lines = report.split('\n');
  const at = lines.findIndex((l) => /where reality diverged/i.test(l));
  if (at < 0) return null;
  const out: string[] = [];
  const first = (lines[at] ?? '')
    .replace(/^#+\s*/, '')
    .replace(/\*{0,2}where reality diverged\*{0,2}\s*[:.—-]?\s*\*{0,2}/i, '')
    .trim();
  if (first !== '') out.push(first);
  for (const line of lines.slice(at + 1)) {
    if (/^#/.test(line)) break;
    if (line.trim() === '') {
      if (out.length > 0) break;
      continue;
    }
    out.push(line);
  }
  return out.length > 0 ? out.join('\n') : null;
}

/** "Forecast vs actual" score breakdown (learning loop phase 13): the scored
 * forecast beside what the current run measurably did — areas diff, size /
 * duration bands, outcome, the component vector — and the executor's own account
 * of where reality diverged. READ-ONLY and advisory. */
export function ForecastVsActual({ phase }: { phase: EpicPhase }): JSX.Element {
  const s = phase.surprise;
  const divergence = extractDivergence(phase.completionReport);
  if (s == null) {
    const why =
      forecastsOf(phase).length === 0
        ? 'no forecast — this phase declares no ## Forecast, so there is nothing to score its run against'
        : phase.runEndedAt === null
          ? 'not run yet — a score appears once a run of this phase has finished and been measured'
          : 'not scored — the run’s actuals are not recorded yet, or nothing about it was measurable';
    return (
      <>
        <div className="font-mono text-[11.5px] text-ink-faint">{why}</div>
        {divergence !== null && (
          <RailSection label="where reality diverged">
            <Markdown text={divergence} />
          </RailSection>
        )}
      </>
    );
  }
  const d = s.detail;
  const row = (label: string, forecast: string, actual: string, miss: boolean): JSX.Element => (
    <div className="flex gap-2">
      <span className="w-[68px] shrink-0 text-ink-faint">{label}</span>
      <span className="text-ink-dim">{forecast === '' ? '—' : forecast}</span>
      <span className="text-ink-faint">→</span>
      <span className={miss ? 'text-amber' : 'text-ink-dim'}>{actual === '' ? '—' : actual}</span>
    </div>
  );
  const areaList = (label: string, items: string[], cls: string): JSX.Element | null =>
    items.length === 0 ? null : (
      <div className="flex gap-2">
        <span className="w-[68px] shrink-0 text-ink-faint">{label}</span>
        <span className={`break-words ${cls}`}>{items.join(', ')}</span>
      </div>
    );
  const components = Object.entries(s.components) as [string, number | null | undefined][];
  return (
    <>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className={`rounded border px-1.5 py-px font-mono text-[10px] ${surpriseCls(s.index)}`}>
          surprise {s.index.toFixed(2)} · {surpriseLabel(s.top)}
        </span>
        <span className="font-mono text-[10px] text-ink-faint">
          scored against the {d.forecastKind}
          {d.forecastPostHoc ? ' (post hoc — excluded from calibration)' : ''} · advisory, never a gate
        </span>
      </div>
      <RailSection label="bands & outcome">
        <div className="space-y-0.5 font-mono text-[10.5px]">
          {row('size', d.forecastSize, d.actualSize, (d.sizeDistance ?? 0) > 0)}
          {row('duration', d.forecastDuration, d.actualDuration, (d.durationDistance ?? 0) > 0)}
          {row('outcome', d.forecastOutcome, d.actualOutcome, (s.components.outcome_miss ?? 0) > 0)}
          {d.confidence !== null && row('confidence', d.confidence.toFixed(2), d.majorMiss ? 'major miss' : 'held', d.majorMiss)}
        </div>
      </RailSection>
      <RailSection label="areas">
        {d.actualAreas === null ? (
          <div className="font-mono text-[10.5px] text-ink-faint">the run’s diff was not measured</div>
        ) : (
          <div className="space-y-0.5 font-mono text-[10.5px]">
            {areaList('unexpected', d.unexpectedAreas, 'text-red')}
            {areaList('missed', d.missedAreas, 'text-amber')}
            {areaList('as forecast', d.matchedAreas, 'text-green')}
            {d.unexpectedAreas.length + d.missedAreas.length + d.matchedAreas.length === 0 && (
              <div className="text-ink-faint">no areas to compare</div>
            )}
          </div>
        )}
      </RailSection>
      <RailSection label="surprise vector">
        <div className="space-y-0.5 font-mono text-[10.5px]">
          {components.map(([name, v]) => (
            <div key={name} className="flex gap-2">
              <span className={`w-[120px] shrink-0 ${name === s.top ? 'text-ink' : 'text-ink-faint'}`}>
                {surpriseLabel(name)}
              </span>
              {/* null is "not measurable", never zero. */}
              <span className="text-ink-dim">{v === null || v === undefined ? 'n/a' : v.toFixed(2)}</span>
              <span className="text-ink-faint">× {(s.weights[name as keyof typeof s.weights] ?? 0).toFixed(2)}</span>
            </div>
          ))}
        </div>
      </RailSection>
      {s.revision !== null && (
        <RailSection label="prior → posterior">
          <div className="space-y-0.5 font-mono text-[10.5px] text-ink-dim">
            <div>revision {s.revision.index.toFixed(2)} — how much reading the code changed the expectation</div>
            {s.revision.areasAdded.length > 0 && <div>areas added: {s.revision.areasAdded.join(', ')}</div>}
            {s.revision.areasDropped.length > 0 && <div>areas dropped: {s.revision.areasDropped.join(', ')}</div>}
            {s.revision.priorOutcome !== s.revision.posteriorOutcome && (
              <div>
                outcome {s.revision.priorOutcome || '—'} → {s.revision.posteriorOutcome || '—'}
              </div>
            )}
          </div>
        </RailSection>
      )}
      <RailSection label="where reality diverged">
        {divergence === null ? (
          <div className="font-mono text-[10.5px] text-ink-faint">
            the Completion Report carries no “Where reality diverged” paragraph
          </div>
        ) : (
          <Markdown text={divergence} />
        )}
      </RailSection>
    </>
  );
}

/** One forecast column — the prior or the posterior, whichever the doc carries.
 *
 * Every value is rendered VERBATIM, including a band the daemon does not
 * recognise: the operator cannot fix a typo the UI has already normalised away.
 * `forecastLints` below the columns is what says a value is wrong. */
export function ForecastColumn({ f }: { f: PhaseForecast }): JSX.Element {
  const row = (label: string, value: string): JSX.Element | null =>
    value === '' ? null : (
      <div className="flex gap-1.5">
        <span className="w-[68px] shrink-0 text-ink-faint">{label}</span>
        <span className="break-words text-ink-dim">{value}</span>
      </div>
    );
  return (
    <div className="min-w-0 flex-1 rounded-md border border-line px-2.5 py-2 font-mono text-[10.5px]">
      <div className="mb-1.5 flex items-center gap-1.5">
        <span className="uppercase tracking-wider text-ink">{f.kind === '' ? '(no kind)' : f.kind}</span>
        {f.postHoc && (
          <span
            data-tip={
              f.postHocReason === 'after-first-edit'
                ? 'written to the doc after the run had already changed another file — not a prediction, so calibration skips it'
                : 'written into a doc that already reported the work done — not a prediction, so calibration skips it'
            }
            className="rounded border border-amber/40 bg-amber/10 px-1.5 py-px text-[9.5px] text-amber"
          >
            post hoc
          </span>
        )}
      </div>
      <div className="space-y-0.5">
        {row('written', f.writtenAt)}
        {row('size', f.sizeBand)}
        {row('duration', f.durationBand)}
        {row('outcome', f.outcome)}
        {/* null, not 0: "the author said nothing" is not "certain it is wrong". */}
        {f.confidence !== null && row('confidence', f.confidence.toFixed(2))}
        {f.areas.length > 0 && row('areas', f.areas.join(', '))}
        {f.files.length > 0 && row('files', f.files.join(', '))}
        {f.risks.length > 0 && row('risks', f.risks.join(' · '))}
      </div>
    </div>
  );
}

/** The phase's `## Forecast` blocks, prior and posterior side by side, plus the
 * lints over them. READ-ONLY, and renders nothing at all when the doc declares
 * no forecast — which is every phase until an author opts in.
 *
 * Deliberately NOT a verdict and deliberately placed away from the completion
 * chips: a forecast is a prediction to be scored later, not something a phase
 * can fail. A lint here says the block is unreadable, never that the work is. */
export function ForecastSection({ phase }: { phase: EpicPhase }): JSX.Element | null {
  const forecasts = forecastsOf(phase);
  const lints = forecastLintsOf(phase);
  if (forecasts.length === 0 && lints.length === 0) return null;
  return (
    <RailSection label="forecast">
      <div className="flex flex-col gap-2 sm:flex-row">
        {forecasts.map((f, i) => (
          <ForecastColumn key={`${f.kind}-${String(i)}`} f={f} />
        ))}
      </div>
      {lints.length > 0 && (
        <div className="mt-2 space-y-0.5">
          {lints.map((l, i) => (
            <div key={`${l.code}-${String(i)}`} className="font-mono text-[10.5px] text-amber">
              {l.kind === '' ? '' : `${l.kind}: `}
              {l.message}
            </div>
          ))}
        </div>
      )}
    </RailSection>
  );
}
