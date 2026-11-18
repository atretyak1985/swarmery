// The plan's condensed strip (D-14), shown in place of the full PlanHeader while a
// phase is open: one row on `bg-surface2`, mono 11px. The title here opens the
// phase LIST, not the plan's details — the full header's own title keeps that job;
// this bar exists so a phase page can get back to the list without losing the
// plan's identity and run state. The blocker banner is the caller's: it stands
// above this bar, not inside it (PlanHeader renders both).

import { useLingui } from '@lingui/react/macro';
import type { ReactNode } from 'react';
import type { Epic } from '../../api/types';
import { fmtSpent } from './planModel';
import { PlanBarPhases } from './PlanBarPhases';

export function PlanBar({
  epic,
  resolvedSeqs,
  spent,
  onBack,
  action,
}: {
  epic: Epic;
  resolvedSeqs: Set<number>;
  /** No forecast appears here on purpose (v4 D-5): a plan has none in dollars. */
  spent: number | null;
  /** The bar's title leads back to the phase list. */
  onBack: () => void;
  /** The one pause/resume/run(/retry) button, built by the caller so it reads the
   * same run state the full header's own button would. */
  action: ReactNode;
}): JSX.Element {
  const { t } = useLingui();
  return (
    <div className="flex items-center gap-3 bg-surface2 px-4 py-[7px] font-mono text-[11px] leading-[normal] text-ink-dim desk:px-7">
      <span className="shrink-0 text-[10px] tracking-[0.08em] text-ink-faint uppercase">{t`plan`}</span>
      <button
        type="button"
        onClick={onBack}
        data-tip={t`back to the phase list`}
        className="min-w-0 max-w-[280px] cursor-pointer truncate bg-transparent p-0 text-left text-[13px] font-semibold text-ink transition-colors hover:text-brand"
      >
        {epic.title}
      </button>
      <PlanBarPhases epic={epic} resolvedSeqs={resolvedSeqs} />
      {spent !== null && (
        <span className="shrink-0 whitespace-nowrap">
          <span className="text-brand">{fmtSpent(spent)}</span> {t`spent`}
        </span>
      )}
      <span className="ml-auto shrink-0">{action}</span>
    </div>
  );
}
