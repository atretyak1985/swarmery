// A small reusable tablist for the System detail panels.
//
// It exists because the two tab bars this project already had (System.tsx's
// page tabs, ItemDetail's edit/preview toggle) carry role="tab" + aria-selected
// but no keyboard support at all: every tab sits in the tab order and no arrow
// key does anything, which is the WAI-ARIA tabs pattern half-implemented. This
// one does the other half — roving tabindex (exactly ONE tab is tabbable, the
// active one), ArrowLeft/ArrowRight with wraparound, Home/End — and ties each
// tab to the panel it controls via aria-controls/aria-labelledby.
//
// The visual classes are copied verbatim from System.tsx's page tab bar so a
// section tablist reads as native inside the panel. The key handling itself is
// in ./docsSection (nextTabIndex, tabIndexOf) so it can be unit-tested without
// a renderer — see docsSection.test.ts.

import { useRef } from 'react';
import type { KeyboardEvent, ReactNode } from 'react';
import { nextTabIndex, tabDomIds, tabIndexOf } from './docsSection';

export function SectionTabs({
  tabs,
  labels,
  active,
  onSelect,
  onReselect,
  idPrefix,
  ariaLabel,
}: {
  tabs: readonly string[];
  labels: Record<string, string>;
  /** No tab open is a state of its own: every tab reads unselected, the first
   * stays in the tab order. */
  active: string | null;
  onSelect: (tab: string) => void;
  /** A click on the open tab; absent, it does nothing. Keys never fold. */
  onReselect?: () => void;
  /** Namespace for the tab/panel DOM ids — unique per mounted tablist. */
  idPrefix: string;
  ariaLabel: string;
}): JSX.Element {
  const btnRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    const next = nextTabIndex(e.key, active === null ? -1 : tabs.indexOf(active), tabs.length);
    if (next === null) return; // not our key — leave the event alone
    const target = tabs[next];
    if (target === undefined) return;
    e.preventDefault();
    onSelect(target);
    // Selection follows focus (the pattern's automatic-activation variant):
    // move the DOM focus too, or the roving tabindex would strand it on a
    // button that just became tabIndex=-1.
    btnRefs.current[target]?.focus();
  };

  return (
    <div
      role="tablist"
      aria-label={ariaLabel}
      onKeyDown={onKeyDown}
      className="mt-3 flex gap-1 overflow-x-auto border-b border-line [-webkit-overflow-scrolling:touch]"
    >
      {tabs.map((t) => {
        const { tabId, panelId } = tabDomIds(idPrefix, t);
        const selected = t === active;
        return (
          <button
            key={t}
            id={tabId}
            ref={(el) => {
              btnRefs.current[t] = el;
            }}
            type="button"
            role="tab"
            aria-selected={selected}
            // Only the SELECTED tab claims aria-controls. The panels are
            // mounted one at a time, so an inactive tab would point at an id
            // that is not in the document — an axe `aria-valid-attr-value`
            // violation and a dead reference for a screen reader following it.
            //
            // The alternative fix (mount every panel, hide the inactive ones)
            // was rejected: ItemDetail's Definition panel is the whole editor,
            // version history and diff viewer, and mounting it behind `hidden`
            // while the reader is on Docs would run all of that for a panel
            // nobody can see. aria-controls is only RECOMMENDED on role="tab"
            // (WAI-ARIA APG), so dropping it where it cannot resolve costs
            // nothing and keeps the DOM at one panel.
            aria-controls={selected ? panelId : undefined}
            tabIndex={tabIndexOf(t, active ?? '', tabs)}
            onClick={() => (selected ? onReselect?.() : onSelect(t))}
            className={`-mb-px shrink-0 border-b-2 px-3.5 py-[7px] text-[12.5px] font-medium whitespace-nowrap transition-colors ${
              selected ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
            }`}
          >
            {labels[t] ?? t}
          </button>
        );
      })}
    </div>
  );
}

/** The panel half of the pattern — labelled by, and controlled by, its tab. */
export function SectionPanel({
  idPrefix,
  tab,
  children,
}: {
  idPrefix: string;
  tab: string;
  children: ReactNode;
}): JSX.Element {
  const { tabId, panelId } = tabDomIds(idPrefix, tab);
  return (
    <div role="tabpanel" id={panelId} aria-labelledby={tabId} tabIndex={0}>
      {children}
    </div>
  );
}

/** The sections of a detail that the design's blocks cannot express: one tablist
 * under the blocks, all folded until one is opened (`active` null), then its one
 * mounted panel. A click on the open tab folds it again. */
export function DetailSections({
  tabs,
  labels,
  active,
  onSelect,
  idPrefix,
  ariaLabel,
  children,
}: {
  tabs: readonly string[];
  labels: Record<string, string>;
  active: string | null;
  onSelect: (tab: string | null) => void;
  idPrefix: string;
  ariaLabel: string;
  children: ReactNode;
}): JSX.Element {
  return (
    <div className="mt-5">
      <SectionTabs
        tabs={tabs}
        labels={labels}
        active={active}
        onSelect={onSelect}
        onReselect={() => onSelect(null)}
        idPrefix={idPrefix}
        ariaLabel={ariaLabel}
      />
      {active !== null && (
        <div className="mt-4">
          <SectionPanel idPrefix={idPrefix} tab={active}>
            {children}
          </SectionPanel>
        </div>
      )}
    </div>
  );
}
