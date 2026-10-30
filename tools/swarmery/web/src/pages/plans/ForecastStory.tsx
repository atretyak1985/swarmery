// The phase page's Report (design update): forecast vs actual told as a verdict, one
// sentence, then expected → happened → why → what next in cells. The numbers stay
// one click away: the ForecastVsActual breakdown and the prior/posterior columns fold
// underneath, where the code's terms remain as labels. Dollars never appear here:
// a forecast has none (plan D-5). Sentences come from forecastStoryModel.ts; bands
// from lib/offPlan.ts.

import { msg } from '@lingui/core/macro';
import type { MessageDescriptor } from '@lingui/core';
import { Trans, useLingui } from '@lingui/react/macro';
import type { ReactNode } from 'react';
import type { EpicPhase } from '../../api/types';
import { UI_TERMS } from '../../lib/glossary';
import type { OffPlanBand } from '../../lib/offPlan';
import { forecastStory } from './forecastStoryModel';
import { ForecastSection, ForecastVsActual, OFF_PLAN_LABEL } from './ForecastVsActual';

/** A folded layer under the story: one ruled line, its content under it. */
function Fold({ label, children }: { label: string; children: ReactNode }): JSX.Element {
  return (
    <details className="group border-b border-line">
      <summary className="flex cursor-pointer list-none items-center gap-2 py-[9px] text-[12.5px] text-ink-2 transition-colors hover:text-brand [&::-webkit-details-marker]:hidden">
        <span aria-hidden="true" className="font-mono text-[10px] text-ink-faint transition-transform group-open:rotate-90">
          ▸
        </span>
        {label}
      </summary>
      <div className="pb-3 pl-[18px]">{children}</div>
    </details>
  );
}

const BAND_TONE: Record<OffPlanBand, { dot: string; text: string }> = {
  'on plan': { dot: 'bg-green', text: 'text-green' },
  'off plan': { dot: 'bg-amber', text: 'text-amber' }, // i18n-ignore — band key
  'far off plan': { dot: 'bg-red', text: 'text-red' }, // i18n-ignore — band key
};

/** The story's cell labels, resolved with `i18n._()` where they render. */
const ROW_LABELS = {
  expected: msg`expected`,
  happened: msg`happened`,
  why: msg`why`,
  next: msg`what next`,
} as const satisfies Record<string, MessageDescriptor>;

export function ForecastStory({ phase }: { phase: EpicPhase }): JSX.Element {
  const { t, i18n } = useLingui();
  const story = forecastStory(phase);
  const s = phase.surprise;
  // Unscored run: no story to tell, but the breakdown still says WHY it is unscored.
  if (story === null || s == null) return <ForecastVsActual phase={phase} />;
  const rows: { label: string; text: string; accent?: boolean }[] = [
    { label: i18n._(ROW_LABELS.expected), text: story.expected },
    { label: i18n._(ROW_LABELS.happened), text: story.happened },
    ...(story.why === null ? [] : [{ label: i18n._(ROW_LABELS.why), text: story.why, accent: story.band !== 'on plan' }]),
    { label: i18n._(ROW_LABELS.next), text: story.next },
  ];
  const surpriseTerm = UI_TERMS.surprise.ui;
  const index = s.index.toFixed(2);
  const prior = UI_TERMS.prior.ui;
  const posterior = UI_TERMS.posterior.ui;
  const tone = BAND_TONE[story.band];
  return (
    <div data-testid="forecast-story">
      <div className="flex flex-wrap items-center gap-2">
        <span aria-hidden="true" className={`size-2 rounded-full ${tone.dot}`} />
        <span className={`text-[13.5px] leading-[normal] font-semibold ${tone.text}`}>
          {i18n._(OFF_PLAN_LABEL[story.band])}
        </span>
        <span className="font-mono text-[10.5px] leading-[normal] text-ink-faint">
          <Trans>
            {surpriseTerm} {index} · nothing gates on this
          </Trans>
        </span>
      </div>
      <p className="mt-2 max-w-[70ch] text-[14px] leading-[1.55] text-pretty text-ink-2">{story.headline}</p>
      <div className="mt-3.5 grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-2.5">
        {rows.map((r) => (
          <div key={r.label} className="rounded-lg border border-line px-3 py-2.5">
            <div
              className={`font-mono text-[10px] leading-[normal] tracking-[0.1em] uppercase ${
                r.accent === true ? 'text-amber' : 'text-ink-faint'
              }`}
            >
              {r.label}
            </div>
            <div className="mt-1 text-[12.5px] leading-normal text-ink-2">{r.text}</div>
          </div>
        ))}
      </div>
      <div className="mt-3.5 flex flex-col">
        <Fold label={t`show the score breakdown`}>
          <ForecastVsActual phase={phase} />
        </Fold>
        <Fold label={t`${prior} vs ${posterior}`}>
          <ForecastSection phase={phase} />
        </Fold>
      </div>
    </div>
  );
}
