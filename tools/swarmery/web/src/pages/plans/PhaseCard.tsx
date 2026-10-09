// Canvas v3 artboard 1d: a phase's state as one card — mono header
// ("Phase 3 · 6/8 · opus" + off-plan band), the title, ONE sentence of state,
// ONE primary action, quiet secondaries, a meta line, and everything else folded.
// Presentational: the handlers (Retry, cancel, …) come from Plans.tsx as props, so
// the phase-run lifecycle stays where it lives today.

import type { ReactNode } from 'react';
import type { EpicPhase, PhaseLanding, PhaseRunOutcome, ProviderTerms } from '../../api/types';
import { fmtAgo, fmtSpan } from '../../lib/format';
import { UI_TERMS } from '../../lib/glossary';
import { offPlanBand, type OffPlanBand } from '../../lib/offPlan';
import { modelShortName } from '../../lib/sessionModelChip';
import { forecastStory } from './forecastStoryModel';
import { ForecastVsActual } from './ForecastVsActual';
import { landingChip, type LandingChip as LandingChipModel } from './landingModel';

// OffPlanChip lives here so the story and the card share one band→tone mapping
// (ForecastStory.tsx imports it from this file).
/** Glyph + tone per band — the one colour mapping the story and the phase card share. */
export const OFF_PLAN_TONE: Record<OffPlanBand, { glyph: string; cls: string }> = {
  'on plan': { glyph: '●', cls: 'border-green/45 bg-green/10 text-green' },
  'off plan': { glyph: '▲', cls: 'border-amber/45 bg-amber/10 text-amber' },
  'far off plan': { glyph: '▲', cls: 'border-red/45 bg-red/10 text-red' },
};

