// A phase's own page, in the plan's main column in place of the plan: one band with
// the blocker banner over it when one waits on the operator, then a breadcrumb back
// to the plan with ↑/↓, the name, one sentence of state, the numbers, and six tabs
// with the phase's actions on the same line. The shell only: the body and every
// handler stay in PhaseDetailPanel, which owns the doc, the checkbox writes and the
// runs. The way back is the breadcrumb and Escape. Edit is an action, not a tab: its
// URL stays, so an old link still opens the editor.

import type { MessageDescriptor } from '@lingui/core';
import { msg, plural, t } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { useEffect, useId, useLayoutEffect, useRef, type KeyboardEvent, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import type { Epic, EpicPhase } from '../../api/types';
import { DetailHeader } from '../../components/place/DetailHeader';
import { nextTabIndex } from '../../components/Tabs';
import { modelShortName } from '../../lib/sessionModelChip';

export type PhaseTab = 'story' | 'plain' | 'criteria' | 'runs' | 'review' | 'report' | 'edit';

/** The phase page's tabs; labels are message descriptors, resolved with `i18n._()`
 * where they render. `edit` is listed for the browser-tab title. */
export const PHASE_TABS: readonly { id: PhaseTab; label: MessageDescriptor }[] = [
  { id: 'story', label: msg`Story` },
  { id: 'plain', label: msg`Plain words` },
  { id: 'criteria', label: msg`Criteria` },
  { id: 'runs', label: msg`Runs` },
  { id: 'review', label: msg`Review` },
  { id: 'report', label: msg`Report` },
  { id: 'edit', label: msg`Edit` },
];

/** "Phase 3 of 4 · 6/8 criteria · opus" — where the phase stands, in one line. */
export function phasePanelSubtitle(epic: Epic, phase: EpicPhase): string {
  const at = epic.phases.findIndex((p) => p.seq === phase.seq);
  const position = String(at + 1);
  const count = String(epic.phases.length);
  const seq = String(phase.seq);
  const done = String(phase.checkboxesDone);
  const parts = [
    at >= 0 ? t`Phase ${position} of ${count}` : t`Phase ${seq}`,
    plural(phase.checkboxesTotal, {
      one: `${done}/# criterion`,
      few: `${done}/# criteria`,
      many: `${done}/# criteria`,
      other: `${done}/# criteria`,
    }),
  ];
  const model = phase.runModel ?? phase.docModel;
  if (model !== null) parts.push(modelShortName(model));
  return parts.join(' · ');
}

/** True when a key press belongs to a text field, not to the page. */
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
  /** Back to the plan; the breadcrumb's link. */
  onBack?: (() => void) | undefined;
  /** The stage word and its colour class, in the breadcrumb. */
  stage?: { label: string; tone: string } | undefined;
  /** The URL of each tab — when given, the tabs are real links (their click
   * navigates; `onTab` then only serves callers without it). */
  hrefFor?: ((tab: PhaseTab) => string) | undefined;
  /** The BlockerBanner, above the band's content. */
  banner?: ReactNode;
  /** State sentence and numbers, between the name and the tabs. */
  head?: ReactNode;
  /** The small number beside a tab's label. */
  tabCounts?: Partial<Record<PhaseTab, string>> | undefined;
  /** On the tabs' line, at its right edge. */
  actions?: ReactNode;
  children: ReactNode;
}

const NAV_BUTTON =
  'h-5 w-[22px] cursor-pointer rounded-[5px] border border-line bg-transparent p-0 font-mono text-[11px] leading-none text-ink-dim transition-colors hover:text-ink disabled:cursor-default disabled:opacity-40';

