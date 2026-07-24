// Plans / Epics (fusion phase 10): a workspace plan IS an epic. This tab lists
// the project's epics (plan dirs the ingester parsed) with a checkbox rollup,
// drills into a phase timeline (seq order, depends-on badges, per-phase
// progress, an Activate button that mints a board task and stays disabled until
// the phases it depends on have their board tasks done), and opens any plan doc
// in an editable drawer — the workspace folder becomes invisible infrastructure
// (read, edit, activate, track from the platform; files stay the storage).
//
// Liveness: the epic list refetches on the board's `task_updated` WS signal so
// an activated phase and its board-task column stay current without a reload.

import { useCallback, useEffect, useMemo, useState } from 'react';
import type { BoardColumn, Epic, EpicPhase, WSMessage } from '../api/types';
import { activateEpicPhase, fetchEpics, PhaseAlreadyActivatedError } from '../api';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { useLiveUpdates } from '../lib/ws';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { PlanDocDrawer } from '../workspace/PlanDocDrawer';

/** A board column that counts as "resolved" for the dependency gate. */
function isResolvedColumn(col: BoardColumn | null): boolean {
  return col === 'done' || col === 'archived';
}

export function Plans(): JSX.Element {
  const { project, projectId, loading: projLoading } = useProjectWorkspace();
  const [epics, setEpics] = useState<Epic[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<number | null>(null); // taskId
  const [actionError, setActionError] = useState<string | null>(null);
  const [busyPhase, setBusyPhase] = useState<number | null>(null);
  const [editDoc, setEditDoc] = useState<{ taskId: number; path: string; title: string } | null>(null);

  const reload = useCallback((): void => {
    if (projectId === null) {
      setEpics([]);
      return;
    }
    fetchEpics(projectId)
      .then((rows) => {
        setEpics(rows);
        setError(null);
        // Keep the selection if it still exists; else pick the first.
        setSelected((cur) => {
          if (cur !== null && rows.some((e) => e.taskId === cur)) return cur;
          return rows[0]?.taskId ?? null;
        });
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [projectId]);

  useEffect(() => {
    reload();
  }, [reload]);

  // A board task changing (activation, a move to done) can change a phase's
  // gate — refetch the epics on task_updated for this project.
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type !== 'task_updated') return;
      if (projectId !== null && msg.payload.projectId !== projectId) return;
      reload();
    },
    [projectId, reload],
  );
  useLiveUpdates(onMessage, reload);

  const activeEpic = useMemo(
    () => (selected !== null ? (epics?.find((e) => e.taskId === selected) ?? null) : null),
    [epics, selected],
  );

  const activate = (epic: Epic, phase: EpicPhase): void => {
    setBusyPhase(phase.id);
    setActionError(null);
    activateEpicPhase(epic.taskId, phase.id)
      .then(() => reload())
      .catch((e: unknown) => {
        if (e instanceof PhaseAlreadyActivatedError) {
          // Someone already activated it — just refresh to show the board link.
          reload();
          return;
        }
        setActionError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => setBusyPhase(null));
  };

  if (projLoading) return <Loading label="workspace…" />;
  if (project === null) {
    return (
      <div className="px-4 py-8 desk:px-8">
        <Empty>unknown project — pick one from the switcher</Empty>
      </div>
    );
  }
  if (error !== null) {
    return (
      <div className="px-4 py-6 desk:px-8">
        <ErrorBox message={error} onRetry={reload} />
      </div>
    );
  }
  if (epics === null) return <Loading label="epics…" />;
  if (epics.length === 0) {
    return (
      <div className="px-4 py-8 desk:px-8">
        <Empty>
          no epics yet — a plan under this project&apos;s workspace (a{' '}
          <code className="font-mono text-[11px]">plan/</code> dir with a phase table) becomes an epic here
        </Empty>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col px-3 py-4 desk:px-6">
      {actionError !== null && (
        <div
          role="alert"
          className="mb-3 flex items-center gap-2 rounded-lg border border-red/40 bg-red/10 px-3 py-1.5 font-mono text-[11px] text-red"
        >
          <span className="min-w-0 flex-1">{actionError}</span>
          <button type="button" onClick={() => setActionError(null)} aria-label="dismiss" className="text-red/70">
            ×
          </button>
        </div>
      )}

      <div className="flex min-h-0 flex-1 gap-5">
        {/* Epic list. */}
        <div className="w-[280px] shrink-0 space-y-1.5 overflow-y-auto pr-1">
          {epics.map((e) => (
            <button
              key={e.taskId}
              type="button"
              onClick={() => setSelected(e.taskId)}
              aria-current={selected === e.taskId}
              className={`block w-full rounded-lg border px-3 py-2.5 text-left transition-colors ${
                selected === e.taskId
                  ? 'border-line-strong bg-surface2'
                  : 'border-line bg-surface/40 hover:border-line-strong'
              }`}
            >
              <div className="truncate text-[13px] font-medium text-ink">{e.title}</div>
              <div className="mt-0.5 font-mono text-[10px] text-ink-faint">
                {e.startedAt !== null ? e.startedAt.slice(0, 10) : e.externalId}
                {' · '}
                {e.phases.length} phase{e.phases.length === 1 ? '' : 's'}
              </div>
              <ProgressBar done={e.rollup.done} total={e.rollup.total} className="mt-2" />
            </button>
          ))}
        </div>

        {/* Epic detail: phase timeline. */}
        <div className="min-w-0 flex-1 overflow-y-auto">
          {activeEpic === null ? (
            <Empty>select an epic</Empty>
          ) : (
            <EpicDetail
              epic={activeEpic}
              busyPhase={busyPhase}
              onActivate={activate}
              onEditDoc={(path, title) => setEditDoc({ taskId: activeEpic.taskId, path, title })}
            />
          )}
        </div>
      </div>

      {editDoc !== null && (
        <PlanDocDrawer
          taskId={editDoc.taskId}
          path={editDoc.path}
          title={editDoc.title}
          onClose={() => setEditDoc(null)}
          onChanged={reload}
        />
      )}
    </div>
  );
}

function EpicDetail({
  epic,
  busyPhase,
  onActivate,
  onEditDoc,
}: {
  epic: Epic;
  busyPhase: number | null;
  onActivate: (epic: Epic, phase: EpicPhase) => void;
  onEditDoc: (path: string, title: string) => void;
}): JSX.Element {
  // Which seq numbers are "resolved" (their board task is done/archived) — used
  // to gate the Activate button of dependent phases.
  const resolvedSeqs = useMemo(() => {
    const s = new Set<number>();
    for (const p of epic.phases) {
      if (isResolvedColumn(p.boardColumn)) s.add(p.seq);
    }
    return s;
  }, [epic.phases]);

  return (
    <div className="pr-1">
      <div className="mb-3 flex items-baseline justify-between gap-3">
        <h2 className="truncate text-[15px] font-semibold text-ink">{epic.title}</h2>
        <span className="shrink-0 font-mono text-[11px] text-ink-dim">
          {epic.rollup.done}/{epic.rollup.total} ({Math.round(epic.rollup.pct)}%)
        </span>
      </div>
      <button
        type="button"
        onClick={() => onEditDoc('README.md', `${epic.title} — README`)}
        className="mb-4 rounded-md border border-line px-2 py-1 font-mono text-[10.5px] text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
      >
        ❐ open plan README
      </button>

      <ol className="space-y-2">
        {epic.phases.map((p) => {
          const unmetDeps = p.dependsOn.filter((seq) => !resolvedSeqs.has(seq));
          const activated = p.activatedAt !== null;
          const canActivate = !activated && unmetDeps.length === 0;
          const disabledReason =
            unmetDeps.length > 0
              ? `waiting on phase ${unmetDeps.join(', ')} (board task not done)`
              : undefined;
          return (
            <li
              key={p.id}
              className="rounded-lg border border-line bg-surface/40 px-3 py-2.5"
            >
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-[10px] text-ink-faint">#{p.seq}</span>
                    <span className="truncate text-[13px] font-medium text-ink">{p.name}</span>
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5">
                    {p.dependsOn.map((seq) => (
                      <span
                        key={seq}
                        className={`rounded border px-1 py-px font-mono text-[9px] ${
                          resolvedSeqs.has(seq)
                            ? 'border-green/40 text-green'
                            : 'border-line text-ink-faint'
                        }`}
                        title={resolvedSeqs.has(seq) ? 'dependency resolved' : 'dependency pending'}
                      >
                        ← #{seq}
                      </span>
                    ))}
                    <span className="font-mono text-[10px] text-ink-faint">
                      {p.checkboxesDone}/{p.checkboxesTotal || 0}
                    </span>
                  </div>
                  <ProgressBar done={p.checkboxesDone} total={p.checkboxesTotal} className="mt-2 max-w-[220px]" />
                </div>

                <div className="flex shrink-0 flex-col items-end gap-1.5">
                  {activated ? (
                    <span
                      className="rounded border border-brand/40 bg-brand/10 px-1.5 py-px font-mono text-[9.5px] text-brand"
                      title={p.boardTaskExternalId ?? undefined}
                    >
                      activated{p.boardColumn !== null ? ` · ${p.boardColumn}` : ''}
                    </span>
                  ) : (
                    <button
                      type="button"
                      disabled={!canActivate || busyPhase === p.id}
                      onClick={() => onActivate(epic, p)}
                      title={disabledReason}
                      className="rounded-md border border-line-strong bg-surface2 px-2 py-1 font-mono text-[10.5px] text-brand transition-colors hover:bg-surface2/70 disabled:cursor-not-allowed disabled:border-line disabled:text-ink-faint"
                    >
                      {busyPhase === p.id ? 'activating…' : 'Activate'}
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={() => onEditDoc(p.docRelPath, p.name)}
                    className="font-mono text-[9.5px] text-ink-dim underline-offset-2 transition-colors hover:text-ink hover:underline"
                  >
                    edit doc
                  </button>
                </div>
              </div>
              {disabledReason !== undefined && (
                <div className="mt-1.5 font-mono text-[9.5px] text-ink-faint">{disabledReason}</div>
              )}
            </li>
          );
        })}
      </ol>
    </div>
  );
}

/** A thin progress bar. total===0 renders an empty track (no divide-by-zero). */
function ProgressBar({
  done,
  total,
  className = '',
}: {
  done: number;
  total: number;
  className?: string;
}): JSX.Element {
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  return (
    <div
      className={`h-1.5 overflow-hidden rounded-full bg-surface2 ${className}`}
      role="progressbar"
      aria-valuenow={pct}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={`${done} of ${total} checkboxes done`}
    >
      <div className="h-full rounded-full bg-brand transition-[width]" style={{ width: `${String(pct)}%` }} />
    </div>
  );
}
