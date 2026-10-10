// Shared primitives of the System screen (phase 4, Stage 1 read-only):
// scope/origin/lint badges, the scope filter row (project filtering lives in
// the global header scope switcher), and the list-fetch hook every tab uses. Visual language mirrors components/ui.tsx (hairline
// pill chips, mono micro-type); tooltips are native `title` attributes.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { RefObject } from 'react';
import type { LintSeverity } from '../../api/types';
import type { SystemListFilters } from '../../api/system';
import { useProjectColor } from '../../lib/projectColors';

/* ----- badges ----- */

export function ScopeBadge({
  scope,
  projectSlug,
  projectName,
}: {
  scope: 'global' | 'project';
  projectSlug: string | null;
  projectName?: string | null | undefined;
}): JSX.Element {
  const colorFor = useProjectColor();
  if (scope === 'project') {
    const label = projectName ?? projectSlug;
    const color = projectSlug !== null ? colorFor(projectSlug) : undefined;
    return (
      <span
        className="rounded-full border border-blue/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-blue"
        data-tip-mono data-tip={projectSlug ?? undefined}
      >
        <Trans>project</Trans>
        {label !== null ? (
          <>
            {' · '}
            <span style={color !== undefined ? { color } : undefined}>{label}</span>
          </>
        ) : (
          ''
        )}
      </span>
    );
  }
  return (
    <span className="rounded-full border border-line-strong px-2 py-px font-mono text-[10px] whitespace-nowrap text-ink-dim">
      <Trans>global</Trans>
    </span>
  );
}

export function OriginBadge({
  origin,
  pluginName,
}: {
  origin: 'local' | 'plugin';
  pluginName: string | null;
}): JSX.Element {
  if (origin === 'plugin') {
    return (
      <span className="rounded-full border border-brand/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-brand">
        <Trans>plugin</Trans>
        {pluginName !== null ? ` · ${pluginName}` : ''}
      </span>
    );
  }
  return (
    <span className="rounded-full border border-line-strong px-2 py-px font-mono text-[10px] whitespace-nowrap text-ink-dim">
      <Trans>local</Trans>
    </span>
  );
}

export const LINT_TONES: Record<LintSeverity, string> = {
  error: 'text-red',
  warn: 'text-amber',
  info: 'text-blue',
};

/** Max-severity lint marker of a list row; null (clean) renders nothing. */
export function LintDot({
  severity,
  message,
}: {
  severity: LintSeverity | null;
  message?: string;
}): JSX.Element | null {
  const { t } = useLingui();
  if (severity === null) return null;
  return (
    <span
      className={`shrink-0 font-mono text-[11px] leading-none ${LINT_TONES[severity]}`}
      data-tip={message ?? t`worst active lint finding: ${severity}`}
      aria-label={t`lint ${severity}`}
    >
      {severity === 'info' ? '●' : '▲'}
    </span>
  );
}

/* ----- frontmatter → table -----
 * The contract serves the RAW YAML block (redacted). Top-level `key: value`
 * lines become rows; indented/list continuation lines append to the previous
 * row's value. Anything unparseable falls back to a mono <pre>.
 * Shared by the agents/skills detail panel and the read-only command panel. */

interface FmRow {
  key: string;
  value: string;
}

export function parseFrontmatter(frontmatter: string): FmRow[] | null {
  const rows: FmRow[] = [];
  for (const line of frontmatter.split('\n')) {
    if (line.trim() === '') continue;
    const top = /^([A-Za-z0-9_-]+):\s*(.*)$/.exec(line);
    if (top !== null && !line.startsWith(' ') && !line.startsWith('\t')) {
      rows.push({ key: top[1] ?? '', value: top[2] ?? '' });
      continue;
    }
    const last = rows[rows.length - 1];
    if (last === undefined) return null; // continuation before any key
    last.value = last.value === '' ? line.trim() : `${last.value}\n${line.trim()}`;
  }
  return rows.length > 0 ? rows : null;
}

