// A dock tab's action menu (terminal-dock-webstorm phase 4): Split right / down,
// Move left / right, Move to group N, Close — the keyboard route to everything a
// drag does. Opened by the tab's "⋯" button, Shift+F10 or the ContextMenu key.
// The menu takes focus when it opens; ↑/↓ (Home/End) walk the enabled items, Enter
// or Space runs one, Esc closes it and hands focus back to the tab.
//
// Positioned `fixed` at the anchor, so a tab strip's horizontal scroller never
// clips it; it stays inside the dock's DOM subtree, so it still shows when the
// dock is the full-screen element.

import { useEffect, useLayoutEffect, useRef, useState } from 'react';

/** One menu entry. A disabled entry is shown but skipped by the keyboard. */
export interface TabMenuItem {
  key: string;
  label: string;
  disabled?: boolean;
  onSelect: () => void;
}

/** Gap kept between the menu and its anchor / the viewport edge, in px. */
const GAP = 2;
const VIEWPORT_MARGIN = 4;
/** Marks the element that toggles the menu, so pressing it is not an "outside" press. */
export const MENU_TRIGGER_ATTR = 'data-tab-menu-trigger';

/** The viewport box the menu hangs from (the tab's client rect). */
export interface MenuAnchor {
  left: number;
  top: number;
  bottom: number;
}

export function TabMenu({
  items,
  label,
  anchor,
  onSelect,
  onDismiss,
}: {
  items: readonly TabMenuItem[];
  /** Accessible name of the menu. */
  label: string;
  anchor: MenuAnchor;
  /** Runs an enabled item; the owner closes the menu and acts. */
  onSelect: (item: TabMenuItem) => void;
  /** Closes without acting; `restoreFocus` hands focus back to the tab (Esc). */
  onDismiss: (restoreFocus: boolean) => void;
}): JSX.Element {
  const menuRef = useRef<HTMLDivElement>(null);
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const [pos, setPos] = useState({ left: anchor.left, top: anchor.bottom + GAP });

  // The menu itself takes focus, so ↓ lands on the first item and ↑ on the last.
  useEffect(() => {
    menuRef.current?.focus();
  }, []);

  // Keep the whole menu on screen: shift it left, or flip it above the anchor.
  useLayoutEffect(() => {
    const el = menuRef.current;
    if (el === null) return;
    const r = el.getBoundingClientRect();
    const maxLeft = window.innerWidth - r.width - VIEWPORT_MARGIN;
    const left = Math.max(VIEWPORT_MARGIN, Math.min(anchor.left, maxLeft));
    const below = anchor.bottom + GAP;
    const fitsBelow = below + r.height <= window.innerHeight - VIEWPORT_MARGIN;
    const top = fitsBelow ? below : Math.max(VIEWPORT_MARGIN, anchor.top - GAP - r.height);
    setPos((p) => (p.left === left && p.top === top ? p : { left, top }));
  }, [anchor.left, anchor.top, anchor.bottom]);

  // A press anywhere outside closes the menu (focus stays where the press put it).
  // The trigger is exempt: its own click toggles the menu.
  useEffect(() => {
    const onPointerDown = (e: PointerEvent): void => {
      const el = menuRef.current;
      if (!(e.target instanceof Element)) return;
      if (el !== null && el.contains(e.target)) return;
      if (e.target.closest(`[${MENU_TRIGGER_ATTR}]`) !== null) return;
      onDismiss(false);
    };
    document.addEventListener('pointerdown', onPointerDown, true);
    return () => document.removeEventListener('pointerdown', onPointerDown, true);
  }, [onDismiss]);

  const enabled = items.map((it, i) => (it.disabled === true ? -1 : i)).filter((i) => i >= 0);

  const focusItem = (i: number | undefined): void => {
    if (i !== undefined) itemRefs.current[i]?.focus();
  };

  const onKeyDown = (e: React.KeyboardEvent): void => {
    const current = itemRefs.current.findIndex((el) => el !== null && el === document.activeElement);
    const at = enabled.indexOf(current);
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        focusItem(at < 0 ? enabled[0] : enabled[(at + 1) % enabled.length]);
        return;
      case 'ArrowUp':
        e.preventDefault();
        focusItem(at < 0 ? enabled[enabled.length - 1] : enabled[(at - 1 + enabled.length) % enabled.length]);
        return;
      case 'Home':
        e.preventDefault();
        focusItem(enabled[0]);
        return;
      case 'End':
        e.preventDefault();
        focusItem(enabled[enabled.length - 1]);
        return;
      case 'Enter':
      case ' ': {
        // preventDefault also stops the browser's own Enter → click on the button.
        e.preventDefault();
        const item = items[current];
        if (item !== undefined && item.disabled !== true) onSelect(item);
        return;
      }
      case 'Escape':
        // Handled here: the dock's "Esc restores a maximized dock" must not fire too.
        e.preventDefault();
        e.stopPropagation();
        onDismiss(true);
        return;
      case 'Tab':
        e.preventDefault();
        onDismiss(true);
        return;
      default:
    }
  };

  return (
    <div
      ref={menuRef}
      role="menu"
      aria-label={label}
      aria-orientation="vertical"
      tabIndex={-1}
      onKeyDown={onKeyDown}
      className="fixed z-50 min-w-[168px] rounded-md border border-line-strong bg-surface2 py-1 font-mono text-[11px] text-ink shadow-lg outline-none"
      style={{ left: pos.left, top: pos.top }}
    >
      {items.map((item, i) => (
        <button
          key={item.key}
          ref={(el) => {
            itemRefs.current[i] = el;
          }}
          type="button"
          role="menuitem"
          tabIndex={-1}
          disabled={item.disabled === true}
          onClick={() => {
            if (item.disabled !== true) onSelect(item);
          }}
          className="flex w-full items-center px-3 py-1 text-left transition-colors hover:bg-bg focus:bg-bg focus:outline-none disabled:cursor-default disabled:text-ink-faint disabled:hover:bg-transparent"
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}
