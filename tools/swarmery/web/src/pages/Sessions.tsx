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
import { liveActionText } from '../lib/payload';
import { useScope } from '../lib/scope';
import { applySessionMessage, useLiveUpdates } from '../lib/ws';
import { ProjectDropdown } from '../components/ProjectDropdown';
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
  // Deep-linkable project filter (?project=<slug> — Overview rail rows) wins
  // over the global scope on first render; scope changes re-seed it after.
  const [searchParams] = useSearchParams();
  const { scope } = useScope();
  const [projects, setProjects] = useState<Project[]>([]);
  const [project, setProject] = useState<string | null>(searchParams.get('project') ?? scope);
  // The header switcher re-seeds the local filter (spec: local filters still
  // work, but initialize from — and follow — the global scope). Guarded by the
  // previous scope so the mount run cannot clobber a ?project= deep link.
  const prevScopeRef = useRef(scope);
  useEffect(() => {
    if (prevScopeRef.current === scope) return;
    prevScopeRef.current = scope;
    setProject(scope);
  }, [scope]);
  const [status, setStatus] = useState<SessionStatus | null>(null);
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [nowById, setNowById] = useState<Record<number, string>>({});
  const loadingMoreRef = useRef(false);
  const sentinelRef = useRef<HTMLDivElement>(null);
  // Request generation: bumped whenever the project filter resets the list so
  // stale in-flight page responses (old project) are dropped, not appended —
  // otherwise a slow page-2 fetch would leak old-project rows and resurrect
  // the old cursor (project filtering is server-side, not re-checked here).
  const genRef = useRef(0);

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
    const gen = genRef.current;
    const filters: { project?: string } = {};
    if (project !== null) filters.project = project;
    fetchSessions(filters, { limit: PAGE_LIMIT })
      .then((page) => {
        if (gen !== genRef.current) return; // stale — filter changed mid-flight
        setSessions(page.sessions);
        setNextCursor(page.nextCursor);
        setError(null);
      })
      .catch((e: unknown) => {
        if (gen !== genRef.current) return;
        setError(String(e));
      });
  }, [project]);

  useEffect(() => {
    genRef.current += 1; // invalidate in-flight responses for the old filter
    setSessions(null);
    setNextCursor(null);
    load();
  }, [load]);

  // Next page: append, dedup by id (a WS prepend may already hold a row).
  const loadMore = useCallback((): void => {
    if (nextCursor === null || loadingMoreRef.current) return;
    loadingMoreRef.current = true;
    const gen = genRef.current;
    const filters: { project?: string } = {};
    if (project !== null) filters.project = project;
    fetchSessions(filters, { limit: PAGE_LIMIT, cursor: nextCursor })
      .then((page) => {
        if (gen !== genRef.current) return; // stale — would leak old-project rows
        setSessions((prev) => {
          const seen = new Set((prev ?? []).map((s) => s.id));
          return [...(prev ?? []), ...page.sessions.filter((s) => !seen.has(s.id))];
        });
        setNextCursor(page.nextCursor);
      })
      .catch((e: unknown) => {
        if (gen !== genRef.current) return;
        setError(String(e));
      })
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
