// One tab group's strip in the terminal dock (terminal-dock-webstorm phase 4).
// With a single group it sits inline in the dock header (the look the dock has
// always had); with several, each group carries its own strip on top.
//
// Tabs drag with native HTML5 DnD: the dragged tab id rides on TAB_MIME. Over a
// strip, the insertion point is the first tab whose midpoint lies right of the
// pointer, shown as a thin bar; a drop hands (tab, index) to the dock, which
// reorders within the group or moves the tab across groups. Each tab's "⋯"
// button, Shift+F10 or the ContextMenu key opens its TabMenu — the keyboard
// route to every drag action.

import { useLingui } from '@lingui/react/macro';
import { type CSSProperties, Fragment, type ReactNode, useEffect, useRef, useState } from 'react';
import { type MenuAnchor, TabMenu, type TabMenuItem } from './TabMenu';
import type { TermStatus } from './XTerm';

/** The drag payload type: a dock tab id. */
export const TAB_MIME = 'application/x-swarmery-tab';

/** What a strip shows for one tab; the dock derives it from the tab and its PTY. */
export interface StripTab {
  id: string;
  /** Short caption (a Claude tab's session title, else its label). */
  caption: string;
  /** The full session title, shown on hover; null when there is none. */
  fullTitle: string | null;
  status: TermStatus | undefined;
  /** Why the tab's terminal closed, as a tooltip; undefined while it is not closed. */
  closedWhy: string | undefined;
}

/** Does this drag carry a dock tab? */
export function hasDraggedTab(dt: DataTransfer | null | undefined): boolean {
  if (dt === null || dt === undefined || dt.types === undefined) return false;
  return Array.from(dt.types).includes(TAB_MIME);
}

/** The index a drop at `clientX` inserts before: the first tab whose midpoint lies right of it. */
export function insertionIndex(tabEls: readonly Element[], clientX: number): number {
  for (let i = 0; i < tabEls.length; i++) {
    const r = tabEls[i]?.getBoundingClientRect();
    if (r !== undefined && clientX < r.left + r.width / 2) return i;
  }
  return tabEls.length;
}

