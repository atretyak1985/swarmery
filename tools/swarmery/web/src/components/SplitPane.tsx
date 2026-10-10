// Master/detail triage layout (Canvas v3, artboard 2b — the Inbox): a fixed
// 380px list on the left that scrolls on its own, the selected item's detail on
// the right. Built for queues the operator works through from the keyboard:
//
//   - j / k move the selection down / up the list;
//   - `keymap` binds extra single keys to the selected item (e approve,
//     x deny, s skip in the Inbox);
//   - every key is ignored while focus is in an input, textarea, select or
//     contenteditable, and when a modifier is held (⌘K and friends pass through);
//   - a footer hint row ("j/k move · e approve · …") is generated from the
//     keymap labels, so the hints can never drift from the bindings.
//
// The listener is window-level on purpose: a triage page is keyboard-driven
// whether or not focus happens to sit inside the list.

import { Trans } from '@lingui/react/macro';
import { useEffect, useRef, type ReactNode } from 'react';

export interface SplitPaneKey<T> {
  /** A single character, matched case-sensitively against KeyboardEvent.key. */
  key: string;
  /** Verb shown in the footer hint ("approve"). */
  label: string;
  run: (item: T) => void;
}

/** True when a key press belongs to a text field, not to the pane. */
function typingIn(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName);
}

export function SplitPane<T>({
  items,
  getId,
  renderRow,
  renderDetail,
  selectedId,
  onSelect,
  keymap = [],
  ariaLabel,
}: {
  items: readonly T[];
  getId: (item: T) => string;
  renderRow: (item: T, selected: boolean) => ReactNode;
  renderDetail: (item: T) => ReactNode;
  selectedId: string | null;
  onSelect: (id: string) => void;
  keymap?: readonly SplitPaneKey<T>[];
  /** Accessible name of the list. */
  ariaLabel: string;
}): JSX.Element {
  const index = selectedId === null ? -1 : items.findIndex((it) => getId(it) === selectedId);
  const selected = index === -1 ? undefined : items[index];
  const rowRefs = useRef(new Map<string, HTMLDivElement>());

  // Latest state without re-binding the window listener on every render.
  const state = useRef({ items, index, selected, getId, onSelect, keymap });
  useEffect(() => {
    state.current = { items, index, selected, getId, onSelect, keymap };
  });

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
      if (typingIn(e.target)) return;
      const s = state.current;
      if (e.key === 'j' || e.key === 'k') {
        if (s.items.length === 0) return;
        const next =
          s.index === -1
            ? 0
            : Math.min(Math.max(s.index + (e.key === 'j' ? 1 : -1), 0), s.items.length - 1);
        const item = s.items[next];
        if (item === undefined) return;
        e.preventDefault();
        const id = s.getId(item);
        s.onSelect(id);
        // jsdom has no scrollIntoView — optional call keeps tests honest.
        rowRefs.current.get(id)?.scrollIntoView?.({ block: 'nearest' });
        return;
      }
      const binding = s.keymap.find((b) => b.key === e.key);
      if (binding === undefined || s.selected === undefined) return;
      e.preventDefault();
      binding.run(s.selected);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <div className="grid h-full min-h-0 grid-cols-[380px_minmax(0,1fr)]">
      <div className="flex min-h-0 flex-col border-r border-line">
        <div role="listbox" aria-label={ariaLabel} className="min-h-0 flex-1 overflow-y-auto">
          {items.map((item) => {
            const id = getId(item);
            const isSelected = id === selectedId;
            return (
              <div
                key={id}
                ref={(el) => {
                  if (el === null) rowRefs.current.delete(id);
                  else rowRefs.current.set(id, el);
                }}
                role="option"
                aria-selected={isSelected}
                tabIndex={isSelected || (index === -1 && item === items[0]) ? 0 : -1}
                onClick={() => onSelect(id)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    onSelect(id);
                  }
                }}
                className={`cursor-pointer border-l-2 transition-colors ${
                  isSelected ? 'border-brand bg-surface2' : 'border-transparent hover:bg-surface2/50'
                }`}
              >
                {renderRow(item, isSelected)}
              </div>
            );
          })}
        </div>
        <div className="flex flex-wrap gap-3 border-t border-line px-3.5 py-2 font-mono text-[10px] text-ink-faint">
          <span>
            <Trans>j/k move</Trans>
          </span>
          {keymap.map((b) => (
            <span key={b.key}>
              {b.key} {b.label}
            </span>
          ))}
        </div>
      </div>
      <div className="min-h-0 min-w-0 overflow-y-auto">
        {selected !== undefined && renderDetail(selected)}
      </div>
    </div>
  );
}