export function FrontmatterTable({ frontmatter }: { frontmatter: string }): JSX.Element {
  const rows = useMemo(() => parseFrontmatter(frontmatter), [frontmatter]);
  if (rows === null) {
    return (
      <pre className="overflow-x-auto rounded-lg border border-line bg-bg px-3 py-2.5 font-mono text-[11px] leading-relaxed text-ink-2">
        {frontmatter}
      </pre>
    );
  }
  return (
    <div className="overflow-hidden rounded-lg border border-line">
      <table className="w-full border-collapse">
        <tbody>
          {rows.map((row) => (
            <tr key={row.key}>
              <td className="w-[120px] border-b border-line-soft px-2.5 py-1.5 align-top font-mono text-[10px] tracking-[0.06em] text-ink-faint uppercase">
                {row.key}
              </td>
              <td className="border-b border-line-soft px-2.5 py-1.5 align-top font-mono text-[11.5px] whitespace-pre-wrap text-ink-2">
                {row.value === '' ? '—' : row.value}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/* ----- filter chips (Sessions FilterChip rhythm) ----- */

export function FilterChip({
  selected,
  onClick,
  children,
}: {
  selected: boolean;
  onClick: () => void;
  children: string;
}): JSX.Element {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={selected}
      className={`shrink-0 rounded-full border px-[11px] py-1 font-mono text-[10.5px] whitespace-nowrap transition-colors ${
        selected ? 'border-ink-faint bg-surface2 text-ink' : 'border-line-strong text-ink-dim hover:text-ink'
      }`}
    >
      {children}
    </button>
  );
}

/* ----- project dropdown (Sessions-style headless select) ----- */

function DropdownOption({
  selected,
  label,
  labelColor,
  onSelect,
}: {
  selected: boolean;
  label: string;
  /** Color the option label (project rows); omit for scope/sort options. */
  labelColor?: string;
  onSelect: () => void;
}): JSX.Element {
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      onClick={onSelect}
      className={`flex w-full items-center gap-2 px-3 py-1.5 text-left font-mono text-[11px] transition-colors hover:bg-surface2 ${selected ? 'text-ink' : 'text-ink-dim'}`}
    >
      <span
        className="min-w-0 flex-1 truncate"
        style={labelColor !== undefined ? { color: labelColor } : undefined}
      >
        {label}
      </span>
      {selected && <span aria-hidden="true">✓</span>}
    </button>
  );
}

/** Shared headless-dropdown behaviour: close on outside pointer-down / Escape
 * (Escape returns focus to the trigger). setOpen from useState is stable. */
function useDropdownDismiss(
  open: boolean,
  setOpen: (open: boolean) => void,
  rootRef: RefObject<HTMLDivElement | null>,
  buttonRef: RefObject<HTMLButtonElement | null>,
): void {
  useEffect(() => {
    if (!open) return undefined;
    const onPointerDown = (e: MouseEvent): void => {
      if (rootRef.current !== null && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        setOpen(false);
        buttonRef.current?.focus();
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open, setOpen, rootRef, buttonRef]);
}

/** Roving focus over a listbox's [role=option] children (wraps around). */
function focusOption(menuRef: RefObject<HTMLDivElement | null>, delta: 1 | -1): void {
  const opts = menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]');
  if (!opts?.length) return;
  const list = Array.from(opts);
  const idx = list.indexOf(document.activeElement as HTMLButtonElement);
  list[(idx + delta + list.length) % list.length]?.focus();
}

/* ----- sort ----- */

/** List sort keys (URL ?sort=). 'name' is the default and stays out of the URL. */
export type SystemSort = 'name' | 'used' | 'recent' | 'lint';

export const SORT_LABELS: Record<SystemSort, MessageDescriptor> = {
  name: msg`name (A→Z)`,
  used: msg`most used`,
  recent: msg`recently used`,
  lint: msg`lint severity`,
};

const SORT_KEYS: SystemSort[] = ['name', 'used', 'recent', 'lint'];

/** ?sort= → a valid key; anything else (incl. null) falls back to 'name'. */
export function parseSort(value: string | null): SystemSort {
  return (SORT_KEYS as string[]).includes(value ?? '') ? (value as SystemSort) : 'name';
}

const LINT_RANK: Record<LintSeverity, number> = { error: 3, warn: 2, info: 1 };
const lintRank = (s: LintSeverity | null): number => (s === null ? 0 : LINT_RANK[s]);

/** The shape the comparators read — every list row (agents/skills/commands). */
interface Sortable {
  name: string;
  tasks30d: number;
  lastUsed: string | null;
  lintMax: LintSeverity | null;
}

const byName = (a: Sortable, b: Sortable): number => a.name.localeCompare(b.name);

/** Newest lastUsed first; never-used rows sink to the bottom. */
function byRecent(a: Sortable, b: Sortable): number {
  if (a.lastUsed === b.lastUsed) return 0;
  if (a.lastUsed === null) return 1;
  if (b.lastUsed === null) return -1;
  return b.lastUsed.localeCompare(a.lastUsed);
}

/** Stable client-side sort (name is always the final tiebreak). */
export function sortItems<T extends Sortable>(rows: T[], sort: SystemSort): T[] {
  const copy = [...rows];
  copy.sort((a, b) => {
    switch (sort) {
      case 'used':
        return b.tasks30d - a.tasks30d || byRecent(a, b) || byName(a, b);
      case 'recent':
        return byRecent(a, b) || byName(a, b);
      case 'lint':
        return lintRank(b.lintMax) - lintRank(a.lintMax) || byName(a, b);
      default:
        return byName(a, b);
    }
  });
  return copy;
}

/** Sort-key dropdown (headless select; mirrors ProjectDropdown minus the dots). */
export function SortDropdown({
  value,
  onChange,
}: {
  value: SystemSort;
  onChange: (sort: SystemSort) => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useDropdownDismiss(open, setOpen, rootRef, buttonRef);

  const select = (sort: SystemSort): void => {
    onChange(sort);
    setOpen(false);
    buttonRef.current?.focus();
  };

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        ref={buttonRef}
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={t`sort list`}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => { if (e.key === 'ArrowDown' && open) { e.preventDefault(); focusOption(menuRef, 1); } }}
        className="flex items-center gap-1.5 rounded-full border border-line px-2.5 py-[3px] font-mono text-[10.5px] whitespace-nowrap text-ink-dim transition-colors hover:text-ink aria-expanded:border-ink-dim aria-expanded:bg-surface2 aria-expanded:text-ink"
      >
        <span aria-hidden="true" className="text-[11px] leading-none text-ink-dim/70">⇅</span>
        <span className="truncate">{i18n._(SORT_LABELS[value])}</span>
        <span aria-hidden="true" className="text-[8px]">▾</span>
      </button>
      {open && (
        <div
          ref={menuRef}
          role="listbox"
          aria-label={t`sort by`}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); focusOption(menuRef, e.key === 'ArrowDown' ? 1 : -1); }
          }}
          className="absolute top-full right-0 z-20 mt-1 min-w-[160px] overflow-y-auto rounded-lg border border-line bg-surface py-1 shadow-xl shadow-black/40"
        >
          {SORT_KEYS.map((key) => (
            <DropdownOption
              key={key}
              selected={value === key}
              label={i18n._(SORT_LABELS[key])}
              onSelect={() => select(key)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

/** Scope chips (+ optional sort dropdown) — the top filter bar of every System
 * tab. Scope is pushed to the API; project filtering comes from the global
 * header scope switcher and text search from the header ⌘K palette, so neither
 * lives in this row. When onSort is provided the client-side sort dropdown is
 * shown (agents/skills). */
export function FiltersRow({
  scope,
  onScope,
  sort,
  onSort,
  inline = false,
}: {
  scope: 'global' | 'project' | null;
  onScope: (scope: 'global' | 'project' | null) => void;
  sort?: SystemSort;
  onSort?: (sort: SystemSort) => void;
  /** Rendered alongside other controls (SystemShell's filter row) rather than
   * as the standalone bar under a heading — drops the leading top margin. */
  inline?: boolean;
}): JSX.Element {
  const { t } = useLingui();
  return (
    <div className={`${inline ? '' : 'mt-4 '}flex flex-wrap items-center gap-2`}>
      <FilterChip selected={scope === null} onClick={() => onScope(null)}>
        {t`all scopes`}
      </FilterChip>
      <FilterChip selected={scope === 'global'} onClick={() => onScope('global')}>
        {t`global`}
      </FilterChip>
      <FilterChip selected={scope === 'project'} onClick={() => onScope('project')}>
        {t`project`}
      </FilterChip>
      {sort !== undefined && onSort !== undefined && (
        <span className="ml-auto">
          <SortDropdown value={sort} onChange={onSort} />
        </span>
      )}
    </div>
  );
}

/* ----- list-fetch hook ----- */

interface SystemListState<T> {
  rows: T[] | null;
  error: string | null;
  /** Project slugs seen in the last UNfiltered response (chip options). */
  projectOptions: string[];
  retry: () => void;
}

/**
 * Fetches one /api/system list whenever filters or refreshKey change
 * (refreshKey bumps on WS system_item_updated → refetch of the open tab).
 */
export function useSystemList<T extends { projectSlug: string | null }>(
  fetcher: (filters: SystemListFilters) => Promise<T[]>,
  scope: 'global' | 'project' | null,
  project: string | null,
  refreshKey: number,
): SystemListState<T> {
  const [rows, setRows] = useState<T[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [projectOptions, setProjectOptions] = useState<string[]>([]);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    const filters: SystemListFilters = {};
    if (scope !== null) filters.scope = scope;
    if (project !== null) filters.project = project;
    fetcher(filters)
      .then((list) => {
        if (cancelled) return;
        setRows(list);
        setError(null);
        if (project === null) {
          const slugs = [...new Set(list.map((r) => r.projectSlug).filter((s) => s !== null))];
          setProjectOptions(slugs.sort());
        }
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [fetcher, scope, project, refreshKey, attempt]);

  return { rows, error, projectOptions, retry: () => setAttempt((a) => a + 1) };
}
