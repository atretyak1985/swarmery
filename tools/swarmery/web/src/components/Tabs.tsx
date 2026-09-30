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
//
// A tab with an `href` (plans-deep-links phase 2) is a real anchor — a
// react-router <Link role="tab"> — so Cmd/middle-click, "copy link address" and
// the hover preview work. Its plain click is the Link's own navigation (onChange
// is NOT called as well); modified clicks fall through to the browser. The arrow
// keys activate such a tab by clicking its Link, so this component still needs
// no router while no tab carries an href.

import { useCallback, useRef, type KeyboardEvent } from 'react';
import { Link, useSearchParams } from 'react-router-dom';

export interface TabItem<T extends string> {
  id: T;
  label: string;
  /** Mono micro-label after the label — a number or a short phrase. */
  count?: number | string;
  /** Render the tab as a link to this location; its click navigates. */
  href?: string;
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
  const tabRefs = useRef<Partial<Record<T, HTMLElement | null>>>({});
  const current = tabs.findIndex((t) => t.id === value);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    const next = nextTabIndex(e.key, Math.max(current, 0), tabs.length);
    if (next === null) return; // not our key — leave the event alone
    const target = tabs[next];
    if (target === undefined) return;
    e.preventDefault();
    const el = tabRefs.current[target.id];
    el?.focus();
    // A link tab switches the way its click does: through the Link.
    if (target.href !== undefined) el?.click();
    else onChange(target.id);
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
        const ref = (el: HTMLElement | null): void => {
          tabRefs.current[t.id] = el;
        };
        // Roving tabindex: the selected tab, or the first when the value
        // matches none, is the single Tab stop into the list.
        const tabIndex = selected || (current === -1 && i === 0) ? 0 : -1;
        const className = `-mb-px shrink-0 border-b-2 px-3 py-2 text-[12.5px] whitespace-nowrap transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand ${
          selected ? 'border-brand text-ink' : 'border-transparent text-ink-dim hover:text-ink'
        }`;
        const body = (
          <>
            {t.label}
            {t.count !== undefined && (
              <span className="ml-1 font-mono text-[10px] text-ink-faint">{t.count}</span>
            )}
          </>
        );
        if (t.href !== undefined) {
          return (
            <Link
              key={t.id}
              ref={ref}
              to={t.href}
              role="tab"
              aria-selected={selected}
              tabIndex={tabIndex}
              className={className}
            >
              {body}
            </Link>
          );
        }
        return (
          <button
            key={t.id}
            ref={ref}
            type="button"
            role="tab"
            aria-selected={selected}
            tabIndex={tabIndex}
            onClick={() => onChange(t.id)}
            className={className}
          >
            {body}
          </button>
        );
      })}
    </div>
  );
}

/** `search` with `?<name>=` set to `id` — removed for the fallback, so the
 * canonical URL stays clean. Every other param survives. */
function withTabParam(
  search: string | URLSearchParams,
  name: string,
  id: string,
  fallback: string,
): URLSearchParams {
  const next = new URLSearchParams(search);
  if (id === fallback) next.delete(name);
  else next.set(name, id);
  return next;
}

/** The search string (`?…`, or '' when empty) that selects tab `id` — what a
 * link tab points at, built exactly the way useTabParam writes it. */
export function tabParamHref(search: string, name: string, id: string, fallback: string): string {
  const qs = withTabParam(search, name, id, fallback).toString();
  return qs === '' ? '' : `?${qs}`;
}

/**
 * The active tab as a `?<name>=` search param. An absent or unknown value reads
 * as `fallback`; selecting the fallback removes the param so the canonical URL
 * stays clean. Writes replace the history entry by default — a tab switch is
 * not a page; `{ history: 'push' }` makes every switch one Back step (the Plans
 * place's top tabs, plans-deep-links SC-10).
 */
export function useTabParam<T extends string>(
  name: string,
  ids: readonly T[],
  fallback: T,
  opts?: { history?: 'push' | 'replace' },
): [T, (id: T) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get(name);
  const value = raw !== null && (ids as readonly string[]).includes(raw) ? (raw as T) : fallback;
  const replace = (opts?.history ?? 'replace') === 'replace';
  const setValue = useCallback(
    (id: T): void => {
      setParams((prev) => withTabParam(prev, name, id, fallback), { replace });
    },
    [name, fallback, setParams, replace],
  );
  return [value, setValue];
}
