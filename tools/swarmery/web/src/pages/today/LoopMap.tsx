// The loop (handoff §3): five joined cells, Plan → Run → Measure → Learn → Change.
// A whole cell opens the place behind its stage. One with something waiting on the
// operator takes the accent. The cells wrap on their own at 160px, so the row
// survives a narrow pane without a breakpoint.

import { useLingui } from '@lingui/react/macro';
import { Link } from 'react-router-dom';
import { fmtCount, type Stage } from './loopModel';

function StageCell({ stage }: { stage: Stage }): JSX.Element {
  const hot = stage.waiting;
  return (
    <Link
      to={stage.href}
      data-testid={`stage-${stage.id}`}
      data-waiting={hot ? 'true' : 'false'}
      data-tip={stage.linkLabel}
      className={`block min-w-0 px-4 py-3.5 leading-[normal] transition-colors hover:bg-surface2 ${
        hot ? 'bg-brand-bg' : 'bg-surface'
      }`}
    >
      <div
        className={`font-mono text-[10px] tracking-[0.12em] uppercase ${hot ? 'text-brand' : 'text-ink-faint'}`}
      >
        {stage.step} · {stage.title}
      </div>
      <div className="mt-2 flex flex-wrap items-baseline gap-[5px]">
        <span className={`font-mono text-[22px] leading-none font-medium ${hot ? 'text-brand' : 'text-ink'}`}>
          {fmtCount(stage.n)}
        </span>
        <span className="text-[12px] text-ink-dim">{stage.unit}</span>
      </div>
      {stage.sentence !== '' && (
        <div className="mt-1.5 text-[12px] leading-[1.45] text-ink-dim">{stage.sentence}</div>
      )}
      {stage.alert !== '' && <div className="mt-1 text-[12px] text-brand">{stage.alert}</div>}
    </Link>
  );
}

export function LoopMap({ stages }: { stages: readonly Stage[] }): JSX.Element {
  const { t } = useLingui();
  return (
    <section
      aria-label={t`The loop, this week`}
      data-testid="loop-map"
      className="mt-5 grid grid-cols-[repeat(auto-fit,minmax(160px,1fr))] gap-px overflow-hidden rounded-[10px] border border-line bg-line"
    >
      {stages.map((s) => (
        <StageCell key={s.id} stage={s} />
      ))}
    </section>
  );
}
