// The detail page of an accordion place (handoff §9): one page of blocks that
// switch on by data — eyebrow · sentence title · lead · numbers · horizontal bars ·
// trend · key-value grid · paragraphs · pre · rows · toggles · actions with a link
// on the right. Whatever the blocks cannot express (an existing chart, an editor)
// comes in as `children` under them, so a screen moves here without losing a panel.
//
// `PagePlace` is the rest of such a place: the list column run as an accordion,
// and `usePlaceParams` keeps the group and the row in `?tab=`/`?item=`, so an old
// tab link still opens the right group. Health, Learning, Knowledge, System, Docs
// and Settings all have this one shape.

import { useLingui } from '@lingui/react/macro';
import { Fragment, useCallback, useMemo, useState, type ReactNode } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { ListGroup, type GroupTone } from './ListGroup';
import { ListPane } from './ListPane';
import { ListRow } from './ListRow';
import { NumbersStrip, type NumberItem } from './NumbersStrip';
import { PlaceLayout, type NarrowPane } from './PlaceLayout';

/** Colour carries state and alarm only (handoff §4): faint is the design's --dim. */
export type PageTone = 'default' | 'soft' | 'faint' | 'brand' | 'amber' | 'green' | 'red' | 'blue' | 'purple';

const TEXT_TONE: Record<PageTone, string> = {
  default: 'text-ink',
  soft: 'text-ink-2',
  faint: 'text-ink-faint',
  brand: 'text-brand',
  amber: 'text-amber',
  green: 'text-green',
  red: 'text-red',
  blue: 'text-blue',
  purple: 'text-purple',
};

const FILL_TONE: Record<PageTone, string> = {
  default: 'bg-ink',
  soft: 'bg-ink-2',
  faint: 'bg-ink-faint',
  brand: 'bg-brand',
  amber: 'bg-amber',
  green: 'bg-green',
  red: 'bg-red',
  blue: 'bg-blue',
  purple: 'bg-purple',
};

const BLOCK_TITLE = 'font-mono text-[10px] leading-[normal] tracking-[0.12em] text-ink-faint uppercase';

export interface PageBar {
  label: string;
  /** 0–100; clamped. */
  percent: number;
  value: string;
  tone: PageTone;
}

export interface PageKv {
  k: string;
  v: string;
  tone?: PageTone;
}

export interface PageRow {
  key: string;
  glyph: string;
  glyphTone?: PageTone;
  label: string;
  sub?: string;
  right?: string;
  rightTone?: PageTone;
  age?: string;
  /** A link when set; otherwise a button when `onClick` is, else plain text. */
  to?: string;
  onClick?: () => void;
  /** Accessible name when the visible text is not enough. */
  ariaLabel?: string;
}

export type PageToggle =
  | { key: string; label: string; hint?: string; on: boolean; onChange: (next: boolean) => void; disabled?: boolean }
  | {
      key: string;
      label: string;
      hint?: string;
      options: readonly { value: string; label: string }[];
      value: string;
      onPick: (next: string) => void;
    };

export interface PageAction {
  key?: string;
  label: string;
  /** Set for the primary action: its border and text take the tone. */
  tone?: PageTone;
  onClick?: () => void;
  to?: string;
  disabled?: boolean;
  /** Tooltip, in the app's data-tip form. */
  tip?: string;
  /** A control with behaviour of its own (a picker), in place of the button. */
  node?: ReactNode;
}

export interface PageLink {
  label: string;
  to?: string;
  onClick?: () => void;
}

export interface PageDetailProps {
  kind: string;
  kindTone?: PageTone;
  meta?: string;
  title: string;
  lead?: string | null;
  stats?: readonly NumberItem[];
  barsTitle?: string;
  bars?: readonly PageBar[];
  trendTitle?: string;
  trendNote?: string;
  /** 8–14 values, oldest first; the last one takes `trendTone`. */
  trend?: readonly number[];
  trendTone?: PageTone;
  kv?: readonly PageKv[];
  paras?: readonly string[];
  code?: string | null;
  rowsTitle?: string;
  rows?: readonly PageRow[];
  toggles?: readonly PageToggle[];
  actions?: readonly PageAction[];
  link?: PageLink;
  /** Sections with data the blocks cannot express; they keep their own shape. */
  children?: ReactNode;
}

