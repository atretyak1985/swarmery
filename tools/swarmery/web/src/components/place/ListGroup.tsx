// A group inside a list (handoff §1): a mono uppercase label, its count, a
// hairline and a caret. Sessions, Needs you, Inbox, Plans and Today keep every
// group open; Health, Learning, Knowledge, System, Docs and Settings run them as
// an accordion — one open, the first by default, and the counts always visible.
// `useAccordion` is that mode: one key open, and clicking the open group leaves
// it open, as the design does, because a place always shows one group.

import { useCallback, useState, type ReactNode } from 'react';

export type GroupTone = 'default' | 'amber' | 'green' | 'red';

const LABEL_TONE: Record<GroupTone, string> = {
  default: 'text-ink-faint',
  amber: 'text-amber',
  green: 'text-green',
  red: 'text-red',
};

export function ListGroup({
  label,
  count,
  tone = 'default',
  open = true,
  onToggle,
  children,
}: {
  label: string;
  count: number;
  tone?: GroupTone;
  open?: boolean;
  /** Omitted for an always-open group: the header is then plain, not a control. */
  onToggle?: () => void;
  children?: ReactNode;
}): JSX.Element {
  const header = (
    <>
      <span className={`font-mono text-[10px] tracking-[0.12em] uppercase ${LABEL_TONE[tone]}`}>{label}</span>
      <span className="font-mono text-[10px] text-ink-faint">{count}</span>
      <span aria-hidden="true" className="h-px flex-1 bg-line" />
      <span aria-hidden="true" className="font-mono text-[10px] text-ink-faint">
        {open ? '▾' : '▸'}
      </span>
    </>
  );
  const headerClass =
    'flex w-full items-center gap-2 border-t border-line pt-[10px] pr-[14px] pb-1.5 pl-[14px] leading-[normal]';
  return (
    <div>
      {onToggle === undefined ? (
        <div className={headerClass}>{header}</div>
      ) : (
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className={`${headerClass} cursor-pointer select-none transition-colors hover:bg-surface`}
        >
          {header}
        </button>
      )}
      {open && children}
    </div>
  );
}

export interface Accordion {
  isOpen: (key: string) => boolean;
  /** Opens `key` and so closes the one that was open. */
  open: (key: string) => void;
}

/** One open group among `keys`; the first is open until the operator picks. A key
 * that left the list falls back to the first, so a place never shows none open. */
export function useAccordion(keys: readonly string[]): Accordion {
  const [picked, setPicked] = useState<string | null>(null);
  const current = picked !== null && keys.includes(picked) ? picked : (keys[0] ?? null);
  const isOpen = useCallback((key: string): boolean => key === current, [current]);
  const open = useCallback((key: string): void => setPicked(key), []);
  return { isOpen, open };
}
