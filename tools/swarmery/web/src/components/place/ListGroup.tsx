// A group inside a list (handoff §1): a caret, a mono uppercase label and its
// count. Sessions, Needs you, Inbox and Today keep every group open, and their head
// keeps the caret's width so labels line up. Docs and Settings run them as an
// accordion — one open, the first by default, and the counts always visible.
// `useAccordion` is that mode: one key open, and clicking the open group leaves
// it open, as the design does, because a place always shows one group. A tab of a
// list hides the head: the tab already names the group.

import { useCallback, useState, type ReactNode } from 'react';

export type GroupTone = 'default' | 'amber' | 'green' | 'red' | 'blue' | 'purple';

const LABEL_TONE: Record<GroupTone, string> = {
  default: 'text-ink-faint',
  amber: 'text-amber',
  green: 'text-green',
  red: 'text-red',
  blue: 'text-blue',
  purple: 'text-purple',
};

export function ListGroup({
  label,
  count,
  tone = 'default',
  open = true,
  onToggle,
  hideHead = false,
  children,
}: {
  label: string;
  count: number;
  tone?: GroupTone;
  open?: boolean;
  /** Omitted for an always-open group: the header is then plain, not a control. */
  onToggle?: () => void;
  /** Under a list tab: the rows alone. */
  hideHead?: boolean;
  children?: ReactNode;
}): JSX.Element {
  const header = (
    <>
      <span aria-hidden="true" className="w-2.5 shrink-0 font-mono text-[10px] text-ink-faint">
        {onToggle === undefined ? '' : open ? '▾' : '▸'}
      </span>
      <span className={`font-mono text-[10px] tracking-[0.12em] uppercase ${LABEL_TONE[tone]}`}>{label}</span>
      <span className="font-mono text-[10px] text-ink-faint">{count}</span>
    </>
  );
  const pad = onToggle === undefined ? 'pt-[10px] pb-1' : open ? 'pt-3 pb-1' : 'py-[7px]';
  const headerClass = `flex w-full items-center gap-2 pr-[14px] pl-2.5 text-left leading-[normal] ${pad}`;
  return (
    <div>
      {hideHead ? null : onToggle === undefined ? (
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