const ACTION_BASE =
  'rounded-[7px] border bg-transparent px-3 py-1.5 font-mono text-[11.5px] leading-[normal] whitespace-nowrap transition-colors disabled:opacity-50';

/** Exported for a control that owns its own behaviour but sits in the actions row. */
export function actionClass(tone: PageTone | undefined): string {
  if (tone === undefined) return `${ACTION_BASE} cursor-pointer border-line-strong text-ink-2 hover:text-ink`;
  const fill = tone === 'brand' ? 'bg-brand-bg' : '';
  return `${ACTION_BASE} cursor-pointer font-bold ${TEXT_TONE[tone]} border-current ${fill}`;
}

function BarsBlock({ title, bars }: { title: string; bars: readonly PageBar[] }): JSX.Element {
  return (
    <div className="mt-[18px] max-w-[640px]">
      <div className={BLOCK_TITLE}>{title}</div>
      <div className="mt-2 flex flex-col gap-[7px]">
        {bars.map((bar) => (
          <div
            key={bar.label}
            className="grid grid-cols-[minmax(120px,0.8fr)_minmax(0,2fr)_80px] items-center gap-3 text-[12.5px]"
          >
            <span className="truncate text-ink-2">{bar.label}</span>
            <span aria-hidden="true" className="block h-1.5 overflow-hidden rounded-[3px] bg-line">
              <span
                className={`block h-full ${FILL_TONE[bar.tone]}`}
                style={{ width: `${String(Math.min(Math.max(bar.percent, 0), 100))}%` }}
              />
            </span>
            <span className={`text-right font-mono text-[11px] ${TEXT_TONE[bar.tone]}`}>{bar.value}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function TrendBlock({
  title,
  note,
  values,
  tone,
}: {
  title: string;
  note?: string | undefined;
  values: readonly number[];
  tone: PageTone;
}): JSX.Element {
  const top = Math.max(...values, 1);
  return (
    <div className="mt-[18px] max-w-[640px]">
      <div className={`flex justify-between ${BLOCK_TITLE}`}>
        <span>{title}</span>
        {note !== undefined && <span className="tracking-normal normal-case">{note}</span>}
      </div>
      <div className="mt-2 flex h-14 items-end gap-[3px] border-b border-line">
        {values.map((value, index) => (
          <div
            // Position is the identity: the series is ordered and never reorders.
            // eslint-disable-next-line react/no-array-index-key
            key={index}
            title={String(value)}
            className={`flex-1 rounded-t-[2px] ${index === values.length - 1 ? FILL_TONE[tone] : 'bg-line-strong'}`}
            style={{ height: `${String(Math.max(6, Math.round((value / top) * 100)))}%` }}
          />
        ))}
      </div>
    </div>
  );
}

function KvBlock({ rows }: { rows: readonly PageKv[] }): JSX.Element {
  return (
    <div className="mt-[18px] grid max-w-[720px] grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-x-6 gap-y-2">
      {rows.map((row) => (
        <div key={row.k} className="flex flex-col gap-0.5 border-b border-line py-1.5">
          <span className="font-mono text-[9.5px] leading-[normal] tracking-[0.1em] text-ink-faint uppercase">
            {row.k}
          </span>
          <span className={`font-mono text-[12px] leading-normal ${TEXT_TONE[row.tone ?? 'soft']}`}>{row.v}</span>
        </div>
      ))}
    </div>
  );
}

function RowBody({ row }: { row: PageRow }): JSX.Element {
  return (
    <>
      <span className={`text-center font-mono text-[11px] ${TEXT_TONE[row.glyphTone ?? 'faint']}`}>{row.glyph}</span>
      <span className="truncate text-[12.5px] text-ink-2">
        {row.label}
        {row.sub !== undefined && <span className="text-ink-dim"> · {row.sub}</span>}
      </span>
      <span className={`font-mono text-[10.5px] ${TEXT_TONE[row.rightTone ?? 'soft']}`}>{row.right}</span>
      <span className="font-mono text-[10.5px] text-ink-faint">{row.age}</span>
    </>
  );
}

// i18n-ignore — a class list, not copy
const ROW_CLASS = 'grid w-full grid-cols-[18px_minmax(0,1fr)_auto_auto] items-center gap-3 border-b border-line py-2';

function RowsBlock({ title, rows }: { title: string; rows: readonly PageRow[] }): JSX.Element {
  return (
    <div className="mt-[18px] max-w-[760px]">
      <div className={BLOCK_TITLE}>{title}</div>
      <div className="mt-1.5">
        {rows.map((row) =>
          row.to !== undefined ? (
            <Link key={row.key} to={row.to} aria-label={row.ariaLabel} className={`${ROW_CLASS} hover:text-brand`}>
              <RowBody row={row} />
            </Link>
          ) : row.onClick !== undefined ? (
            <button
              key={row.key}
              type="button"
              onClick={row.onClick}
              aria-label={row.ariaLabel}
              className={`${ROW_CLASS} cursor-pointer text-left hover:text-brand`}
            >
              <RowBody row={row} />
            </button>
          ) : (
            <div key={row.key} className={ROW_CLASS}>
              <RowBody row={row} />
            </div>
          ),
        )}
      </div>
    </div>
  );
}

/** The design's segmented control, a radio group for one exclusive setting. */
export function Segmented({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: readonly { value: string; label: string }[];
  value: string;
  onChange: (next: string) => void;
}): JSX.Element {
  return (
    <span role="radiogroup" aria-label={label} className="inline-flex gap-0.5 rounded-[7px] border border-line p-0.5">
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="radio"
          aria-checked={option.value === value}
          onClick={() => onChange(option.value)}
          className={`cursor-pointer rounded-[5px] border-none px-[9px] py-[3px] font-mono text-[10.5px] leading-[normal] ${
            option.value === value ? 'bg-surface2 text-ink' : 'bg-transparent text-ink-faint hover:text-ink'
          }`}
        >
          {option.label}
        </button>
      ))}
    </span>
  );
}

function TogglesBlock({ rows }: { rows: readonly PageToggle[] }): JSX.Element {
  return (
    <div className="mt-[18px] flex max-w-[640px] flex-col">
      {rows.map((row) => (
        <div
          key={row.key}
          className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-4 border-b border-line py-[11px]"
        >
          <div>
            <div className="text-[13px] font-medium text-ink">{row.label}</div>
            {row.hint !== undefined && <div className="mt-0.5 text-[12px] leading-[1.45] text-ink-dim">{row.hint}</div>}
          </div>
          {'options' in row ? (
            <Segmented label={row.label} options={row.options} value={row.value} onChange={row.onPick} />
          ) : (
            <button
              type="button"
              role="switch"
              aria-checked={row.on}
              aria-label={row.label}
              disabled={row.disabled}
              onClick={() => row.onChange(!row.on)}
              className={`relative h-5 w-[34px] cursor-pointer rounded-full border-none p-0 disabled:opacity-50 ${
                row.on ? 'bg-brand' : 'bg-line-strong'
              }`}
            >
              <span
                className={`absolute top-0.5 size-4 rounded-full transition-[left] ${
                  row.on ? 'left-4 bg-white' : 'left-0.5 bg-surface'
                }`}
              />
            </button>
          )}
        </div>
      ))}
    </div>
  );
}

function ActionButton({ action }: { action: PageAction }): JSX.Element {
  if (action.to !== undefined) {
    return (
      <Link to={action.to} data-tip={action.tip} className={actionClass(action.tone)}>
        {action.label}
      </Link>
    );
  }
  return (
    <button
      type="button"
      disabled={action.disabled}
      onClick={action.onClick}
      data-tip={action.tip}
      className={actionClass(action.tone)}
    >
      {action.label}
    </button>
  );
}

function ActionsBlock({ actions, link }: { actions: readonly PageAction[]; link?: PageLink | undefined }): JSX.Element {
  const linkClass = 'ml-auto cursor-pointer font-mono text-[10.5px] text-ink-faint hover:text-brand';
  return (
    <div className="mt-[22px] flex flex-wrap items-center gap-2">
      {actions.map((action) => (
        <Fragment key={action.key ?? action.label}>{action.node ?? <ActionButton action={action} />}</Fragment>
      ))}
      {link !== undefined &&
        (link.to !== undefined ? (
          <Link to={link.to} className={linkClass}>
            {link.label}
          </Link>
        ) : (
          <button type="button" onClick={link.onClick} className={linkClass}>
            {link.label}
          </button>
        ))}
    </div>
  );
}

export function PageDetail({
  kind,
  kindTone = 'faint',
  meta,
  title,
  lead,
  stats = [],
  barsTitle = '',
  bars = [],
  trendTitle = '',
  trendNote,
  trend = [],
  trendTone = 'faint',
  kv = [],
  paras = [],
  code,
  rowsTitle = '',
  rows = [],
  toggles = [],
  actions = [],
  link,
  children,
}: PageDetailProps): JSX.Element {
  return (
    <div className="max-w-[980px] px-4 pt-[22px] pb-10 leading-[normal] desk:px-7">
      <div className="flex min-w-0 items-center gap-2 font-mono text-[10.5px]">
        <span className={`shrink-0 whitespace-nowrap ${TEXT_TONE[kindTone]}`}>{kind}</span>
        {meta !== undefined && <span className="min-w-0 truncate text-ink-faint">· {meta}</span>}
      </div>
      <h2 className="mt-2 text-[22px] leading-tight font-semibold tracking-[-0.015em] text-balance text-ink">{title}</h2>
      {lead !== undefined && lead !== null && lead !== '' && (
        <p className="mt-2 max-w-[70ch] text-[14px] leading-[1.55] text-pretty text-ink-2">{lead}</p>
      )}
      {stats.length > 0 && (
        <div className="mt-4">
          <NumbersStrip items={stats} />
        </div>
      )}
      {bars.length > 0 && <BarsBlock title={barsTitle} bars={bars} />}
      {trend.length > 0 && <TrendBlock title={trendTitle} note={trendNote} values={trend} tone={trendTone} />}
      {kv.length > 0 && <KvBlock rows={kv} />}
      {paras.length > 0 && (
        <div className="mt-[18px] flex max-w-[70ch] flex-col gap-2.5">
          {paras.map((para) => (
            <p key={para} className="m-0 text-[13.5px] leading-[1.65] text-pretty text-ink-2">
              {para}
            </p>
          ))}
        </div>
      )}
      {code !== undefined && code !== null && code !== '' && (
        <pre className="mt-4 max-w-[720px] overflow-x-auto rounded-lg border border-line bg-bg px-3.5 py-3 font-mono text-[11.5px] leading-[1.6] whitespace-pre-wrap text-ink-2">
          {code}
        </pre>
      )}
      {rows.length > 0 && <RowsBlock title={rowsTitle} rows={rows} />}
      {toggles.length > 0 && <TogglesBlock rows={toggles} />}
      {(actions.length > 0 || link !== undefined) && <ActionsBlock actions={actions} link={link} />}
      {children}
    </div>
  );
}

/** One row of an accordion place's list. */
export interface PlaceItem {
  key: string;
  glyph: string;
  glyphTone?: PageTone;
  /** A small dot glyph: 9px instead of 12px. */
  glyphSmall?: boolean;
  title: string;
  tag?: string;
  summary?: string;
  age?: string;
  right?: string;
  rightTone?: PageTone;
}

export interface PlaceGroup {
  key: string;
  label: string;
  tone?: GroupTone;
  /** What the header counts when that is not the rows, e.g. a row that opens the whole board. */
  count?: number;
  items: readonly PlaceItem[];
}

/** `?tab=` is the group (or a row that an old tab named), `?item=` the row in it.
 * `pick` writes both at once: two writes in one tick would lose the first. */
export function usePlaceParams<T extends string>(
  ids: readonly T[],
  fallback: T,
): { tab: T; item: string | null; pick: (tab: T, item?: string | null) => void } {
  const [params, setParams] = useSearchParams();
  const raw = params.get('tab');
  const tab = raw !== null && (ids as readonly string[]).includes(raw) ? (raw as T) : fallback;
  const pick = useCallback(
    (nextTab: T, nextItem: string | null = null): void => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (nextTab === fallback) next.delete('tab');
          else next.set('tab', nextTab);
          if (nextItem === null) next.delete('item');
          else next.set('item', nextItem);
          return next;
        },
        { replace: true },
      );
    },
    [fallback, setParams],
  );
  return { tab, item: params.get('item'), pick };
}

