// Sessions list (design §3.3): project filter (/api/projects) as a headless
// dropdown ("● all projects ▾"), status chip row with live counts, title
// search (client-side, debounced), live updates over WS. The project filter
// is pushed to the API as a query param; status is filtered CLIENT-side so
// the chip counts always reflect the searched+project-filtered list.
// Redesign layout: mono search input + project dropdown + hairline separator
// + status chips (wrapping row), sessions grouped by day under mono eyebrow
// rules, each day one navy list card — aligned table columns at ≥900px.

import { useCallback, useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import type { Project, Session, SessionStatus, WSMessage } from '../api/types';
import { fetchProjects, fetchSessions } from '../api';
import { projectColor } from '../lib/colors';
import { projectLabel } from '../lib/format';
import { liveActionText } from '../lib/payload';
import { applySessionMessage, useLiveUpdates } from '../lib/ws';
import { SessionCard } from '../components/SessionCard';
import { Empty, ErrorBox, GroupHeader, Loading } from '../components/ui';

const SEARCH_DEBOUNCE_MS = 150;
const PAGE_LIMIT = 100;

/** Case-insensitive substring match over title / project name / slug / branch. */
function matchesQuery(s: Session, q: string): boolean {
  if (q === '') return true;
  return [s.title, s.projectName, s.projectSlug, s.gitBranch].some(
    (v) => v != null && v.toLowerCase().includes(q),
  );
}

const STATUSES: SessionStatus[] = ['active', 'waiting_approval', 'idle', 'completed', 'killed'];
const STATUS_LABELS: Record<SessionStatus, string> = {
  active: 'active',
  waiting_approval: 'waiting',
  idle: 'idle',
  completed: 'done',
  killed: 'killed',
};

function FilterChip({
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
        selected
          ? 'border-[#4a4e58] bg-surface2 text-ink'
          : 'border-line-strong text-ink-dim hover:text-ink'
      }`}
    >
      {children}
    </button>
  );
}

/* ----- project dropdown — headless "all projects ▾" (screenshot 1) ----- */

function ProjectDropdown({
  projects,
  value,
  onChange,
}: {
  projects: Project[];
  /** Selected project slug, or null = all projects. */
  value: string | null;
  onChange: (slug: string | null) => void;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  // Escape closes (restoring focus to the trigger); outside click closes.
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
  }, [open]);

  const focusOption = (delta: 1 | -1): void => {
    const options = menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]');
    if (options === undefined || options.length === 0) return;
    const list = Array.from(options);
    const idx = list.indexOf(document.activeElement as HTMLButtonElement);
    const next = list[(idx + delta + list.length) % list.length];
    next?.focus();
  };

  const select = (slug: string | null): void => {
    onChange(slug);
    setOpen(false);
    buttonRef.current?.focus();
  };

  const selected = value !== null ? (projects.find((p) => p.slug === value) ?? null) : null;
  // Deep-linked slug not in /api/projects yet — show the raw slug, keep the filter.
  const label =
    value === null ? 'all projects' : selected !== null ? projectLabel(selected.name, selected.slug) : value;
  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        ref={buttonRef}
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label="filter by project"
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' && open) {
            e.preventDefault();
            focusOption(1);
          }
        }}
        className="flex max-w-[200px] items-center gap-1.5 rounded-full border border-line-strong px-[11px] py-[5px] font-mono text-[10.5px] whitespace-nowrap text-ink-dim transition-colors hover:text-ink aria-expanded:border-[#4a4e58] aria-expanded:bg-surface2 aria-expanded:text-ink"
      >
        <span className="truncate" style={value !== null ? { color: projectColor(value) } : undefined}>
          {label}
        </span>
        <span aria-hidden="true" className="text-[9px] text-ink-faint">
          ▾
        </span>
      </button>
      {open && (
        <div
          ref={menuRef}
          role="listbox"
          aria-label="project"
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
              e.preventDefault();
              focusOption(e.key === 'ArrowDown' ? 1 : -1);
            }
          }}
          className="absolute top-full left-0 z-20 mt-1.5 min-w-[210px] overflow-hidden rounded-[11px] border border-line-strong bg-field shadow-[0_16px_34px_rgba(0,0,0,0.5)]"
        >
          <DropdownOption
            selected={value === null}
            label="all projects"
            onSelect={() => select(null)}
          />
          {projects.map((p) => (
            <DropdownOption
              key={p.id}
              selected={value === p.slug}
              label={projectLabel(p.name, p.slug)}
              labelColor={projectColor(p.slug)}
              onSelect={() => select(p.slug)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function DropdownOption({
  selected,
  label,
  labelColor,
  onSelect,
}: {
  selected: boolean;
  label: string;
  /** Color the option label (project rows); omit for "all projects". */
  labelColor?: string;
  onSelect: () => void;
}): JSX.Element {
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      onClick={onSelect}
      className={`flex w-full items-center gap-2 px-3 py-2 text-left font-mono text-[11px] transition-colors hover:bg-surface2 ${
        selected ? 'bg-surface2 text-ink' : 'text-ink-3'
      }`}
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

/* ----- day grouping (presentation only — Redesign "today · sun, jul 6") ----- */

interface DayGroup {
  label: string;
  rows: Session[];
}

function dayLabel(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return 'unknown day';
  const name = d
    .toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' })
    .toLowerCase();
  return d.toDateString() === new Date().toDateString() ? `today · ${name}` : name;
}

function groupByDay(sorted: Session[]): DayGroup[] {
  const groups: DayGroup[] = [];
  for (const s of sorted) {
    const label = dayLabel(s.startedAt);
    const last = groups[groups.length - 1];
    if (last !== undefined && last.label === label) last.rows.push(s);
    else groups.push({ label, rows: [s] });
  }
  return groups;
}

export function Sessions(): JSX.Element {
  // Deep-linkable project filter (?project=<slug> — Overview rail rows).
  const [searchParams] = useSearchParams();
  const [projects, setProjects] = useState<Project[]>([]);
  const [project, setProject] = useState<string | null>(searchParams.get('project'));
  const [status, setStatus] = useState<SessionStatus | null>(null);
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [nowById, setNowById] = useState<Record<number, string>>({});
  const loadingMoreRef = useRef(false);
  const sentinelRef = useRef<HTMLDivElement>(null);

  // Title search: raw input + a ~150ms-debounced lowercase query.
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  useEffect(() => {
    const t = setTimeout(() => setQuery(search.trim().toLowerCase()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [search]);

  useEffect(() => {
    fetchProjects()
      .then(setProjects)
      .catch(() => setProjects([])); // dropdown degrades to "all projects"
  }, []);

  // First page — also the WS-reconnect refetch: the live socket keeps the
  // loaded window fresh in between, so resetting to page 1 on reconnect is
  // the simplest correct behaviour (older pages reload on scroll).
  // Only the project filter goes to the API — status stays client-side so
  // the chip counts can be computed over every status of the loaded list.
  const load = useCallback((): void => {
    const filters: { project?: string } = {};
    if (project !== null) filters.project = project;
    fetchSessions(filters, { limit: PAGE_LIMIT })
      .then((page) => {
        setSessions(page.sessions);
        setNextCursor(page.nextCursor);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, [project]);

  useEffect(() => {
    setSessions(null);
    setNextCursor(null);
    load();
  }, [load]);

  // Next page: append, dedup by id (a WS prepend may already hold a row).
  const loadMore = useCallback((): void => {
    if (nextCursor === null || loadingMoreRef.current) return;
    loadingMoreRef.current = true;
    const filters: { project?: string } = {};
    if (project !== null) filters.project = project;
    fetchSessions(filters, { limit: PAGE_LIMIT, cursor: nextCursor })
      .then((page) => {
        setSessions((prev) => {
          const seen = new Set((prev ?? []).map((s) => s.id));
          return [...(prev ?? []), ...page.sessions.filter((s) => !seen.has(s.id))];
        });
        setNextCursor(page.nextCursor);
      })
      .catch((e: unknown) => setError(String(e)))
      .finally(() => {
        loadingMoreRef.current = false;
      });
  }, [nextCursor, project]);

  // Infinite scroll: a sentinel row after the last day group fetches the next
  // page while one exists (rootMargin prefetches before it is visible).
  useEffect(() => {
    const el = sentinelRef.current;
    if (el === null || nextCursor === null) return undefined;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) loadMore();
      },
      { rootMargin: '400px' },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [nextCursor, loadMore]);

  const matchesProject = useCallback(
    (s: Session): boolean => project === null || s.projectSlug === project,
    [project],
  );

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'event_appended') {
        // step-10 contract: the payload carries sessionId → live "now" line.
        const text = liveActionText(msg.payload.event);
        if (text !== null) {
          const { sessionId } = msg.payload;
          setNowById((prev) => ({ ...prev, [sessionId]: text }));
        }
        return;
      }
      setSessions((prev) => {
        if (prev === null) return prev;
        const next = applySessionMessage(prev, msg);
        return next.filter(matchesProject);
      });
    },
    [matchesProject],
  );
  useLiveUpdates(onMessage, load);

  // Chip counts come from the searched + project-filtered list (pre-status),
  // over the LOADED pages only — deeper history loads on scroll.
  const searched = (sessions ?? []).filter((s) => matchesQuery(s, query));
  const counts: Record<SessionStatus, number> = {
    active: 0,
    waiting_approval: 0,
    idle: 0,
    completed: 0,
    killed: 0,
  };
  for (const s of searched) counts[s.status] += 1;

  const sorted = searched
    .filter((s) => status === null || s.status === status)
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  const groups = groupByDay(sorted);

  return (
    <div className="px-4 pt-6 pb-20 desk:px-10 desk:pt-[34px] desk:pb-28">
      <h1 className="font-display text-[26px] font-medium tracking-[-0.01em] desk:text-[30px]">
        Sessions
      </h1>
      <div className="mt-1.5 font-mono text-[11px] text-ink-dim">
        {sorted.length} match · newest first
      </div>

      {/* Filters row (Canvas §Sessions): search · project dropdown │ status chips.
          Wraps cleanly at 390px — the input takes the first line. */}
      <div className="mt-5 flex flex-wrap items-center gap-2">
        <div className="relative w-full desk:w-[260px]">
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="filter by title…"
            aria-label="filter sessions by title"
            className="w-full rounded-[9px] border border-line-strong bg-field px-3 py-[7px] pr-8 font-mono text-[12px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-ink-dim"
          />
          {search !== '' && (
            <button
              type="button"
              onClick={() => setSearch('')}
              aria-label="clear search"
              className="absolute top-1/2 right-2 -translate-y-1/2 font-mono text-[13px] leading-none text-ink-dim transition-colors hover:text-ink"
            >
              ×
            </button>
          )}
        </div>

        <ProjectDropdown projects={projects} value={project} onChange={setProject} />
        <span className="mx-1 w-px shrink-0 self-stretch bg-line-strong" aria-hidden="true" />
        {STATUSES.map((s) => (
          <FilterChip
            key={s}
            selected={status === s}
            onClick={() => setStatus(status === s ? null : s)}
          >
            {counts[s] > 0 ? `${STATUS_LABELS[s]} · ${String(counts[s])}` : STATUS_LABELS[s]}
          </FilterChip>
        ))}
      </div>

      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {sessions === null && error === null && <Loading label="sessions…" />}
      {sessions !== null && sorted.length === 0 && (
        <Empty>
          {query !== '' ? (
            <>
              no sessions match <span className="font-mono text-ink">“{search.trim()}”</span> — try
              a different search or clear the filters
            </>
          ) : (
            <>
              no sessions match — clear filters, or run{' '}
              <span className="font-mono text-ink">swarmery ingest &lt;file.jsonl&gt;</span>
            </>
          )}
        </Empty>
      )}
      {groups.map((g) => (
        <section key={g.label}>
          <GroupHeader>{g.label}</GroupHeader>
          <div className="divide-y divide-line-soft">
            {g.rows.map((s) => (
              <SessionCard key={s.id} session={s} now={nowById[s.id] ?? null} flat />
            ))}
          </div>
        </section>
      ))}
      {nextCursor !== null && (
        <div ref={sentinelRef} className="py-6 text-center font-mono text-[11px] text-ink-faint">
          loading more…
        </div>
      )}
    </div>
  );
}
