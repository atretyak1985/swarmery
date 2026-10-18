// Which transport carried a permission request: the swarmery-link mod (answered
// inside the session, no hook process) or the hook shim. Renders nothing when
// the daemon predates the field, so an old daemon shows no badge at all.

import { useLingui } from '@lingui/react/macro';
import type { ApprovalChannel } from '../api/types';

const TONE: Record<ApprovalChannel, string> = {
  mod: 'border-brand/50 text-brand',
  shim: 'border-line-strong text-ink-3',
};

export function ChannelBadge({
  channel,
  className = '',
}: {
  channel: ApprovalChannel | undefined;
  className?: string;
}): JSX.Element | null {
  const { t } = useLingui();
  if (channel === undefined) return null;
  const label = channel === 'mod' ? t`via mod` : t`via shim`;
  const tip =
    channel === 'mod'
      ? t`answered inside the session by the swarmery-link mod — no hook process`
      : t`answered through the swarmery hook process`;
  return (
    <span
      data-testid="channel-badge"
      data-channel={channel}
      // Focusable so the data-tip layer also opens for keyboard users; the
      // aria-label starts with the visible text (WCAG 2.5.3 label in name).
      tabIndex={0}
      data-tip={tip}
      aria-label={`${label} — ${tip}`}
      className={`rounded-full border px-[9px] py-0.5 font-mono text-[10.5px] font-semibold whitespace-nowrap focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand ${TONE[channel]} ${className}`}
    >
      {label}
    </span>
  );
}
