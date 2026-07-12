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
      className={`shrink-0 rounded-full border px-2.5 py-[3px] font-mono text-[10.5px] whitespace-nowrap transition-colors ${
        selected
          ? 'border-ink-dim bg-surface2 text-ink'
          : 'border-line text-ink-dim hover:text-ink'
      }`}
    >
      {children}
    </button>
  );
}

/* ----- project dropdown — headless "● all projects ▾" (screenshot 1) ----- */

const ALL_PROJECTS_DOT = '#7c8da3'; // ink-dim — neutral "all projects" dot

function Dot({ color }: { color: string }): JSX.Element {
  return (
    <span
      className="h-1.5 w-1.5 shrink-0 rounded-full"
      style={{ background: color }}
      aria-hidden="true"
    />
  );
}

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
  const dot = value === null ? ALL_PROJECTS_DOT : projectColor(value);

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
        className="flex max-w-[200px] items-center gap-1.5 rounded-full border border-line px-2.5 py-[3px] font-mono text-[10.5px] whitespace-nowrap text-ink-dim transition-colors hover:text-ink aria-expanded:border-ink-dim aria-expanded:bg-surface2 aria-expanded:text-ink"
      >
        <Dot color={dot} />
        <span className="truncate">{label}</span>
        <span aria-hidden="true" className="text-[8px]">
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
          className="absolute top-full left-0 z-20 mt-1 min-w-[180px] overflow-hidden rounded-lg border border-line bg-surface py-1 shadow-xl shadow-black/40"
        >
          <DropdownOption
            selected={value === null}
            dot={ALL_PROJECTS_DOT}
            label="all projects"
            onSelect={() => select(null)}
          />
          {projects.map((p) => (
            <DropdownOption
              key={p.id}
              selected={value === p.slug}
              dot={projectColor(p.slug)}
              label={projectLabel(p.name, p.slug)}
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
  dot,
  label,
  onSelect,
}: {
  selected: boolean;
  dot: string;
  label: string;
  onSelect: () => void;
}): JSX.Element {
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      onClick={onSelect}
      className={`flex w-full items-center gap-2 px-3 py-1.5 text-left font-mono text-[11px] transition-colors hover:bg-surface2 ${
        selected ? 'text-ink' : 'text-ink-dim'
      }`}
    >
      <Dot color={dot} />
      <span className="min-w-0 flex-1 truncate">{label}</span>
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
  const [error, setError] = useState<string | null>(null);
  const [nowById, setNowById] = useState<Record<number, string>>({});

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

  // Only the project filter goes to the API — status stays client-side so
  // the chip counts can be computed over every status of the loaded list.
  const load = useCallback((): void => {
    const filters: { project?: string } = {};
    if (project !== null) filters.project = project;
    fetchSessions(filters)
      .then((list) => {
        setSessions(list);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, [project]);

  useEffect(() => {
    setSessions(null);
    load();
  }, [load]);

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

  // Chip counts come from the searched + project-filtered list (pre-status).
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
    <>
      {/* Filters row (screenshot 1): search · project dropdown │ status chips.
          Wraps cleanly at 390px — the input takes the first line. */}
      <div className="flex flex-wrap items-center gap-x-2 gap-y-2 pt-1">
        <div className="relative w-full desk:w-[240px]">
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="filter by title…"
            aria-label="filter sessions by title"
            className="w-full rounded-lg border border-line bg-surface px-3 py-[6px] pr-8 font-mono text-[12px] text-ink transition-colors outline-none placeholder:text-ink-dim focus:border-ink-dim"
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
        <span className="mx-1 w-px shrink-0 self-stretch bg-line" aria-hidden="true" />
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
              no sessions match — try clearing filters, or run{' '}
              <span className="font-mono text-ink">swarmery ingest &lt;file.jsonl&gt;</span>
            </>
          )}
        </Empty>
      )}
      {groups.map((g) => (
        <section key={g.label}>
          <GroupHeader>
            {g.label} · {g.rows.length} {g.rows.length === 1 ? 'session' : 'sessions'}
          </GroupHeader>
          <div className="divide-y divide-line-soft overflow-hidden rounded-xl border border-line bg-surface">
            {g.rows.map((s) => (
              <SessionCard key={s.id} session={s} now={nowById[s.id] ?? null} flat />
            ))}
          </div>
        </section>
      ))}
    </>
  );
}