function matches(item: PlaceItem, needle: string): boolean {
  return item.title.toLowerCase().includes(needle) || (item.summary ?? '').toLowerCase().includes(needle);
}

/** The whole of a §9 place: the accordion list beside `detail`. */
export function PagePlace({
  title,
  sub,
  searchHint,
  groups,
  openGroup,
  selected,
  onOpenGroup,
  onSelect,
  detail,
  secondaryHint = '—',
  onSecondary,
  narrow,
  after,
}: {
  title: string;
  sub?: string;
  searchHint?: string;
  groups: readonly PlaceGroup[];
  openGroup: string;
  /** The selected row of the open group. */
  selected: string | null;
  onOpenGroup: (group: string) => void;
  /** With the row's own group: under a filter it need not be the open one. */
  onSelect: (key: string, group: string) => void;
  detail: ReactNode;
  secondaryHint?: string;
  onSecondary?: () => void;
  narrow?: NarrowPane;
  /** Under the groups, in the list's own scroll: a filter that must not move the
   * header's geometry, or a note about the list. */
  after?: ReactNode;
}): JSX.Element {
  const { t } = useLingui();
  const [query, setQuery] = useState('');
  const needle = query.trim().toLowerCase();
  const visible = useMemo(
    () =>
      needle === ''
        ? groups
        : groups
            .map((group) => ({ ...group, count: undefined, items: group.items.filter((item) => matches(item, needle)) }))
            .filter((group) => group.items.length > 0),
    [groups, needle],
  );
  const open = visible.find((group) => group.key === openGroup) ?? visible[0];
  const moveBy = (step: number): void => {
    const items = open?.items ?? [];
    if (items.length === 0) return;
    const at = items.findIndex((item) => item.key === selected);
    const next = items[Math.min(Math.max(at + step, 0), items.length - 1)];
    if (next !== undefined && open !== undefined) onSelect(next.key, open.key);
  };
  const emptyText = t`Nothing matches.`;
  return (
    <PlaceLayout
      {...(narrow !== undefined ? { narrow } : {})}
      list={
        <ListPane
          title={title}
          {...(sub !== undefined ? { sub } : {})}
          query={query}
          onQueryChange={setQuery}
          searchHint={searchHint ?? t`filter`}
          keys={{
            onNext: () => moveBy(1),
            onPrev: () => moveBy(-1),
            ...(onSecondary !== undefined ? { onSecondary } : {}),
          }}
          secondaryHint={secondaryHint}
          {...(visible.length === 0
            ? { empty: <div className="px-[14px] py-3 font-mono text-[11px] text-ink-faint">{emptyText}</div> }
            : {})}
        >
          {visible.map((group) => {
            const isOpen = group.key === open?.key;
            return (
              <ListGroup
                key={group.key}
                label={group.label}
                count={group.count ?? group.items.length}
                {...(group.tone !== undefined ? { tone: group.tone } : {})}
                open={isOpen}
                onToggle={() => onOpenGroup(group.key)}
              >
                {group.items.map((item) => (
                  <ListRow
                    key={item.key}
                    glyph={
                      <span
                        className={`inline-block w-[18px] text-center font-mono leading-[18px] ${
                          item.glyphSmall === true ? 'text-[9px]' : 'text-[12px]'
                        } ${TEXT_TONE[item.glyphTone ?? 'faint']}`}
                      >
                        {item.glyph}
                      </span>
                    }
                    title={item.title}
                    tag={item.tag}
                    summary={item.summary}
                    age={item.age}
                    pr={item.right}
                    prTone={TEXT_TONE[item.rightTone ?? 'faint']}
                    selected={item.key === selected}
                    onSelect={() => onSelect(item.key, group.key)}
                  />
                ))}
              </ListGroup>
            );
          })}
          {after}
        </ListPane>
      }
      detail={detail}
    />
  );
}