export function TabStrip({
  groupId,
  tabs,
  activeId,
  className,
  style,
  dragging,
  onActivate,
  onClose,
  onDragStart,
  onDragEnd,
  onDropTab,
  menuItems,
  children,
}: {
  groupId: string;
  /** The group's tabs, in strip order. */
  tabs: readonly StripTab[];
  activeId: string | null;
  className: string;
  style?: CSSProperties | undefined;
  /** The tab being dragged anywhere in the dock, or null. */
  dragging: string | null;
  onActivate: (tabId: string) => void;
  onClose: (tabId: string) => void;
  onDragStart: (tabId: string) => void;
  onDragEnd: () => void;
  /** A tab dropped here, before this strip's tab at `index` (counted with the dragged tab in place). */
  onDropTab: (tabId: string, index: number) => void;
  /** The menu entries for one of this strip's tabs. */
  menuItems: (tabId: string) => TabMenuItem[];
  /** Trailing content (the header's "+" button). */
  children?: ReactNode;
}): JSX.Element {
  const { t } = useLingui();
  const stripRef = useRef<HTMLDivElement>(null);
  const buttonRefs = useRef(new Map<string, HTMLButtonElement>());
  const [insertAt, setInsertAt] = useState<number | null>(null);
  const [menu, setMenu] = useState<{ tabId: string; anchor: MenuAnchor } | null>(null);
  // A menu whose tab left this strip (closed, moved by a drag) is gone with it.
  const openMenuFor = menu !== null && tabs.some((tab) => tab.id === menu.tabId) ? menu : null;

  // A drag that ended anywhere leaves no insertion point behind for the next one.
  useEffect(() => {
    if (dragging === null) setInsertAt(null);
  }, [dragging]);

  const tabEls = (): Element[] => Array.from(stripRef.current?.querySelectorAll('[data-tab-id]') ?? []);
  const accepts = (e: React.DragEvent): boolean => dragging !== null || hasDraggedTab(e.dataTransfer);

  const onStripDragOver = (e: React.DragEvent<HTMLDivElement>): void => {
    if (!accepts(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const at = insertionIndex(tabEls(), e.clientX);
    setInsertAt((prev) => (prev === at ? prev : at));
  };
  const onStripDragLeave = (e: React.DragEvent<HTMLDivElement>): void => {
    const next = e.relatedTarget;
    if (next instanceof Node && stripRef.current?.contains(next)) return;
    setInsertAt(null);
  };
  const onStripDrop = (e: React.DragEvent<HTMLDivElement>): void => {
    if (!accepts(e)) return;
    e.preventDefault();
    const id = e.dataTransfer.getData(TAB_MIME) || dragging;
    const at = insertionIndex(tabEls(), e.clientX);
    setInsertAt(null);
    if (id !== null && id !== '') onDropTab(id, at);
  };

  const openMenu = (tabId: string, from: HTMLElement): void => {
    const box = (from.closest('[data-tab-id]') ?? from).getBoundingClientRect();
    setMenu({ tabId, anchor: { left: box.left, top: box.top, bottom: box.bottom } });
  };
  const dismissMenu = (restoreFocus: boolean): void => {
    const id = menu?.tabId;
    setMenu(null);
    if (restoreFocus && id !== undefined) buttonRefs.current.get(id)?.focus();
  };

  const marker = (
    <span
      aria-hidden="true"
      data-testid="insert-marker"
      // -mx-[3px] cancels the extra flex gap, so the tabs do not shift under the pointer.
      className="-mx-[3px] h-4 w-0.5 shrink-0 rounded bg-brand"
    />
  );
  const showMarker = dragging !== null && insertAt !== null;

  return (
    <div
      ref={stripRef}
      data-testid="tab-strip"
      data-group-id={groupId}
      className={className}
      style={style}
      onDragOver={onStripDragOver}
      onDragLeave={onStripDragLeave}
      onDrop={onStripDrop}
    >
      {tabs.map((tab, i) => {
        const active = tab.id === activeId;
        const { caption, status: st } = tab;
        return (
          <Fragment key={tab.id}>
            {showMarker && insertAt === i && marker}
            <div
              data-tab-id={tab.id}
              draggable
              onDragStart={(e) => {
                e.dataTransfer.setData(TAB_MIME, tab.id);
                e.dataTransfer.effectAllowed = 'move';
                onDragStart(tab.id);
              }}
              onDragEnd={onDragEnd}
              className={`group flex shrink-0 items-center gap-1.5 rounded-md border px-2 py-1 font-mono text-[10.5px] ${
                active ? 'border-line-strong bg-surface2 text-ink' : 'border-transparent text-ink-dim'
              }${dragging === tab.id ? ' opacity-50' : ''}`}
            >
              <button
                ref={(el) => {
                  if (el === null) buttonRefs.current.delete(tab.id);
                  else buttonRefs.current.set(tab.id, el);
                }}
                type="button"
                data-tab-button={tab.id}
                aria-haspopup="menu"
                onClick={() => onActivate(tab.id)}
                onKeyDown={(e) => {
                  if ((e.key === 'F10' && e.shiftKey) || e.key === 'ContextMenu') {
                    e.preventDefault();
                    openMenu(tab.id, e.currentTarget);
                  }
                }}
                onContextMenu={(e) => {
                  e.preventDefault();
                  openMenu(tab.id, e.currentTarget);
                }}
                title={tab.closedWhy}
                className="flex items-center gap-1.5"
              >
                <span
                  aria-hidden="true"
                  className={`inline-block h-[6px] w-[6px] rounded-full ${
                    st === 'open' ? 'bg-green' : st === 'closed' ? 'bg-red' : 'bg-amber'
                  }`}
                />
                <span className="max-w-[140px] truncate" title={tab.fullTitle ?? undefined}>
                  {caption}
                </span>
              </button>
              <button
                type="button"
                aria-label={t`Actions for ${caption}`}
                aria-haspopup="menu"
                aria-expanded={openMenuFor?.tabId === tab.id}
                // TabMenu's outside-press check exempts its trigger (MENU_TRIGGER_ATTR).
                data-tab-menu-trigger=""
                onClick={(e) => {
                  if (openMenuFor?.tabId === tab.id) setMenu(null);
                  else openMenu(tab.id, e.currentTarget);
                }}
                className="text-ink-faint opacity-0 transition-opacity hover:text-ink focus-visible:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100"
              >
                ⋯
              </button>
              <button
                type="button"
                aria-label={`close ${caption}`}
                onClick={() => onClose(tab.id)}
                className="text-ink-faint transition-colors hover:text-red"
              >
                ×
              </button>
            </div>
          </Fragment>
        );
      })}
      {showMarker && insertAt === tabs.length && marker}
      {children}
      {openMenuFor !== null && (
        <TabMenu
          items={menuItems(openMenuFor.tabId)}
          label={t`Tab actions`}
          anchor={openMenuFor.anchor}
          onSelect={(item) => {
            setMenu(null);
            item.onSelect();
          }}
          onDismiss={dismissMenu}
        />
      )}
    </div>
  );
}
