// Shared tab bar (Canvas v3, artboard 2c): tabs INSIDE a place, each with an
// optional count ("Agents 7", "Advisor 4 open"). Later phases compose it into
// Health, Learning, Knowledge, Plans and System instead of hand-rolling a
// tablist per page.
//
// The full WAI-ARIA tabs pattern: role="tablist"/"tab" + aria-selected, a
// roving tabindex (only the selected tab is tabbable), ArrowLeft/ArrowRight
// with wraparound and Home/End. Selection follows focus (automatic activation),
// so the arrow keys also move DOM focus — otherwise the roving tabindex would
// strand it on a tab that just became tabIndex=-1.
//
// useTabParam mirrors the active tab to `?<name>=` so a redirect from a retired
// page (e.g. /retro → /health?tab=advisor) can land on a specific tab.

import { useCallback, useRef, type KeyboardEvent } from 'react';
import { useSearchParams } from 'react-router-dom';

export interface TabItem<T extends string> {
  id: T;
  label: string;
  /** Mono micro-label after the label — a number or a short phrase. */
  count?: number | string;
}

/** Next selected index for a tablist key, or null when the key is not ours. */
export function nextTabIndex(key: string, current: number, length: number): number | null {
  if (length === 0) return null;
  switch (key) {
    case 'ArrowRight':
      return (current + 1) % length;
    case 'ArrowLeft':
      return (current - 1 + length) % length;
    case 'Home':
      return 0;
    case 'End':
      return length - 1;
    default:
      return null;
  }
}

export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
  ariaLabel,
}: {
  tabs: readonly TabItem<T>[];
  value: T;
  onChange: (id: T) => void;
  ariaLabel: string;
}): JSX.Element {
  const btnRefs = useRef<Partial<Record<T, HTMLButtonElement | null>>>({});
  const current = tabs.findIndex((t) => t.id === value);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    const next = nextTabIndex(e.key, Math.max(current, 0), tabs.length);
    if (next === null) return; // not our key — leave the event alone
    const target = tabs[next];
    if (target === undefined) return;
    e.preventDefault();
    onChange(target.id);
    btnRefs.current[target.id]?.focus();
  };

  return (
    <div
      role="tablist"
      aria-label={ariaLabel}
      onKeyDown={onKeyDown}
      className="flex gap-[2px] overflow-x-auto border-b border-line [-webkit-overflow-scrolling:touch]"
    >
      {tabs.map((t, i) => {
        const selected = t.id === value;
        return (
          <button
            key={t.id}
            ref={(el) => {
              btnRefs.current[t.id] = el;
            }}
            type="button"
            role="tab"
            aria-selected={selected}
            // Roving tabindex: the selected tab, or the first when the value
            // matches none, is the single Tab stop into the list.
            tabIndex={selected || (current === -1 && i === 0) ? 0 : -1}
            onClick={() => onChange(t.id)}
            className={`-mb-px shrink-0 border-b-2 px-3 py-2 text-[12.5px] whitespace-nowrap transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand ${
              selected ? 'border-brand text-ink' : 'border-transparent text-ink-dim hover:text-ink'
            }`}
          >
            {t.label}
            {t.count !== undefined && (
              <span className="ml-1 font-mono text-[10px] text-ink-faint">{t.count}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

/**
 * The active tab as a `?<name>=` search param. An absent or unknown value reads
 * as `fallback`; selecting the fallback removes the param so the canonical URL
 * stays clean. Writes replace the history entry — a tab switch is not a page.
 */
export function useTabParam<T extends string>(
  name: string,
  ids: readonly T[],
  fallback: T,
): [T, (id: T) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get(name);
  const value = raw !== null && (ids as readonly string[]).includes(raw) ? (raw as T) : fallback;
  const setValue = useCallback(
    (id: T): void => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id === fallback) next.delete(name);
          else next.set(name, id);
          return next;
        },
        { replace: true },
      );
    },
    [name, fallback, setParams],
  );
  return [value, setValue];
}
