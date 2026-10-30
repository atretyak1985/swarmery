// The strip of tabs under a list's header (design update, phase 9): a filled band
// edge to edge, one tab per slice of the list, each with its count. It sits in the
// header's own padding, so the negative margins carry it to the column's edges and
// down onto the rows. A tab with nothing in it shows no number.
//
// The keys are the shared tablist's (nextTabIndex): arrows and Home/End with a
// roving tabindex, and selection follows focus.

import { useRef, type KeyboardEvent, type ReactNode } from 'react';
import { nextTabIndex } from '../Tabs';

export interface ListTab<T extends string = string> {
  id: T;
  label: string;
  /** Hidden at zero; the design's `n ? String(n) : null`. */
  count?: number | undefined;
  /** Its number stays amber while unselected: it counts what waits on the operator. */
  needs?: boolean;
}

export function ListTabs<T extends string>({
  tabs,
  value,
  onChange,
  ariaLabel,
}: {
  tabs: readonly ListTab<T>[];
  value: T;
  onChange: (id: T) => void;
  ariaLabel: string;
}): JSX.Element {
  const refs = useRef<Partial<Record<T, HTMLButtonElement | null>>>({});
  const current = tabs.findIndex((tab) => tab.id === value);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    const next = nextTabIndex(e.key, Math.max(current, 0), tabs.length);
    if (next === null) return;
    const target = tabs[next];
    if (target === undefined) return;
    e.preventDefault();
    refs.current[target.id]?.focus();
    onChange(target.id);
  };

  return (
    <div
      role="tablist"
      aria-label={ariaLabel}
      onKeyDown={onKeyDown}
      className="-mx-[14px] mt-3 -mb-2 flex flex-wrap gap-0.5 border-y border-line bg-surface2 px-2.5 py-1.5"
    >
      {tabs.map((tab, i) => {
        const on = tab.id === value;
        const count = tab.count ?? 0;
        return (
          <button
            key={tab.id}
            ref={(el) => {
              refs.current[tab.id] = el;
            }}
            type="button"
            role="tab"
            aria-selected={on}
            tabIndex={on || (current === -1 && i === 0) ? 0 : -1}
            onClick={() => onChange(tab.id)}
            className={`inline-flex cursor-pointer items-center gap-[5px] rounded-[6px] border px-[9px] py-[3px] text-[12px] leading-[normal] font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand ${
              on
                ? 'border-line-strong bg-surface text-ink shadow-[0_1px_0_rgba(0,0,0,0.25)]'
                : 'border-transparent bg-transparent text-ink-dim hover:text-ink'
            }`}
          >
            {tab.label}
            {count > 0 && (
              <span className={`font-mono text-[10px] ${on || tab.needs === true ? 'text-brand' : 'text-ink-faint'}`}>
                {count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}

/** What a tabbed list says when the tab holds nothing. */
export function ListEmpty({ children }: { children: ReactNode }): JSX.Element {
  return <div className="px-[14px] py-10 text-[12.5px] leading-[1.55] text-ink-dim">{children}</div>;
}
