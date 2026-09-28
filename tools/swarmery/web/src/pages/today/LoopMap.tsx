// "The loop, this week" (Canvas v3 phase 4, artboard 1a): five joined stage
// cards, Plan → Run → Measure → Learn → Change. A stage with something waiting
// on the operator turns amber (border, label, number, link). Five columns from
// `desk` up; one column below it.

import { Link } from 'react-router-dom';
import { fmtCount, type Stage } from './loopModel';

/** 1a: live sessions read green even while approvals wait (the amber is in the
 * sentence); any other waiting stage carries its amber into the number. */
function numberTone(s: Stage): string {
  if (s.id === 'run' && s.n > 0) return 'text-green';
  if (s.waiting) return 'text-amber';
  return 'text-ink';
}

function StageCard({ stage }: { stage: Stage }): JSX.Element {
  const accent = stage.waiting ? 'text-amber' : 'text-ink-faint';
  return (
    <div
      data-testid={`stage-${stage.id}`}
      data-waiting={stage.waiting ? 'true' : 'false'}
      className={`relative flex flex-col rounded-[14px] border px-[18px] py-4 desk:rounded-none desk:first:rounded-l-[14px] desk:last:rounded-r-[14px] desk:not-first:border-l-0 ${
        stage.waiting ? 'border-amber/50 bg-amber/5' : 'border-line bg-surface'
      }`}
    >
      <div className={`font-mono text-[10px] tracking-[0.14em] uppercase ${accent}`}>
        {stage.step} · {stage.title}
      </div>
      <div className={`mt-2 font-display text-[26px] leading-[1.15] font-semibold ${numberTone(stage)}`}>
        {fmtCount(stage.n)} <span className="font-sans text-[13px] font-normal text-ink-dim">{stage.unit}</span>
      </div>
      <p className="mt-1.5 text-[12px] leading-normal text-ink-3">
        {stage.sentence}
        {stage.alert !== '' && (
          <>
            {stage.sentence !== '' && ' '}
            <span className="text-amber">{stage.alert}</span>
          </>
        )}
      </p>
      <Link
        to={stage.href}
        className={`mt-2.5 self-start font-mono text-[10.5px] hover:underline ${accent}`}
      >
        {stage.linkLabel}
      </Link>
    </div>
  );
}

export function LoopMap({ stages }: { stages: readonly Stage[] }): JSX.Element {
  return (
    <section aria-label="The loop, this week" data-testid="loop-map">
      <div className="mt-[26px] grid grid-cols-1 gap-2 desk:grid-cols-5 desk:gap-0">
        {stages.map((s) => (
          <StageCard key={s.id} stage={s} />
        ))}
      </div>
      <div className="mt-3 flex items-center gap-2.5 font-mono text-[10.5px] text-ink-faint">
        <span>data flows →</span>
        <span aria-hidden className="h-px flex-1 bg-linear-to-r from-line via-amber via-70% to-line" />
        <span className="hidden desk:inline">and the loop closes when Change lowers next week's Measure</span>
      </div>
    </section>
  );
}
