// Canvas v3 artboard 1c ("Стало"): forecast vs actual told as a story — band
// chip, one-sentence headline, then expected → happened → why → what follows.
// The numbers stay one click away: the moved ForecastVsActual breakdown and the
// prior/posterior columns fold under two <details>, where the code's terms remain
// as labels. Sentences come from forecastStoryModel.ts; bands from lib/offPlan.ts.

import type { ReactNode } from 'react';
import type { EpicPhase } from '../../api/types';
import { UI_TERMS } from '../../lib/glossary';
import { forecastStory } from './forecastStoryModel';
import { ForecastSection, ForecastVsActual } from './ForecastVsActual';
import { OffPlanChip } from './PhaseCard';

/** A folded layer under the story: pill summary, content full width once open. */
function Fold({ label, children }: { label: string; children: ReactNode }): JSX.Element {
  return (
    <details className="group open:w-full">
      <summary className="inline-flex cursor-pointer list-none rounded-lg border border-line-strong px-3 py-[5px] font-mono text-[11px] text-ink-3 transition-colors hover:text-ink [&::-webkit-details-marker]:hidden">
        <span className="mr-1.5 inline-block transition-transform group-open:rotate-90">▸</span>
        {label}
      </summary>
      <div className="mt-3 rounded-lg border border-line-soft px-3 py-3">{children}</div>
    </details>
  );
}

const ROW_LABEL = 'pt-[3px] font-mono text-[10px] uppercase tracking-[0.1em]';

export function ForecastStory({ phase }: { phase: EpicPhase }): JSX.Element {
  const story = forecastStory(phase);
  const s = phase.surprise;
  // Unscored run: no story to tell, but the breakdown still says WHY it is unscored.
  if (story === null || s == null) return <ForecastVsActual phase={phase} />;
  const rows: { label: string; text: string; accent?: boolean }[] = [
    { label: 'expected', text: story.expected },
    { label: 'happened', text: story.happened },
    ...(story.why === null ? [] : [{ label: 'why', text: story.why }]),
    { label: 'what follows', text: story.next, accent: true },
  ];
  return (
    <div data-testid="forecast-story">
      <div className="flex items-center gap-2.5">
        <OffPlanChip band={story.band} />
        <span className="font-mono text-[10.5px] text-ink-faint">
          {UI_TERMS.surprise.ui} {s.index.toFixed(2)} · nothing gates on this
        </span>
      </div>
      <h3 className="mt-3 text-balance font-display text-[20px] font-medium leading-[1.3] tracking-[-0.01em] text-ink">
        {story.headline}
      </h3>
      <div className="mt-3.5 grid grid-cols-[auto_1fr] gap-x-3.5 gap-y-2.5 text-[12.5px] leading-[1.55]">
        {rows.map((r) => (
          <div key={r.label} className="contents">
            <span className={`${ROW_LABEL} ${r.accent === true ? 'text-amber' : 'text-ink-faint'}`}>{r.label}</span>
            <span className="text-ink-2">{r.text}</span>
          </div>
        ))}
      </div>
      <div className="mt-4 flex flex-wrap items-start gap-2">
        <Fold label="show the score breakdown">
          <ForecastVsActual phase={phase} />
        </Fold>
        <Fold label={`${UI_TERMS.prior.ui} vs ${UI_TERMS.posterior.ui}`}>
          <ForecastSection phase={phase} />
        </Fold>
      </div>
    </div>
  );
}
