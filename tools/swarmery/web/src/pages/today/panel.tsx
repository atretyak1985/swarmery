// The two pieces Today's detail repeats: a section head (a mono label, a hairline,
// a link on the right) and the bordered row under it — a glyph, a title, one
// sentence and an age. Waiting on you and Live now are the same row (handoff §3).

import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';

/** Colour here is state: the accent waits on you, green is live. */
export type SectionTone = 'default' | 'brand' | 'green';

const SECTION_TONE: Record<SectionTone, string> = {
  default: 'text-ink-faint',
  brand: 'text-brand',
  green: 'text-green',
};

export function SectionHead({
  label,
  tone = 'default',
  children,
}: {
  label: string;
  tone?: SectionTone;
  children?: ReactNode;
}): JSX.Element {
  return (
    <div className="flex items-baseline gap-2">
      <h3
        className={`m-0 font-mono text-[10px] leading-[normal] font-normal tracking-[0.12em] uppercase ${SECTION_TONE[tone]}`}
      >
        {label}
      </h3>
      <span aria-hidden="true" className="h-px flex-1 bg-line" />
      {children}
    </div>
  );
}

/** The mono link at a section head's right end. */
export function SectionLink({ to, children }: { to: string; children: ReactNode }): JSX.Element {
  return (
    <Link
      to={to}
      className="font-mono text-[10.5px] leading-[normal] text-ink-faint transition-colors hover:text-brand"
    >
      {children}
    </Link>
  );
}

/** Nothing to list: the dashed box the two panels share. */
export function PanelEmpty({ children }: { children: ReactNode }): JSX.Element {
  return (
    <p className="m-0 rounded-lg border border-dashed border-line p-3 text-[12.5px] leading-[normal] text-ink-dim">
      {children}
    </p>
  );
}

export function PanelList({ children }: { children: ReactNode }): JSX.Element {
  return <ul className="m-0 flex list-none flex-col gap-1 p-0">{children}</ul>;
}

export function PanelRow({
  testId,
  to,
  glyph,
  title,
  summary,
  age,
  action,
}: {
  testId: string;
  to: string;
  glyph: ReactNode;
  title: string;
  summary: string;
  age: string;
  /** Shown over the age on hover or focus: a row stays one link at rest. */
  action?: ReactNode;
}): JSX.Element {
  return (
    <li className="group relative">
      <Link
        to={to}
        data-testid={testId}
        className="grid grid-cols-[18px_minmax(0,1fr)_auto] items-center gap-[10px] rounded-lg border border-line px-[10px] py-2 leading-[normal] transition-colors hover:border-line-strong"
      >
        {glyph}
        <span className="block min-w-0">
          <span className="block truncate text-[12.5px] font-medium text-ink">{title}</span>
          <span className="mt-px block truncate text-[11.5px] text-ink-dim">{summary}</span>
        </span>
        <span
          className={`font-mono text-[10.5px] whitespace-nowrap text-ink-faint ${
            action === undefined ? '' : 'group-focus-within:invisible group-hover:invisible'
          }`}
        >
          {age}
        </span>
      </Link>
      {action !== undefined && (
        <span className="absolute top-1/2 right-[10px] -translate-y-1/2 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100">
          {action}
        </span>
      )}
    </li>
  );
}
