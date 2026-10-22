// The guard badge (verify-not-trust phase 4): what the daemon's guard check found
// in a finished phase run, independent of the run's own report — protected files
// the run changed, a lent doc copy that was tampered with, calls the phase-guard
// hook refused, a guard block nobody can read. Red, because each one means the
// run's "done" is not the whole story. Renders nothing for a clean check, for a
// review written before the check existed (`{}`), and for an older daemon.
//
// Shown inline on the phase card, in the phase panel's Review tab and in the
// needs-you phase_decision card.

import { plural } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { guardFindings } from '../../api/guard';
import type { GuardSummary } from '../../api/types';

const PILL = 'rounded border border-red/40 bg-red/10 px-1.5 py-px font-mono text-[9.5px] text-red';

export function GuardBadge({
  guard,
  className = '',
}: {
  guard: GuardSummary | null | undefined;
  className?: string;
}): JSX.Element | null {
  const { t } = useLingui();
  const f = guardFindings(guard);
  if (f === null) return null;
  const changedCount = f.changed.length;
  const changedList = f.changed.join(', ');
  const blocked = f.blocked;
  return (
    <span role="status" aria-label={t`guard check`} className={`inline-flex flex-wrap items-center gap-1 ${className}`}>
      {changedCount > 0 && (
        <span className={PILL} data-tip={t`the run changed protected paths: ${changedList}`}>
          {plural(changedCount, {
            one: '# protected file changed',
            few: '# protected files changed',
            many: '# protected files changed',
            other: '# protected files changed',
          })}
        </span>
      )}
      {f.docCopyMismatch && (
        <span className={PILL} data-tip={t`the phase doc lent into the worktree was edited outside its checkboxes, Status, Forecast and Completion Report`}>
          {t`doc copy mismatch`}
        </span>
      )}
      {blocked > 0 && (
        <span className={PILL} data-tip={t`the phase guard refused these calls — see .swarmery/guard.log`}>
          {plural(blocked, {
            one: '# blocked attempt',
            few: '# blocked attempts',
            many: '# blocked attempts',
            other: '# blocked attempts',
          })}
        </span>
      )}
      {f.unreadable && (
        <span className={PILL} data-tip={guard?.parseError?.reason ?? ''}>
          {t`guard block unreadable`}
        </span>
      )}
    </span>
  );
}
