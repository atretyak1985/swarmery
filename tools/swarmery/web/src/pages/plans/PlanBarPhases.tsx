// The plan bar's own phase bar (D-14): a 140px-wide strip of segments, one per
// phase, in the same state and colour as the full header's bar (segmentTone's
// SEGMENT_TONE) so the condensed and the full view never disagree. No legend —
// there is no room for one in a 7px-tall bar — the tooltip and aria-label carry
// the same "seq · name · state" the full bar's segment does.

import { useLingui } from '@lingui/react/macro';
import { Link, useLocation } from 'react-router-dom';
import type { Epic } from '../../api/types';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import { segmentState, segmentWord } from './planModel';
import { plansHref } from './plansUrl';
import { SEGMENT_TONE } from './segmentTone';

export function PlanBarPhases({
  epic,
  resolvedSeqs,
}: {
  epic: Epic;
  resolvedSeqs: Set<number>;
}): JSX.Element | null {
  const { t } = useLingui();
  const { slug } = useProjectWorkspace();
  const { search } = useLocation();
  const segments = epic.phases.map((phase) => ({ phase, state: segmentState(phase, resolvedSeqs) }));
  if (segments.length === 0) return null;
  return (
    <div className="flex h-1.5 w-[140px] shrink-0 gap-px" role="img" aria-label={t`phase progress`}>
      {segments.map(({ phase, state }) => {
        const seq = String(phase.seq);
        const name = phase.name;
        const word = segmentWord(state);
        return (
          <Link
            key={phase.seq}
            to={plansHref(slug, { plan: epic.externalId, detail: { kind: 'phase', seq: phase.seq, tab: 'story' } }, search)}
            aria-label={t`phase ${seq} · ${name} · ${word}`}
            data-tip={t`${seq} · ${name} · ${word}`}
            data-state={state}
            className={`flex-1 rounded-[1px] ${SEGMENT_TONE[state].fill}`}
          />
        );
      })}
    </div>
  );
}
