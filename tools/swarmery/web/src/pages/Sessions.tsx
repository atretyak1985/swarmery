// Sessions list (design §3.3): project filter (/api/projects), status filter,
// live updates over WS. Filters are pushed to the API as query params; WS
// upserts are re-checked against the active filter client-side.

import { useCallback, useEffect, useState } from 'react';
import type { Project, Session, SessionStatus, WSMessage } from '../api/types';
import { fetchProjects, fetchSessions } from '../api';
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

export function Sessions(): JSX.Element {
  const [projects, setProjects] = useState<Project[]>([]);
  const [project, setProject] = useState<string | null>(null);
  const [status, setStatus] = useState<SessionStatus | null>(null);
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);

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
      if (msg.type === 'event_appended') return;
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

  return (
    <>
      <div className="-mx-4 flex gap-1.5 overflow-x-auto px-4 pb-2.5 [-webkit-overflow-scrolling:touch]">
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
      {sorted.map((s) => (
        <SessionCard key={s.id} session={s} />
      ))}
    </>
  );
}
