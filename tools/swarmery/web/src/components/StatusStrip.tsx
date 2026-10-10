// Sticky status strip (Canvas v3, artboard 2c): the top of every place answers
// three questions in one band — what this is (title + subtitle), what is
// waiting on the operator, and what changed because of them — above one date
// range for the whole place. It replaces the per-page range filters and
// "Analyze now" rows the old pages each carried.
//
// Cells are data, not markup: a label, a value, an optional delta, an optional
// link and a tone (amber = waiting on you, green = good news). The range is a
// segmented single-choice control (radiogroup), the same idiom as the theme
// mode segments. An optional `tabs` slot docks a <Tabs/> bar to the strip's
// bottom edge, where its underline doubles as the strip's own border.

import { useLingui } from '@lingui/react/macro';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';

export type StatusTone = 'amber' | 'green' | 'neutral';

export interface StatusCell {
  label: string;
  value: ReactNode;
  delta?: ReactNode;
  href?: string;
  tone?: StatusTone;
}

export interface StatusRange<V extends string> {
  value: V;
  options: readonly { value: V; label: string }[];
  onChange: (value: V) => void;
}

const CELL_TONE: Record<StatusTone, { box: string; label: string }> = {
  neutral: { box: 'border-line bg-surface', label: 'text-ink-faint' }, // i18n-ignore — Tailwind classes
  green: { box: 'border-line bg-surface', label: 'text-green' }, // i18n-ignore — Tailwind classes
  amber: { box: 'border-amber/40 bg-amber/5', label: 'text-amber' }, // i18n-ignore — Tailwind classes
};

export function StatusStrip<V extends string = string>({
  title,
  subtitle,
  range,
  cells,
  tabs,
}: {
  title: string;
  subtitle?: ReactNode;
  range?: StatusRange<V>;
  cells: readonly StatusCell[];
  /** A <Tabs/> bar docked to the bottom edge. */
  tabs?: ReactNode;
}): JSX.Element {
  return (
    <div
      className={`sticky top-0 z-10 bg-bg px-7 pt-[18px] ${tabs === undefined ? 'border-b border-line pb-4' : ''}`}
    >
      <div className="flex flex-wrap items-baseline gap-3">
        <h1 className="font-display text-[26px] leading-[1.15] font-medium tracking-[-0.01em] text-ink">
          {title}
        </h1>
        {subtitle !== undefined && (
          <span className="font-mono text-[11px] text-ink-faint">{subtitle}</span>
        )}
        {range !== undefined && <RangeControl range={range} />}
      </div>
      {cells.length > 0 && (
        <div className="mt-3 grid grid-cols-[repeat(auto-fit,minmax(180px,1fr))] gap-2.5">
          {cells.map((cell) => (
            <Cell key={cell.label} cell={cell} />
          ))}
        </div>
      )}
      {tabs !== undefined && <div className="mt-3.5">{tabs}</div>}
    </div>
  );
}

function Cell({ cell }: { cell: StatusCell }): JSX.Element {
  const tone = CELL_TONE[cell.tone ?? 'neutral'];
  const body = (
    <>
      <div className={`font-mono text-[9.5px] tracking-[0.12em] uppercase ${tone.label}`}>{cell.label}</div>
      <div className="mt-[3px] text-[12.5px] text-ink-2">
        {cell.value}
        {cell.delta !== undefined && <> {cell.delta}</>}
      </div>
    </>
  );
  const box = `block rounded-[10px] border px-3 py-[9px] ${tone.box}`;
  return cell.href !== undefined ? (
    <Link to={cell.href} className={`${box} transition-colors hover:border-line-strong`}>
      {body}
    </Link>
  ) : (
    <div className={box}>{body}</div>
  );
}

function RangeControl<V extends string>({ range }: { range: StatusRange<V> }): JSX.Element {
  const { t } = useLingui();
  return (
    <div
      role="radiogroup"
      aria-label={t`range`}
      className="ml-auto inline-flex gap-[2px] rounded-lg border border-line-strong bg-bg p-[2px] font-mono text-[10.5px]"
    >
      {range.options.map((opt) => {
        const checked = opt.value === range.value;
        return (
          <button
            key={opt.value}
            type="button"
            role="radio"
            aria-checked={checked}
            onClick={() => {
              if (!checked) range.onChange(opt.value);
            }}
            className={`rounded-md px-2 py-[2px] transition-colors ${
              checked ? 'bg-line text-ink' : 'text-ink-faint hover:text-ink'
            }`}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}
