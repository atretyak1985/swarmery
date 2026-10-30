// Canvas v3 artboard 1d: a phase's state as one card — mono header
// ("Phase 3 · 6/8 · opus" + off-plan band), the title, ONE sentence of state,
// ONE primary action, quiet secondaries, a meta line, and everything else folded.
// Presentational: the handlers (Retry, cancel, …) come from Plans.tsx as props, so
// the phase-run lifecycle stays where it lives today.

import type { MessageDescriptor } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { ReactNode } from 'react';
import type {
  DecisionHold,
  DecisionHoldRef,
  EpicPhase,
  NeedsYouDecision,
  PhaseLanding,
  PhaseRunOutcome,
  ProviderTerms,
} from '../../api/types';
import { phaseGuard } from '../../api/guard';
import { fmtAgo, fmtDateTime, fmtSpan } from '../../lib/format';
import { UI_TERMS } from '../../lib/glossary';
import { offPlanBand, type OffPlanBand } from '../../lib/offPlan';
import { safeHref } from '../../lib/safeHref';
import { modelShortName } from '../../lib/sessionModelChip';
import { forecastStory } from './forecastStoryModel';
import { ForecastVsActual, OFF_PLAN_LABEL } from './ForecastVsActual';
import { GuardBadge } from './GuardBadge';
import { landingChip, type LandingChip as LandingChipModel } from './landingModel';
import { StalledBadge } from './StalledBadge';

// OffPlanChip lives here so the story and the card share one band→tone mapping
// (ForecastStory.tsx imports it from this file).
/** Glyph + tone per band — the one colour mapping the story and the phase card share. */
export const OFF_PLAN_TONE: Record<OffPlanBand, { glyph: string; cls: string }> = {
  'on plan': { glyph: '●', cls: 'border-green/45 bg-green/10 text-green' },
  'off plan': { glyph: '▲', cls: 'border-amber/45 bg-amber/10 text-amber' }, // i18n-ignore — band key (copy: OFF_PLAN_LABEL)
  'far off plan': { glyph: '▲', cls: 'border-red/45 bg-red/10 text-red' }, // i18n-ignore — band key (copy: OFF_PLAN_LABEL)
};