/** The pill "▲ far off plan". `compact` is the phase card's header variant. */
export function OffPlanChip({ band, compact = false }: { band: OffPlanBand; compact?: boolean }): JSX.Element {
  const tone = OFF_PLAN_TONE[band];
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1.5 rounded-full border font-mono ${tone.cls} ${
        compact ? 'bg-transparent px-2 py-px text-[10px] tracking-[0.06em]' : 'px-2.5 py-[3px] text-[10.5px] font-semibold'
      }`}
      data-tip={UI_TERMS.surprise.hint}
    >
      <span aria-hidden="true">{tone.glyph}</span>
      {band}
    </span>
  );
}

export interface PhaseCardAction {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  tip?: string;
}

export interface PhaseCardProps {
  phase: EpicPhase;
  /** The one-sentence state when the run has no forecast story (or to override it). */
  state?: { title: string; detail?: string | null };
  primary?: PhaseCardAction;
  secondaries?: PhaseCardAction[];
  /** Right-aligned quiet link, e.g. "retry · this session →". */
  aside?: ReactNode;
  /** Body of the folded "agent's full report" — usually the Completion Report. */
  report?: ReactNode;
  /** The project's code-host vocabulary; null (or absent) until it loads, and
   *  the landing chip stays hidden until then — it never guesses a label. */
  terms?: ProviderTerms | null;
}

const OUTCOME_SENTENCE: Record<PhaseRunOutcome, string> = {
  idle: 'Not run yet.',
  running: 'An agent is working on this phase now.',
  completed: 'The last run finished the work.',
  partial: 'The last run ticked some criteria but did not finish the phase.',
  noop: 'The last run finished without ticking a single criterion.',
  failed: 'The last run failed.',
};

const OUTCOME_DOT: Record<PhaseRunOutcome, string> = {
  idle: 'bg-ink-faint',
  running: 'animate-pulse bg-brand',
  completed: 'bg-green',
  partial: 'bg-amber',
  noop: 'bg-amber',
  failed: 'bg-red',
};

const BAND_DOT: Record<OffPlanBand, string> = {
  'on plan': 'bg-green',
  'off plan': 'bg-amber',
  'far off plan': 'bg-red',
};

const SECONDARY_BTN =
  'rounded-lg border border-line-strong px-3 py-1.5 font-mono text-[11.5px] text-ink-3 transition-colors hover:text-ink disabled:opacity-50';

const LANDING_TONE: Record<LandingChipModel['state'], string> = {
  ready: 'border-brand/45 text-brand',
  pushed: 'border-line-strong text-ink-3',
  pr_open: 'border-brand/45 text-brand',
  merged: 'border-green/45 text-green',
  returned: 'border-amber/45 text-amber',
};

/** Where the phase's run branch is on its way to the code host — nothing
 *  before the landing flow, a link to an open change request. */
function LandingChip({
  landing,
  terms,
}: {
  landing: Pick<PhaseLanding, 'state' | 'prUrl' | 'prNumber'>;
  terms: ProviderTerms;
}): JSX.Element | null {
  const chip = landingChip(landing, terms);
  if (chip === null) return null;
  const cls = `inline-flex shrink-0 items-center rounded-full border bg-transparent px-2 py-px font-mono text-[10px] normal-case tracking-[0.04em] ${LANDING_TONE[chip.state]}`;
  return chip.href !== null ? (
    <a
      data-testid="phase-landing-chip"
      href={chip.href}
      target="_blank"
      rel="noreferrer"
      className={`${cls} hover:underline`}
    >
      {chip.text}
    </a>
  ) : (
    <span data-testid="phase-landing-chip" className={cls}>
      {chip.text}
    </span>
  );
}

function Fold({ label, children }: { label: string; children: ReactNode }): JSX.Element {
  return (
    <details className="group open:w-full">
      <summary className="cursor-pointer list-none transition-colors hover:text-ink-3 [&::-webkit-details-marker]:hidden">
        <span className="mr-1 inline-block transition-transform group-open:rotate-90">▸</span>
        {label}
      </summary>
      <div className="mt-2 font-sans text-[12.5px] text-ink-2">{children}</div>
    </details>
  );
}

export function PhaseCard({
  phase,
  state,
  primary,
  secondaries = [],
  aside,
  report,
  terms = null,
}: PhaseCardProps): JSX.Element {
  const story = forecastStory(phase);
  const band = phase.surprise == null ? null : offPlanBand(phase.surprise.index);
  const title = state?.title ?? story?.headline ?? OUTCOME_SENTENCE[phase.runOutcome];
  const detail = state?.detail ?? story?.why ?? null;
  const dot = band !== null ? BAND_DOT[band] : OUTCOME_DOT[phase.runOutcome];
  const model = phase.runModel ?? phase.docModel;

  const meta: string[] = [];
  if (phase.runStartedAt !== null && phase.runEndedAt !== null)
    meta.push(`ran ${fmtSpan(phase.runStartedAt, phase.runEndedAt)}`);
  meta.push(
    `${String(phase.checkboxesDone)} of ${String(phase.checkboxesTotal)} criteria${
      phase.runCheckboxesBefore === null ? '' : ` (${String(phase.runCheckboxesBefore)} before)`
    }`,
  );
  if (phase.docUpdatedAt !== null) meta.push(`edited ${fmtAgo(phase.docUpdatedAt)}`);

  return (
    <article data-testid="phase-card" className="rounded-xl border border-line bg-surface px-5 py-[18px]">
      <div className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-ink-faint">
        <span>
          Phase {phase.seq} · {phase.checkboxesDone}/{phase.checkboxesTotal}
        </span>
        {model !== null && (
          <>
            <span aria-hidden="true">·</span>
            <span>{modelShortName(model)}</span>
          </>
        )}
        <span className="ml-auto flex items-center gap-1.5">
          {terms != null && phase.landing != null && <LandingChip landing={phase.landing} terms={terms} />}
          {band !== null && <OffPlanChip band={band} compact />}
        </span>
      </div>
      <div className="mt-1.5 text-[15px] font-medium leading-[1.35] text-ink">{phase.name}</div>

      <div className="mt-3 flex items-start gap-2.5 rounded-[10px] border border-line bg-bg px-3 py-2.5">
        <span className={`mt-[5px] h-2 w-2 shrink-0 rounded-full ${dot}`} />
        <div className="min-w-0">
          <div className="text-[13px] font-medium text-ink">{title}</div>
          {detail !== null && detail !== '' && (
            <div className="mt-[3px] text-[12px] leading-[1.5] text-ink-3">{detail}</div>
          )}
        </div>
      </div>

      {(primary !== undefined || secondaries.length > 0 || aside !== undefined) && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {primary !== undefined && (
            <button
              type="button"
              onClick={primary.onClick}
              disabled={primary.disabled}
              data-tip={primary.tip}
              className="rounded-lg border border-brand/50 bg-brand/10 px-3.5 py-1.5 font-mono text-[11.5px] font-bold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
            >
              {primary.label}
            </button>
          )}
          {secondaries.map((a) => (
            <button
              key={a.label}
              type="button"
              onClick={a.onClick}
              disabled={a.disabled}
              data-tip={a.tip}
              className={SECONDARY_BTN}
            >
              {a.label}
            </button>
          ))}
          {aside !== undefined && <span className="ml-auto font-mono text-[10.5px] text-ink-faint">{aside}</span>}
        </div>
      )}

      <div className="mt-3.5 flex flex-wrap gap-x-3.5 gap-y-1 border-t border-line-soft pt-2.5 font-mono text-[10.5px] text-ink-faint">
        {meta.map((m) => (
          <span key={m}>{m}</span>
        ))}
        <Fold label="forecast vs actual">
          <ForecastVsActual phase={phase} />
        </Fold>
        {report !== undefined && <Fold label="agent's full report">{report}</Fold>}
      </div>
    </article>
  );
}
