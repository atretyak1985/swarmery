// The phase verification verdict chip, shared by the phase panel's Runs tab and
// its Review tab (moved out of Plans.tsx unchanged; it now takes the verdict
// and its detail rather than the whole phase, because the Review tab reads them
// from the review response).

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import type { PhaseVerifyVerdict } from '../../api/types';

/** Verdict chip copy + styling. `amber` is the app's semantic needs-a-human color,
 * which is exactly what an inconclusive grade is (the same token workspace/TaskCard
 * uses for a card's inconclusive verdict — one meaning, one color). */
const VERDICT_CHIP: Record<PhaseVerifyVerdict, { cls: string; label: MessageDescriptor; title: MessageDescriptor }> = {
  pass: {
    cls: 'border-green/40 bg-green/10 text-green',
    label: msg`verified`,
    title: msg`a read-only verifier confirmed this phase’s acceptance criteria`,
  },
  fail: {
    cls: 'border-red/40 bg-red/10 text-red',
    label: msg`verify failed`,
    title: msg`a read-only verifier could NOT confirm the ticked criteria — open the run diagnosis`,
  },
  inconclusive: {
    cls: 'border-amber/40 bg-amber/10 text-amber',
    label: msg`verify inconclusive`,
    title: msg`the verifier could not conclude (env or timeout) — this is not a failing grade`,
  },
};

/** The verification verdict, BESIDE the outcome chip and never instead of it: the
 * outcome answers "did work land?" (checkboxes — the single progress truth, decision
 * D5) and the verdict answers "was it confirmed?". Renders nothing when the phase was
 * never graded, which is the default — verification is opt-in per phase doc. */
export function VerifyVerdictChip({
  verdict,
  detail,
}: {
  verdict: PhaseVerifyVerdict | null;
  detail: string | null;
}): JSX.Element | null {
  const { i18n } = useLingui();
  if (verdict === null) return null;
  const { cls, label, title } = VERDICT_CHIP[verdict];
  return (
    <span
      className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${cls}`}
      // The verifier's own reasons when it has any — the whole point of surfacing the
      // verdict is carrying WHY, and the fallback keeps the tooltip meaningful.
      data-tip={detail !== null && detail !== '' ? detail : i18n._(title)}
    >
      {i18n._(label)}
    </span>
  );
}
