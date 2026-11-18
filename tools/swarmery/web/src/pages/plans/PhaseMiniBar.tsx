// The list row's thin bar of a plan's phases: one segment per phase, coloured by the
// same state the plan header's bar shows. Decorative — the row's summary already says
// "N of M phases landed" — so it is hidden from assistive tech and holds no links.

import type { EpicPhase } from '../../api/types';
import { segmentState } from './planModel';
import { SEGMENT_TONE } from './segmentTone';

export function PhaseMiniBar({
  phases,
  resolvedSeqs,
}: {
  phases: EpicPhase[];
  resolvedSeqs: Set<number>;
}): JSX.Element | null {
  if (phases.length === 0) return null;
  return (
    <span aria-hidden="true" data-phase-bar="" className="mt-[7px] flex h-[3px] gap-[3px]">
      {phases.map((phase) => {
        const state = segmentState(phase, resolvedSeqs);
        return (
          <span
            key={phase.seq}
            data-state={state}
            className={`max-w-[56px] flex-1 rounded-full ${SEGMENT_TONE[state].fill}`}
          />
        );
      })}
    </span>
  );
}
