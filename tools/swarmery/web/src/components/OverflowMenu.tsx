// The `⋯` menu of a detail's action bar: the rare actions, out of the way.
//
// WAI-ARIA menu button: the trigger carries aria-haspopup/aria-expanded, the list
// is role=menu with role=menuitem buttons. Opening from the keyboard (Enter, Space,
// ArrowDown, ArrowUp) lands on an item; ArrowUp/ArrowDown/Home/End move between
// items; Escape closes and puts focus back on the trigger, as does picking an item.
// Tab and a click outside close it without stealing focus.
//
// An item that cannot run now stays in the list, disabled (aria-disabled, still
// reachable by the arrows), with its reason in grey on its right: an action that
// silently vanished would leave the reader guessing whether it exists.

import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';

export interface OverflowMenuItem {
  id: string;
  label: string;
  onSelect: () => void;
  /** Why the item cannot run now. Set → disabled, the reason shown on its right. */
  disabledReason?: string | null;
  tone?: 'default' | 'red';
}

const TRIGGER =
  'rounded-[7px] border border-line-strong bg-transparent px-3 py-1.5 font-mono text-[11.5px] leading-[normal] whitespace-nowrap text-ink-2 transition-colors hover:border-ink-dim focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand';

export function OverflowMenu({
  label,
  items,
  glyph = '⋯',
}: {
  /** The trigger's and the menu's accessible name. */
  label: string;
  items: readonly OverflowMenuItem[];
  glyph?: string;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLSpanElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  // Which item takes focus once the menu mounts: the first, or the last (ArrowUp).
  const landOn = useRef<'first' | 'last'>('first');
  const menuId = useId();

  const itemEls = (): HTMLButtonElement[] =>
    Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]') ?? []);

  const close = useCallback((refocus: boolean): void => {
    setOpen(false);
    if (refocus) triggerRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!open) return undefined;
    const els = itemEls();
    (landOn.current === 'last' ? els[els.length - 1] : els[0])?.focus();
    const onDown = (e: MouseEvent): void => {
      if (e.target instanceof Node && rootRef.current?.contains(e.target) !== true) close(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open, close]);

  const openAt = (where: 'first' | 'last'): void => {
    landOn.current = where;
    setOpen(true);
  };

  const onTriggerKey = (e: KeyboardEvent<HTMLButtonElement>): void => {
    if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      openAt('first');
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      openAt('last');
    } else if (e.key === 'Escape' && open) {
      e.preventDefault();
      close(true);
    }
  };

  const onMenuKey = (e: KeyboardEvent<HTMLDivElement>): void => {
    const els = itemEls();
    const at = els.indexOf(document.activeElement as HTMLButtonElement);
    const move = (to: number): void => {
      e.preventDefault();
      els[(to + els.length) % els.length]?.focus();
    };
    switch (e.key) {
      case 'ArrowDown':
        move(at + 1);
        break;
      case 'ArrowUp':
        move(at - 1);
        break;
      case 'Home':
        move(0);
        break;
      case 'End':
        move(els.length - 1);
        break;
      case 'Escape':
        e.preventDefault();
        e.stopPropagation();
        close(true);
        break;
      case 'Tab':
        close(false);
        break;
    }
  };

  const pick = (item: OverflowMenuItem): void => {
    if (item.disabledReason != null) return;
    close(true);
    item.onSelect();
  };

  return (
    <span ref={rootRef} className="relative inline-flex">
      <button
        ref={triggerRef}
        type="button"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => (open ? close(false) : openAt('first'))}
        onKeyDown={onTriggerKey}
        className={TRIGGER}
      >
        {glyph}
      </button>
      {open && (
        <div
          ref={menuRef}
          id={menuId}
          role="menu"
          aria-label={label}
          onKeyDown={onMenuKey}
          className="absolute top-[calc(100%+6px)] right-0 z-30 flex min-w-[240px] flex-col rounded-[9px] border border-line-strong bg-surface py-1 shadow-[0_12px_30px_rgba(0,0,0,0.35)]"
        >
          {items.map((item) => {
            const reason = item.disabledReason ?? null;
            return (
              <button
                key={item.id}
                type="button"
                role="menuitem"
                tabIndex={-1}
                aria-disabled={reason !== null ? true : undefined}
                onClick={() => pick(item)}
                className={`flex items-baseline justify-between gap-4 px-3 py-1.5 text-left font-mono text-[11.5px] whitespace-nowrap outline-none focus-visible:bg-surface2 hover:bg-surface2 ${
                  reason !== null ? 'cursor-not-allowed text-ink-faint' : item.tone === 'red' ? 'text-red' : 'text-ink-2'
                }`}
              >
                <span>{item.label}</span>
                {reason !== null && <span className="text-[10.5px] text-ink-faint">{reason}</span>}
              </button>
            );
          })}
        </div>
      )}
    </span>
  );
}
