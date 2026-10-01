// Canvas v3 artboard 2d: a phase opens in the shared Drawer OVER the plan's
// phase list instead of replacing it. The shell only: subtitle ("Phase 3 of 4 ·
// 6/8 criteria · opus"), title, the Story · Criteria · Runs · Report · Edit tab
// bar and ↑/↓ stepping. The body stays Plans.tsx's PhaseDetailPanel, which owns
// the doc, the checkbox writes and the run handlers.

import type { ReactNode } from 'react';
import type { Epic, EpicPhase } from '../../api/types';
import { Drawer } from '../../components/Drawer';
import { type TabItem, Tabs } from '../../components/Tabs';
import { modelShortName } from '../../lib/sessionModelChip';

export type PhaseTab = 'story' | 'criteria' | 'runs' | 'report' | 'edit';

export const PHASE_TABS: readonly TabItem<PhaseTab>[] = [
  { id: 'story', label: 'Story' },
  { id: 'criteria', label: 'Criteria' },
  { id: 'runs', label: 'Runs' },
  { id: 'report', label: 'Report' },
  { id: 'edit', label: 'Edit' },
];

/** "Phase 3 of 4 · 6/8 criteria · opus" — the drawer's mono meta line. */
export function phaseDrawerSubtitle(epic: Epic, phase: EpicPhase): string {
  const at = epic.phases.findIndex((p) => p.seq === phase.seq);
  const parts = [
    at >= 0
      ? `Phase ${String(at + 1)} of ${String(epic.phases.length)}`
      : `Phase ${String(phase.seq)}`,
    `${String(phase.checkboxesDone)}/${String(phase.checkboxesTotal)} criteria`,
  ];
  const model = phase.runModel ?? phase.docModel;
  if (model !== null) parts.push(modelShortName(model));
  return parts.join(' · ');
}

export interface PhaseDrawerProps {
  epic: Epic;
  phase: EpicPhase;
  tab: PhaseTab;
  onTab: (tab: PhaseTab) => void;
  /** Step to the previous / next phase (↑ / ↓); undefined at the ends. */
  onPrev?: (() => void) | undefined;
  onNext?: (() => void) | undefined;
  onClose: () => void;
  /** A done phase retires its Edit tab (its doc is the record, not a plan). */
  editable?: boolean;
  /** The URL of each tab — when given, the tabs are real links (their click
   * navigates; `onTab` then only serves callers without it). */
  hrefFor?: ((tab: PhaseTab) => string) | undefined;
  children: ReactNode;
}

export function PhaseDrawer({
  epic,
  phase,
  tab,
  onTab,
  onPrev,
  onNext,
  onClose,
  editable = true,
  hrefFor,
  children,
}: PhaseDrawerProps): JSX.Element {
  const tabs = PHASE_TABS.filter((t) => editable || t.id !== 'edit').map((t): TabItem<PhaseTab> => ({
    ...t,
    ...(t.id === 'criteria' && phase.checkboxesTotal > 0
      ? { count: `${String(phase.checkboxesDone)}/${String(phase.checkboxesTotal)}` }
      : {}),
    ...(hrefFor !== undefined ? { href: hrefFor(t.id) } : {}),
  }));
  const label = tabs.find((t) => t.id === tab)?.label ?? tab;
  return (
    <Drawer
      open
      onClose={onClose}
      width={640}
      subtitle={phaseDrawerSubtitle(epic, phase)}
      title={phase.name}
      {...(onPrev !== undefined ? { onPrev } : {})}
      {...(onNext !== undefined ? { onNext } : {})}
    >
      <div className="-mx-[22px] -mt-[18px] mb-4 px-[14px]">
        <Tabs tabs={tabs} value={tab} onChange={onTab} ariaLabel="phase details tabs" />
      </div>
      <div role="tabpanel" aria-label={label} className="text-[13px] leading-relaxed">
        {children}
      </div>
    </Drawer>
  );
}
