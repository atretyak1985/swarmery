// The colour of a phase on a plan's bar, shared by the plan header's bar and the
// list row's mini bar so the two never disagree.

import type { SegmentState } from './planModel';

export const SEGMENT_TONE: Record<SegmentState, { fill: string; text: string }> = {
  landed: { fill: 'bg-green', text: 'text-green' },
  running: { fill: 'bg-blue', text: 'text-blue' },
  guard: { fill: 'bg-red', text: 'text-red' },
  decision: { fill: 'bg-red', text: 'text-red' },
  failed: { fill: 'bg-red', text: 'text-red' },
  manual: { fill: 'bg-amber', text: 'text-amber' },
  blocked: { fill: 'bg-ink-dim', text: 'text-ink-dim' },
  started: { fill: 'bg-ink-faint', text: 'text-ink-faint' },
  pending: { fill: 'bg-line-strong', text: 'text-line-strong' },
};
