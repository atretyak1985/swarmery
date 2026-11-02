// The lesson effect verdict chip and its confounder list (honest learning
// phase 1). The verdict says how much of a lesson's measured drop it can be
// credited with; a confounded lesson's drop is shared with the changes listed.
import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { EffectVerdict, LessonConfounder } from '../../api/lessons';

const VERDICT_LABEL: Record<EffectVerdict, MessageDescriptor> = {
  helped: msg`helped`,
  hurt: msg`hurt`,
  no_effect: msg`no effect`,
  confounded: msg`confounded`,
  insufficient: msg`not enough data`,
};

const VERDICT_TIP: Record<EffectVerdict, MessageDescriptor> = {
  helped: msg`The area got less surprising after this lesson, and nothing else changed there in the measured window.`,
  hurt: msg`The area got more surprising after this lesson, and nothing else changed there in the measured window.`,
  no_effect: msg`The area's surprise barely moved after this lesson.`,
  confounded: msg`Something else changed in the measured window, so the drop cannot be credited to this lesson alone. It ranks like an unmeasured lesson and is never proposed as ineffective.`,
  insufficient: msg`Too few scored runs on one side of the activation to judge yet.`,
};

const VERDICT_TONE: Record<EffectVerdict, string> = {
  helped: 'border-green/40 text-green',
  hurt: 'border-red/40 text-red',
  no_effect: 'border-line text-ink-dim',
  confounded: 'border-amber/40 text-amber',
  insufficient: 'border-line text-ink-faint',
};

const KIND_LABEL: Record<LessonConfounder['kind'], MessageDescriptor> = {
  lesson: msg`another lesson`,
  proposal: msg`applied agent change`,
  model_switch: msg`model switch`,
};

export function EffectVerdictChip({ verdict }: { verdict: EffectVerdict }): JSX.Element {
  const { i18n } = useLingui();
  return (
    <span
      data-testid="effect-verdict"
      data-verdict={verdict}
      title={i18n._(VERDICT_TIP[verdict])}
      className={`rounded border px-1 font-mono text-[10px] ${VERDICT_TONE[verdict]}`}
    >
      {i18n._(VERDICT_LABEL[verdict])}
    </span>
  );
}

/** The confounders by name: kind, id, and the detector's own sentence. */
export function ConfounderList({ confounders }: { confounders: readonly LessonConfounder[] }): JSX.Element | null {
  const { i18n } = useLingui();
  if (confounders.length === 0) return null;
  return (
    <div className="mt-1 text-[11px] text-ink-dim">
      <div className="font-mono text-[10px]">
        <Trans>confounded by:</Trans>
      </div>
      <ul className="mt-0.5 grid gap-0.5" aria-label={i18n._(msg`confounders`)}>
        {confounders.map((c) => (
          <li key={`${c.kind}:${c.id}:${c.at}`}>
            <span className="font-mono text-[10px] text-amber">{i18n._(KIND_LABEL[c.kind])}</span>{' '}
            <span className="font-mono text-[10px] text-ink">{c.id}</span> — {c.detail}
          </li>
        ))}
      </ul>
    </div>
  );
}
