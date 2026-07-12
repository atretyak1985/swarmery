// Sessions list (design §3.3): project filter (/api/projects), status filter,
// live updates over WS. Filters are pushed to the API as query params; WS
// upserts are re-checked against the active filter client-side.
// Redesign layout: pill filter chips, sessions grouped by day under mono
// eyebrow rules, each day one navy list card with hairline dividers.

import { useCallback, useEffect, useState } from 'react';
import type { Project, Session, SessionStatus, WSMessage } from '../api/types';
import { fetchProjects, fetchSessions } from '../api';
import { liveActionText } from '../lib/payload';
import { applySessionMessage, useLiveUpdates } from '../lib/ws';
import { SessionCard } from '../components/SessionCard';
import { Empty, ErrorBox, Loading } from '../components/ui';

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
  const [projects, setProjects] = useState<Project[]>([]);
  const [project, setProject] = useState<string | null>(null);
  const [status, setStatus] = useState<SessionStatus | null>(null);
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [nowById, setNowById] = useState<Record<number, string>>({});

  useEffect(() => {
    fetchProjects()
      .then(setProjects)
      .catch(() => setProjects([])); // filter chips degrade gracefully
  }, []);

  const load = useCallback((): void => {
    const filters: { project?: string; status?: string } = {};
    if (project !== null) filters.project = project;
    if (status !== null) filters.status = status;
    fetchSessions(filters)
      .then((list) => {
        setSessions(list);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, [project, status]);

  useEffect(() => {
    setSessions(null);
    load();
  }, [load]);

  const matchesFilter = useCallback(
    (s: Session): boolean =>
      (project === null || s.projectSlug === project) && (status === null || s.status === status),
    [project, status],
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
        return next.filter(matchesFilter);
      });
    },
    [matchesFilter],
  );
  useLiveUpdates(onMessage, load);

  const sorted = (sessions ?? [])
    .slice()
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  const groups = groupByDay(sorted);

  return (
    <>
      <div className="-mx-4 flex gap-1.5 overflow-x-auto px-4 pt-1 pb-2.5 [-webkit-overflow-scrolling:touch]">
        <FilterChip selected={project === null} onClick={() => setProject(null)}>
          all projects
        </FilterChip>
        {projects.map((p) => (
          <FilterChip
            key={p.id}
            selected={project === p.slug}
            onClick={() => setProject(project === p.slug ? null : p.slug)}
          >
            {p.slug}
          </FilterChip>
        ))}
        <span className="mx-1 w-px shrink-0 self-stretch bg-line" aria-hidden="true" />
        {STATUSES.map((s) => (
          <FilterChip
            key={s}
            selected={status === s}
            onClick={() => setStatus(status === s ? null : s)}
          >
            {STATUS_LABELS[s]}
          </FilterChip>
        ))}
      </div>

      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {sessions === null && error === null && <Loading label="sessions…" />}
      {sessions !== null && sorted.length === 0 && (
        <Empty>
          no sessions match — try clearing filters, or run{' '}
          <span className="font-mono text-ink">swarmery ingest &lt;file.jsonl&gt;</span>
        </Empty>
      )}
      {groups.map((g) => (
        <section key={g.label}>
          <div className="mt-4 mb-2 flex items-center gap-2 font-mono text-[10.5px] tracking-[0.1em] text-ink-dim uppercase first-of-type:mt-1">
            {g.label} · {g.rows.length} {g.rows.length === 1 ? 'session' : 'sessions'}
            <span className="h-px flex-1 bg-line" aria-hidden="true" />
          </div>
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