/** The pill "▲ far off plan". `compact` is the phase card's header variant. */
export function OffPlanChip({ band, compact = false }: { band: OffPlanBand; compact?: boolean }): JSX.Element {
  const { i18n } = useLingui();
  const tone = OFF_PLAN_TONE[band];
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1.5 rounded-full border font-mono ${tone.cls} ${
        compact ? 'bg-transparent px-2 py-px text-[10px] tracking-[0.06em]' : 'px-2.5 py-[3px] text-[10.5px] font-semibold'
      }`}
      data-tip={UI_TERMS.surprise.hint}
    >
      <span aria-hidden="true">{tone.glyph}</span>
      {i18n._(OFF_PLAN_LABEL[band])}
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
  /** Where a "waits on D-n" hold links: the planning session turn that asked the
   *  decision. null (or absent) renders the hold without a link. */
  decisionHref?: (ref: DecisionHoldRef) => string | null;
  /** Where the phase's open agent stop is answered (the Needs you page). null
   *  (or absent) renders the stop without a link. */
  stopHref?: string | null;
  /** On the phase page, whose head already carries the name and the state: no
   *  frame, and the chips alone where the card has its header. */
  embedded?: boolean;
}

/** Whether `p` is a path on this origin: it starts with one `/` that a browser
 *  cannot read as the start of `//host` — browsers treat `\` as `/` and drop
 *  tabs and newlines from a URL, so `/\host` and `/<tab>/host` are refused, as
 *  is any backslash, whitespace or control character. */
function isSameOriginPath(p: string): boolean {
  if (!p.startsWith('/') || /^\/[/\\]/.test(p) || /[\\\s]/.test(p)) return false;
  for (let i = 0; i < p.length; i++) {
    const c = p.charCodeAt(i);
    if (c < 0x20 || c === 0x7f) return false;
  }
  return true;
}

/** The decision's own planning-turn link (the daemon's `planningTurn`), else
 *  the caller's; null when neither has one. Only same-origin paths are used. */
function holdHref(d: DecisionHoldRef, hrefFor?: ((ref: DecisionHoldRef) => string | null) | undefined): string | null {
  if (d.planningTurn !== undefined && isSameOriginPath(d.planningTurn)) {
    return d.planningTurn;
  }
  return hrefFor?.(d) ?? null;
}

/** "waits on decision D-3 — <question>": the phase's decision hold, inline. The
 *  run gate refuses this phase until each listed decision is taken or marked
 *  cheap. */
export function DecisionHoldNote({
  hold,
  hrefFor,
}: {
  hold: DecisionHold;
  hrefFor?: ((ref: DecisionHoldRef) => string | null) | undefined;
}): JSX.Element {
  return (
    <div
      data-testid="decision-hold"
      className="mt-3 rounded-[10px] border border-amber/40 bg-amber/5 px-3 py-2.5 text-[12.5px]"
    >
      {hold.decisions.map((d) => {
        const id = d.decisionId;
        const href = holdHref(d, hrefFor);
        return (
          <div key={id} className="flex flex-wrap items-baseline gap-x-1.5">
            <span className="font-medium text-amber">
              <Trans>waits on decision {id}</Trans>
            </span>
            <span className="text-ink-3">— {d.question}</span>
            {href !== null && (
              <a href={href} className="font-mono text-[11px] text-ink-dim transition-colors hover:text-brand">
                <Trans>open the question →</Trans>
              </a>
            )}
          </div>
        );
      })}
    </div>
  );
}

/** "waits on decision D-7 · stop: protected-path — <reason>": the phase's open
 *  agent-declared stop, inline. Its last run stopped on a trigger instead of
 *  working around it; the next run starts once the operator answers. */
export function StopDecisionNote({
  decision,
  href,
}: {
  decision: NeedsYouDecision;
  href?: string | null | undefined;
}): JSX.Element {
  const id = decision.decisionId;
  const trigger = decision.trigger ?? '';
  const reason = decision.reason !== undefined && decision.reason !== '' ? decision.reason : decision.question;
  return (
    <div
      data-testid="stop-decision"
      className="mt-3 flex flex-wrap items-baseline gap-x-1.5 rounded-[10px] border border-red/40 bg-red/5 px-3 py-2.5 text-[12.5px]"
    >
      <span className="font-medium text-red">
        <Trans>waits on decision {id}</Trans>
      </span>
      {trigger !== '' && (
        <span className="rounded border border-red/45 px-1.5 py-px font-mono text-[10px] text-red">
          <Trans>stop: {trigger}</Trans>
        </span>
      )}
      <span className="text-ink-3">— {reason}</span>
      {href != null && (
        <a href={href} className="font-mono text-[11px] text-ink-dim transition-colors hover:text-brand">
          <Trans>answer it →</Trans>
        </a>
      )}
    </div>
  );
}

/** The phase's open decision when it is an agent-declared stop; null otherwise. */
function openStop(phase: EpicPhase): NeedsYouDecision | null {
  const d = phase.guardDecision ?? null;
  if (d === null) return null;
  return d.trigger !== undefined || d.freeText === true ? d : null;
}

const OUTCOME_SENTENCE: Record<PhaseRunOutcome, MessageDescriptor> = {
  idle: msg`Not run yet.`,
  running: msg`An agent is working on this phase now.`,
  completed: msg`The last run finished the work.`,
  partial: msg`The last run ticked some criteria but did not finish the phase.`,
  noop: msg`The last run finished without ticking a single criterion.`,
  failed: msg`The last run failed.`,
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
  'off plan': 'bg-amber', // i18n-ignore — band key
  'far off plan': 'bg-red', // i18n-ignore — band key
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
 *  before the landing flow, a link to an open change request. A merged chip
 *  carries the merge date (`landedAt`) as its tooltip; no other state shows
 *  `landedAt`, which only a merge sets. */
function LandingChip({
  landing,
  terms,
}: {
  landing: Pick<PhaseLanding, 'state' | 'prUrl' | 'prNumber'> & Partial<Pick<PhaseLanding, 'landedAt'>>;
  terms: ProviderTerms;
}): JSX.Element | null {
  const { t } = useLingui();
  const chip = landingChip(landing, terms);
  if (chip === null) return null;
  const cls = `inline-flex shrink-0 items-center rounded-full border bg-transparent px-2 py-px font-mono text-[10px] normal-case tracking-[0.04em] ${LANDING_TONE[chip.state]}`;
  const landedAt = landing.landedAt ?? null;
  if (chip.state === 'merged' && landedAt !== null) {
    const mergedAt = fmtDateTime(landedAt);
    return (
      <span data-testid="phase-landing-chip" className={cls} title={t`merged ${mergedAt}`}>
        {chip.text}
      </span>
    );
  }
  return chip.href !== null ? (
    <a
      data-testid="phase-landing-chip"
      href={safeHref(chip.href) ?? undefined}
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
  decisionHref,
  stopHref = null,
  embedded = false,
}: PhaseCardProps): JSX.Element {
  const { i18n, t } = useLingui();
  const story = forecastStory(phase);
  const stop = openStop(phase);
  const band = phase.surprise == null ? null : offPlanBand(phase.surprise.index);
  const title = state?.title ?? story?.headline ?? i18n._(OUTCOME_SENTENCE[phase.runOutcome]);
  const detail = state?.detail ?? story?.why ?? null;
  const dot = band !== null ? BAND_DOT[band] : OUTCOME_DOT[phase.runOutcome];
  const model = phase.runModel ?? phase.docModel;
  const seq = phase.seq;

  const meta: string[] = [];
  if (phase.runStartedAt !== null && phase.runEndedAt !== null) {
    const span = fmtSpan(phase.runStartedAt, phase.runEndedAt);
    meta.push(t`ran ${span}`);
  }
  const done = String(phase.checkboxesDone);
  const before = String(phase.runCheckboxesBefore);
  meta.push(
    `${plural(phase.checkboxesTotal, {
      one: `${done} of # criterion`,
      few: `${done} of # criteria`,
      many: `${done} of # criteria`,
      other: `${done} of # criteria`,
    })}${phase.runCheckboxesBefore === null ? '' : ` ${t`(${before} before)`}`}`,
  );
  if (phase.docUpdatedAt !== null) {
    const edited = fmtAgo(phase.docUpdatedAt);
    meta.push(t`edited ${edited}`);
  }

  const chips = (
    <>
      <StalledBadge stall={phase.stalled} />
      {terms != null && phase.landing != null && <LandingChip landing={phase.landing} terms={terms} />}
      {band !== null && <OffPlanChip band={band} compact />}
    </>
  );

  return (
    <article
      data-testid="phase-card"
      className={embedded ? undefined : 'rounded-xl border border-line bg-surface px-5 py-[18px]'}
    >
      {embedded ? (
        <div className="flex flex-wrap items-center gap-1.5 empty:hidden">{chips}</div>
      ) : (
        <>
          <div className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.1em] text-ink-faint">
            <span>
              <Trans>Phase {seq}</Trans> · {phase.checkboxesDone}/{phase.checkboxesTotal}
            </span>
            {model !== null && (
              <>
                <span aria-hidden="true">·</span>
                <span>{modelShortName(model)}</span>
              </>
            )}
            <span className="ml-auto flex items-center gap-1.5">{chips}</span>
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
        </>
      )}

      {phase.decisionHold != null && phase.decisionHold.decisions.length > 0 && (
        <DecisionHoldNote hold={phase.decisionHold} hrefFor={decisionHref} />
      )}
      {stop !== null && <StopDecisionNote decision={stop} href={stopHref} />}

      <GuardBadge guard={phaseGuard(phase)} className="mt-2" />

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
        <Fold label={t`forecast vs actual`}>
          <ForecastVsActual phase={phase} />
        </Fold>
        {report !== undefined && <Fold label={t`agent's full report`}>{report}</Fold>}
      </div>
    </article>
  );
}
