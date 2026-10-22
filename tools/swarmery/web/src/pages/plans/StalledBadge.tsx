// «stalled: <stage> — <reason> <N min>» (verify-not-trust phase 11): the phase's
// active stage — executor, reviewer or verifier — stopped working, as the
// daemon's stall detector derived it (EpicPhase.stalled, from the stall_notices
// ledger its 60 s tick keeps). Inline on the phase card; renders nothing when the
// phase is not stalled. Notification only: the badge offers no action, because
// the detector restarts nothing.

import type { I18n, MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import type { PhaseStall, StallStage } from '../../api/types';

const STAGE: Record<StallStage, MessageDescriptor> = {
  executor: msg`executor`,
  reviewer: msg`reviewer`,
  verifier: msg`verifier`,
};

/** `ended` names what the stage left out: the report, the verdict, the record. */
const ENDED: Record<StallStage, MessageDescriptor> = {
  executor: msg`ended without report`,
  reviewer: msg`ended without verdict`,
  verifier: msg`ended without record`,
};

const REASON: Record<'no-turns' | 'permission-pending', MessageDescriptor> = {
  'no-turns': msg`silent`,
  'permission-pending': msg`permission pending`,
};

/** The stage and reason in the active locale; an unknown value (a newer
 *  daemon) is shown verbatim rather than dropped. */
export function stallParts(i18n: I18n, stall: Pick<PhaseStall, 'stage' | 'reason'>): { stage: string; reason: string } {
  const known = (stall.stage as string) in STAGE;
  const stage = known ? i18n._(STAGE[stall.stage]) : stall.stage;
  let reason: string = stall.reason;
  if (stall.reason === 'ended') {
    if (known) reason = i18n._(ENDED[stall.stage]);
  } else if ((stall.reason as string) in REASON) {
    reason = i18n._(REASON[stall.reason]);
  }
  return { stage, reason };
}

export function StalledBadge({ stall }: { stall: PhaseStall | null | undefined }): JSX.Element | null {
  const { i18n, t } = useLingui();
  if (stall == null) return null;
  const { stage, reason } = stallParts(i18n, stall);
  const minutes = String(stall.sinceMinutes);
  return (
    <span
      data-testid="phase-stalled-badge"
      role="status"
      title={t`The ${stage} of this phase has stopped working. Notification only — nothing was restarted.`}
      className="inline-flex shrink-0 items-center gap-1 rounded-full border border-amber/45 bg-amber/10 px-2 py-px font-mono text-[10px] font-semibold normal-case tracking-[0.04em] text-amber"
    >
      <span aria-hidden="true">◷</span>
      {t`stalled: ${stage} — ${reason} ${minutes} min`}
    </span>
  );
}
