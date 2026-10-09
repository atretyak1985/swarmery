// A phase's inline detail panel: it opens IN the plan's main column, in place
// of the phase list (the plan-level panel does the same), so the Story, the
// criteria and the doc get the whole column rather than a 640px drawer at the
// edge. The shell only: subtitle ("Phase 3 of 4 · 6/8 criteria · opus"),
// title, the Story · Criteria · Runs · Review · Report · Edit tab bar (Review
// only once the phase has run) and ↑/↓ stepping.
// The body stays Plans.tsx's PhaseDetailPanel, which owns the doc, the checkbox
// writes and the run handlers. The way out — "← all phases" and Escape — lives
// above the panel in the plan's action row, like the plan-level panel's does.

import { useEffect, useId, useRef, type ReactNode } from 'react';
import type { Epic, EpicPhase } from '../../api/types';
import { type TabItem, Tabs } from '../../components/Tabs';
import { modelShortName } from '../../lib/sessionModelChip';
import { hasReviewTab } from './landingModel';

export type PhaseTab = 'story' | 'criteria' | 'runs' | 'review' | 'report' | 'edit';

export const PHASE_TABS: readonly TabItem<PhaseTab>[] = [
  { id: 'story', label: 'Story' },
  { id: 'criteria', label: 'Criteria' },
  { id: 'runs', label: 'Runs' },
  { id: 'review', label: 'Review' },
  { id: 'report', label: 'Report' },
  { id: 'edit', label: 'Edit' },
];

/** "Phase 3 of 4 · 6/8 criteria · opus" — the panel's mono meta line. */
export function phasePanelSubtitle(epic: Epic, phase: EpicPhase): string {
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

/** True when a key press belongs to a text field, not to the panel. */
function typingIn(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName);
}

export interface PhasePanelProps {
  epic: Epic;
  phase: EpicPhase;
  tab: PhaseTab;
  onTab: (tab: PhaseTab) => void;
  /** Step to the previous / next phase (↑ / ↓); undefined at the ends. */
  onPrev?: (() => void) | undefined;
  onNext?: (() => void) | undefined;
  /** A done phase retires its Edit tab (its doc is the record, not a plan). */
  editable?: boolean;
  /** The URL of each tab — when given, the tabs are real links (their click
   * navigates; `onTab` then only serves callers without it). */
  hrefFor?: ((tab: PhaseTab) => string) | undefined;
  children: ReactNode;
}

export function PhasePanel({
  epic,
  phase,
  tab,
  onTab,
  onPrev,
  onNext,
  editable = true,
  hrefFor,
  children,
}: PhasePanelProps): JSX.Element {
  const panelRef = useRef<HTMLElement | null>(null);
  const titleId = useId();
  // Latest handlers without re-running the listener effect (callers pass arrows).
  const handlers = useRef({ onPrev, onNext });
  useEffect(() => {
    handlers.current = { onPrev, onNext };
  });

  // Opening a phase moves focus into its panel (the list row that opened it is
  // gone), so a keyboard user lands where the content is — without scrolling
  // the plan header and its "← all phases" control out of view.
  useEffect(() => {
    panelRef.current?.focus({ preventScroll: true });
  }, []);

  // ↑/↓ step through the plan's phases from anywhere on the page, except while
  // typing in a field (the Edit tab's textarea, the criteria filter).
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (typingIn(e.target)) return;
      const { onPrev: prev, onNext: next } = handlers.current;
      if (e.key === 'ArrowUp' && prev !== undefined) {
        e.preventDefault();
        prev();
      } else if (e.key === 'ArrowDown' && next !== undefined) {
        e.preventDefault();
        next();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('keydown', onKey);
    };
  }, []);

  // A phase that never ran has no branch to review, so no Review tab.
  const reviewable = hasReviewTab(phase);
  const tabs = PHASE_TABS.filter(
    (t) => (editable || t.id !== 'edit') && (reviewable || t.id !== 'review'),
  ).map((t): TabItem<PhaseTab> => ({
    ...t,
    ...(t.id === 'criteria' && phase.checkboxesTotal > 0
      ? { count: `${String(phase.checkboxesDone)}/${String(phase.checkboxesTotal)}` }
      : {}),
    ...(hrefFor !== undefined ? { href: hrefFor(t.id) } : {}),
  }));
  const label = tabs.find((t) => t.id === tab)?.label ?? tab;
  const stepping = onPrev !== undefined || onNext !== undefined;

  return (
    <section
      ref={panelRef}
      aria-labelledby={titleId}
      tabIndex={-1}
      className="overflow-hidden rounded-xl border border-line bg-surface outline-none"
    >
      <div className="border-b border-line px-4 pt-3 pb-3">
        <div className="flex items-center gap-2 font-mono text-[10px] text-ink-faint">
          <span className="min-w-0 truncate">{phasePanelSubtitle(epic, phase)}</span>
          {stepping && <span className="ml-auto shrink-0">↑↓ next phase</span>}
        </div>
        <h2 id={titleId} className="mt-1 text-[14px] leading-[1.3] font-semibold text-ink">
          {phase.name}
        </h2>
      </div>
      <div className="px-2">
        <Tabs tabs={tabs} value={tab} onChange={onTab} ariaLabel="phase details tabs" />
      </div>
      <div role="tabpanel" aria-label={label} className="px-4 py-3 text-[13px] leading-relaxed">
        {children}
      </div>
    </section>
  );
}