export function PhasePanel({
  epic,
  phase,
  tab,
  onTab,
  onPrev,
  onNext,
  onBack,
  stage,
  hrefFor,
  banner,
  head,
  tabCounts = {},
  actions,
  children,
}: PhasePanelProps): JSX.Element {
  const { t, i18n } = useLingui();
  const panelRef = useRef<HTMLElement | null>(null);
  const tabRefs = useRef<Partial<Record<PhaseTab, HTMLElement | null>>>({});
  const titleId = useId();
  // Latest handlers without re-running the listener effect (callers pass arrows).
  // Synced in the layout phase, like the listener below, so a key pressed right
  // after a commit never reaches a stale handler.
  const handlers = useRef({ onPrev, onNext });
  useLayoutEffect(() => {
    handlers.current = { onPrev, onNext };
  });

  // Opening a phase moves focus onto its page (the ledger line that opened it is
  // gone), so a keyboard user lands where the content is.
  useEffect(() => {
    panelRef.current?.focus({ preventScroll: true });
  }, []);

  // ↑/↓ step through the plan's phases from anywhere on the page, except while
  // typing in a field (the Edit tab's textarea, the criteria filter). Installed
  // in the layout phase so the shortcut exists as soon as the panel's DOM does:
  // a passive effect can run after the panel is already visible.
  useLayoutEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent): void => {
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

  const tabs = PHASE_TABS.filter((def) => def.id !== 'edit');
  const label = PHASE_TABS.find((def) => def.id === tab);
  const at = epic.phases.findIndex((p) => p.seq === phase.seq);
  const position = String(at + 1);
  const count = String(epic.phases.length);
  const planTitle = epic.title;
  const branch = phase.runBranch ?? '';
  const current = tabs.findIndex((def) => def.id === tab);

  const onTabsKey = (e: KeyboardEvent<HTMLDivElement>): void => {
    const next = nextTabIndex(e.key, Math.max(current, 0), tabs.length);
    if (next === null) return;
    const target = tabs[next];
    if (target === undefined) return;
    e.preventDefault();
    const el = tabRefs.current[target.id];
    el?.focus();
    if (hrefFor !== undefined) el?.click();
    else onTab(target.id);
  };

  return (
    <section ref={panelRef} aria-labelledby={titleId} tabIndex={-1} className="outline-none">
      <DetailHeader bleed flush banner={banner}>
        <div className="flex min-w-0 items-center gap-2 font-mono text-[10.5px] leading-[normal] text-ink-faint">
          {onBack !== undefined && (
            <>
              <button
                type="button"
                onClick={onBack}
                data-tip={t`back to the plan (Esc)`}
                className="max-w-[40%] min-w-0 cursor-pointer truncate bg-transparent p-0 text-left whitespace-nowrap transition-colors hover:text-brand"
              >
                ← {planTitle}
              </button>
              <span aria-hidden="true">·</span>
            </>
          )}
          <span className="shrink-0 whitespace-nowrap">{at >= 0 ? t`phase ${position} of ${count}` : `#${String(phase.seq)}`}</span>
          {stage !== undefined && (
            <>
              <span aria-hidden="true">·</span>
              <span className={`shrink-0 whitespace-nowrap ${stage.tone}`}>{stage.label}</span>
            </>
          )}
          {branch !== '' && (
            <>
              <span aria-hidden="true">·</span>
              <span className="min-w-0 truncate">{branch}</span>
            </>
          )}
          <span className="ml-auto inline-flex shrink-0 gap-0.5">
            <button
              type="button"
              onClick={onPrev}
              disabled={onPrev === undefined}
              aria-label={t`previous phase`}
              data-tip={t`previous phase · ↑`}
              className={NAV_BUTTON}
            >
              ↑
            </button>
            <button
              type="button"
              onClick={onNext}
              disabled={onNext === undefined}
              aria-label={t`next phase`}
              data-tip={t`next phase · ↓`}
              className={NAV_BUTTON}
            >
              ↓
            </button>
          </span>
        </div>
        <h2 id={titleId} className="mt-2 text-[22px] leading-tight font-semibold tracking-[-0.015em] text-balance text-ink">
          {phase.name}
        </h2>
        {head}
        <div className="mt-[18px] flex flex-wrap items-center gap-0.5">
          <div
            role="tablist"
            aria-label={t`phase details tabs`}
            onKeyDown={onTabsKey}
            className="flex flex-wrap items-center gap-0.5"
          >
            {tabs.map((def, i) => {
              const selected = def.id === tab;
              const text = i18n._(def.label);
              const ref = (el: HTMLElement | null): void => {
                tabRefs.current[def.id] = el;
              };
              const className = `-mb-px inline-flex cursor-pointer items-center gap-1.5 border-b-2 bg-transparent px-3 py-2 text-[12.5px] leading-[normal] font-medium whitespace-nowrap transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand ${
                selected ? 'border-brand text-ink' : 'border-transparent text-ink-dim hover:text-ink'
              }`;
              const tabIndex = selected || (current === -1 && i === 0) ? 0 : -1;
              const body = (
                <>
                  {text}
                  {tabCounts[def.id] !== undefined && (
                    <span className="font-mono text-[10px] text-ink-faint">{tabCounts[def.id]}</span>
                  )}
                </>
              );
              return hrefFor !== undefined ? (
                <Link
                  key={def.id}
                  ref={ref}
                  to={hrefFor(def.id)}
                  role="tab"
                  aria-selected={selected}
                  tabIndex={tabIndex}
                  className={className}
                >
                  {body}
                </Link>
              ) : (
                <button
                  key={def.id}
                  ref={ref}
                  type="button"
                  role="tab"
                  aria-selected={selected}
                  tabIndex={tabIndex}
                  onClick={() => onTab(def.id)}
                  className={className}
                >
                  {body}
                </button>
              );
            })}
          </div>
          <span className="min-w-2 flex-1" />
          {actions !== undefined && <div className="inline-flex flex-wrap gap-1.5 py-1">{actions}</div>}
        </div>
      </DetailHeader>
      <div
        role="tabpanel"
        aria-label={label === undefined ? tab : i18n._(label.label)}
        className="mt-[18px] text-[13px] leading-relaxed"
      >
        {children}
      </div>
    </section>
  );
}
