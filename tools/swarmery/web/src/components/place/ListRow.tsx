// The one list row (handoff §1), shared by every place: a state glyph, a title
// with an optional mono tag, a one-line summary, and on the right the age and an
// optional #PR. Selected is a 2px brand edge on the --color-sel fill.
//
// Needs you (artboard 3b on v4, plan D-9) names the kind as a word instead of a
// glyph: a mono `kicker` line above the title, and no glyph column at all.

import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useTheme } from '../../lib/theme';

/** Summary colour carries state only: amber-ish needs-you, red failed. */
export type SummaryTone = 'default' | 'needs' | 'failed';

const SUMMARY_TONE: Record<SummaryTone, string> = {
  default: 'text-ink-dim',
  needs: 'text-ink-2',
  failed: 'text-red',
};

export interface ListRowProps {
  /** Usually a StateGlyph. Without one the row drops the glyph column. */
  glyph?: ReactNode;
  /** A mono line above the title that names what the row is (`Awaiting reply`). */
  kicker?: string | undefined;
  /** `alarm` paints the kicker red and bold: the one kind that may be red. */
  kickerTone?: 'default' | 'alarm';
  title: string;
  /** Project, or "plan run": grey mono text, never a pill. */
  tag?: string | undefined;
  /** One warning after the tag (`⚠ context`), amber mono text. Most rows have none. */
  flag?: string | undefined;
  summary?: string | undefined;
  summaryTone?: SummaryTone;
  age?: string | undefined;
  pr?: string | undefined;
  /** Tailwind text class for the #PR, in its status colour. */
  prTone?: string;
  selected?: boolean;
  /** Overrides the Settings → Appearance preference, which is the default. */
  compact?: boolean;
  /** A session with no title yet: the placeholder reads as one. */
  untitled?: boolean;
  /** Selection lives in the URL: the row is a link. */
  to?: string;
  onSelect?: () => void;
}

/** The row's grid and padding, shared with anything that must stand in the list
 * at a row's height and on its columns (the plan-run card's header). */
export function listRowFrame(compact: boolean, glyph = true): string {
  const columns = glyph ? 'grid-cols-[18px_minmax(0,1fr)_auto]' : 'grid-cols-[minmax(0,1fr)_auto]';
  return `grid w-full cursor-pointer ${columns} items-start gap-[10px] border-l-2 pr-[14px] pl-3 text-left leading-[normal] transition-colors ${
    compact ? 'py-1.5' : 'py-[9px]'
  }`;
}

export function ListRow({
  glyph,
  kicker,
  kickerTone = 'default',
  title,
  tag,
  flag,
  summary,
  summaryTone = 'default',
  age,
  pr,
  prTone = 'text-ink-faint',
  selected = false,
  compact,
  untitled = false,
  to,
  onSelect,
}: ListRowProps): JSX.Element {
  const { compact: compactRows } = useTheme();
  const className = `${listRowFrame(compact ?? compactRows, glyph !== undefined)} ${selected ? 'border-brand bg-sel' : 'border-transparent hover:bg-surface'}`;

  const body = (
    <>
      {glyph !== undefined && <span className="mt-px">{glyph}</span>}
      <span className="block min-w-0">
        {kicker !== undefined && (
          <span
            data-testid="row-kicker"
            className={`mb-[3px] block truncate font-mono text-[10.5px] ${
              kickerTone === 'alarm' ? 'font-bold text-red' : 'text-ink-faint'
            }`}
          >
            {kicker}
          </span>
        )}
        <span className="flex min-w-0 items-baseline gap-1.5">
          <span
            className={`min-w-0 truncate text-[13px] font-medium ${untitled ? 'text-ink-faint italic' : 'text-ink'}`}
          >
            {title}
          </span>
          {tag !== undefined && <span className="shrink-0 font-mono text-[10px] text-ink-faint">{tag}</span>}
          {flag !== undefined && (
            <span data-testid="row-flag" className="shrink-0 font-mono text-[10px] whitespace-nowrap text-amber">
              {flag}
            </span>
          )}
        </span>
        {summary !== undefined && (
          <span className={`mt-0.5 block truncate text-[12px] ${SUMMARY_TONE[summaryTone]}`}>{summary}</span>
        )}
      </span>
      <span className="flex flex-col items-end gap-[3px] font-mono text-[10.5px] whitespace-nowrap text-ink-faint">
        {age !== undefined && <span>{age}</span>}
        {pr !== undefined && <span className={prTone}>{pr}</span>}
      </span>
    </>
  );

  if (to !== undefined) {
    return (
      <Link to={to} onClick={onSelect} aria-current={selected ? 'true' : undefined} className={className}>
        {body}
      </Link>
    );
  }
  return (
    <button type="button" onClick={onSelect} aria-current={selected ? 'true' : undefined} className={className}>
      {body}
    </button>
  );
}
