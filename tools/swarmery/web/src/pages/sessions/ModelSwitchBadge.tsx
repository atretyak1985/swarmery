// Model-switch badge (Sessions rows): the session's own turns ran on more than
// one model. A broader fact than the "→ fell back to" chip (SessionCard's
// SessionModelCell), which compares only the first and last model — a session
// that went opus → sonnet → opus switched twice and changed nothing at the ends.
//
// The daemon decides (internal/api/session_model.go: orchestrator turns only,
// `<synthetic>` stand-ins excluded, `[1m]` folded); this renders its answer and
// nothing else. Neutral tone on purpose: a switch is not a defect, the chip
// beside it carries the amber when the move was a fallback.

import { plural } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import type { Session } from '../../api/types';

export function ModelSwitchBadge({
  session,
}: {
  session: Pick<Session, 'modelSwitched' | 'models'>;
}): JSX.Element | null {
  const { t } = useLingui();
  const models = session.models ?? [];
  if (session.modelSwitched !== true || models.length < 2) return null;
  const count = models.length;
  // Full ids, as SessionModelCell's tooltip keeps them: the `[1m]`-free id is
  // the exact model each turn ran on.
  const list = models.join(' → ');
  return (
    <span
      data-testid="model-switch-badge"
      role="img"
      data-tip={t`switched model mid-session: ${list}`}
      aria-label={t`switched model mid-session: ${list}`}
      className="shrink-0 rounded-full border border-blue/40 bg-blue/10 px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap text-blue"
    >
      ⇄{' '}
      {plural(count, { one: '# model', few: '# models', many: '# models', other: '# models' })}
    </span>
  );
}
