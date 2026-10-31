// The global keyboard (handoff §10): 1–5 places, t theme, j/k or ↑/↓ list rows,
// e the selected row's main action, x its secondary one, Esc close, and [ ] \ the
// panels: sidebar, list, session rail. ⌘K is not here: the Sidebar owns it, and it
// must fire from inside a field.
//
// Several consumers run at once (the shell owns places and theme, a ListPane the
// row keys), so each one registers only the keys it handles and a key nobody
// handles keeps its browser default. Nothing fires while the operator types or
// while a modal dialog owns the keyboard — both would turn a typed letter into
// an action, and `e` and `x` are real actions.

import { useEffect, useRef } from 'react';

export interface GlobalKeyHandlers {
  /** 1–5, as a zero-based index into the main places. */
  onPlace?: (index: number) => void;
  onTheme?: () => void;
  onNext?: () => void;
  onPrev?: () => void;
  onPrimary?: () => void;
  onSecondary?: () => void;
  onEscape?: () => void;
  onPanelNav?: () => void;
  onPanelList?: () => void;
  onPanelRail?: () => void;
}

const FIELD_TAGS: ReadonlySet<string> = new Set(['INPUT', 'TEXTAREA', 'SELECT']);

/** Is the operator typing into `target`? The terminal is a textarea too. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  if (FIELD_TAGS.has(target.tagName)) return true;
  const editable = target.closest('[contenteditable]');
  return editable !== null && editable.getAttribute('contenteditable') !== 'false';
}

function keyHandler(key: string, h: GlobalKeyHandlers): (() => void) | undefined {
  switch (key) {
    case 't':
      return h.onTheme;
    case 'j':
    case 'ArrowDown':
      return h.onNext;
    case 'k':
    case 'ArrowUp':
      return h.onPrev;
    case 'e':
      return h.onPrimary;
    case 'x':
      return h.onSecondary;
    case 'Escape':
      return h.onEscape;
    case '[':
      return h.onPanelNav;
    case ']':
      return h.onPanelList;
    case '\\':
      return h.onPanelRail;
    default: {
      const onPlace = h.onPlace;
      if (onPlace === undefined || !/^[1-5]$/.test(key)) return undefined;
      return () => onPlace(Number(key) - 1);
    }
  }
}

/** Runs the handler `e` maps to; true when it did (the caller's key is spent). */
export function dispatchGlobalKey(e: KeyboardEvent, handlers: GlobalKeyHandlers): boolean {
  if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return false;
  if (isTypingTarget(e.target)) return false;
  if (document.querySelector('[aria-modal="true"]') !== null) return false;
  const run = keyHandler(e.key, handlers);
  if (run === undefined) return false;
  e.preventDefault();
  run();
  return true;
}

/** The key one step from `current` in `keys`; clamps at both ends, and a key that
 * is not in the list counts as the first row. Undefined for an empty list. */
export function stepKey(keys: readonly string[], current: string | null, dir: 1 | -1): string | undefined {
  if (keys.length === 0) return undefined;
  const at = current === null ? -1 : keys.indexOf(current);
  const from = at === -1 ? 0 : at + dir;
  return keys[Math.min(Math.max(from, 0), keys.length - 1)];
}

export function useGlobalKeys(handlers: GlobalKeyHandlers): void {
  // Read through a ref: callers pass fresh closures every render, and rebinding
  // the window listener for each one would be pointless churn.
  const latest = useRef(handlers);
  latest.current = handlers;
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent): void => {
      dispatchGlobalKey(e, latest.current);
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);
}
