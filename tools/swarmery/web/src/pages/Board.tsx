// Board page: the board of a project workspace. THREE lanes — Inbox (undecided)
// → Working (accepted: a Queued group in dispatcher order, then the running
// cards) → Review (ran, awaiting a decision) — with done + archived behind a
// collapsed history strip along the bottom. That is the whole change of board
// redesign phase 4; the five-column kanban it replaced is still what the wire
// speaks (`board_column`), still what the dispatcher triggers on
// (`board_column='todo'`), and still what every PATCH carries. Lanes are derived
// at render time by boardModel.splitLanes and nowhere else.
//
// Drag&drop is GONE. It was the dispatch mechanism — drop a card in Todo and the
// dispatcher runs it — which made the one expensive action on this board a
// gesture you could perform by accident, could not perform from a keyboard, and
// could not perform at all on touch. Movement is verbs now: each card renders
// the exits of the lane it is in (TaskCard), the TaskModal holds the review
// decisions, and the card's "move to →" menu remains the escape hatch for any
// legal transition none of those cover.
//
// A "+ New task" button sits at the top of Inbox and opens NewTaskModal (the
// full intake form). Archived stays lazy — it loads on the first expand of the
// history strip (boardColumn='archived' fetch) to keep the default view light.
//
// The board reads the shared BoardState from the workspace layout so the card,
// the status bar, and the detail modal all reflect one source of truth. Demo
// mode (VITE_MOCK) renders a full board from fixtures.

import { useCallback, useMemo, useState, type ReactNode } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import type { BoardTask } from '../api/types';
import { bulkArchiveBoardTasks, fetchBoardTasks } from '../api';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { useWorkspaceBoard, useWorkspaceTerminal } from '../workspace/ProjectWorkspaceLayout';
import {
  AMNESTY_THRESHOLD,
  amnestyBefore,
  amnestyCandidates,
  BOARD_LANES,
  LANE_TITLES,
  labelFilterOptions,
  matchesLabelFilter,
  splitLanes,
} from '../workspace/boardModel';
import { NewTaskModal } from '../workspace/NewTaskModal';
import { TaskCard } from '../workspace/TaskCard';
import { TaskModal } from '../workspace/TaskModal';
import { TaskGraph } from '../workspace/TaskGraph';
import { Empty, ErrorBox, Loading } from '../components/ui';

/** Board header view mode: the lanes or the dependency graph. */
type BoardView = 'board' | 'graph';

/** A muted group heading INSIDE a lane or the history strip. Deliberately
 * quieter than a lane title: a group is a subdivision of one lane, and it must
 * not read as a fourth lane. */
function GroupLabel({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div className="px-1 pt-1 font-mono text-[9.5px] leading-snug tracking-[0.06em] text-ink-faint uppercase">
      {children}
    </div>
  );
}

/** A one-line status note where cards would otherwise be (empty, loading…). */
function Note({ children }: { children: ReactNode }): JSX.Element {
  return <div className="px-1 py-2 font-mono text-[10px] text-ink-faint">{children}</div>;
}

/**
 * The amnesty is a two-step action, so it needs a state rather than a boolean:
 * the count the user approves must be the one the server produced under
 * `dryRun`, not one the client guessed. 'confirm' carries that number.
 */
type AmnestyState =
  | { phase: 'idle' }
  | { phase: 'counting' }
  | { phase: 'confirm'; matched: number }
  | { phase: 'running' };

export function Board(): JSX.Element {
  const { project, projectId, slug, loading: projLoading } = useProjectWorkspace();
  const board = useWorkspaceBoard();
  const openTerminal = useWorkspaceTerminal();
  const navigate = useNavigate();
  // Agent Hub "Run now" deep-links here with ?compose=@<agent>: — the modal
  // opens prefilled from it (the agent picker resolves the "@name:" prefix) so a
  // task can be dispatched to that agent in one hop.
  const [searchParams, setSearchParams] = useSearchParams();
  const compose = searchParams.get('compose') ?? '';
  const [composing, setComposing] = useState(compose !== '');
  const [openId, setOpenId] = useState<number | null>(null);
  const [view, setView] = useState<BoardView>('board');

  // History strip: `done` rides the live board query, `archived` is its own lazy
  // fetch — unchanged from when these were two columns, only its trigger moved.
  const [archived, setArchived] = useState<BoardTask[] | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [archiveLoading, setArchiveLoading] = useState(false);

  // Label filter — kept in the URL (not local state) so `?label=jira-ticket`
  // is both the write target and the read source: a reload restores it with
  // no separate rehydration step to keep in sync.
  const labelFilter = searchParams.get('label');
  const setLabelFilter = useCallback(
    (label: string | null): void => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (label === null || label === '') next.delete('label');
          else next.set('label', label);
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );
  const labelOptions = useMemo(() => labelFilterOptions(board.tasks, labelFilter), [board.tasks, labelFilter]);
  // Filtered BEFORE the lane split so Archived (its own lazy fetch) is
  // untouched — this list only ever feeds `lanes` below.
  const labelFilteredTasks = useMemo(
    () => board.tasks.filter((t) => matchesLabelFilter(t, labelFilter)),
    [board.tasks, labelFilter],
  );

  // Every grouping and ordering decision the board makes lives in this one pure
  // call — including the Queued group's dispatcher order — so what the board
  // claims about "what runs next" is testable without rendering anything.
  const lanes = useMemo(() => splitLanes(labelFilteredTasks), [labelFilteredTasks]);

  // --- inbox amnesty ----------------------------------------------------------
  // Counted off the UNFILTERED list on purpose: the banner reports the state of
  // the inbox, and the server's sweep ignores the client's label filter. Showing
  // "3 cards" because a filter is applied, then archiving 231, is the one
  // outcome a confirm step exists to prevent.
  const triageTotal = useMemo(
    () => board.tasks.filter((t) => t.boardColumn === 'triage').length,
    [board.tasks],
  );
  // `now` is frozen for the mount: the instant shown in the banner, counted
  // against, and finally sent to the server must be ONE instant, or the
  // confirmed number and the written number drift apart. The CUTOFF is no longer
  // frozen with it — it is derived from the sweeper's own TTL (staleAfter minus
  // idleSince), which arrives with the first board payload, so it settles as
  // soon as the cards land and stays pinned to this instant.
  const [amnestyNow] = useState(() => Date.now());
  const amnestyCutoffAt = useMemo(() => amnestyBefore(board.tasks, amnestyNow), [board.tasks, amnestyNow]);
  const amnestyEligible = useMemo(
    () => amnestyCandidates(board.tasks, amnestyNow),
    [board.tasks, amnestyNow],
  );
  const [amnesty, setAmnesty] = useState<AmnestyState>({ phase: 'idle' });
  const [amnestyError, setAmnestyError] = useState<string | null>(null);
  const showAmnesty = triageTotal > AMNESTY_THRESHOLD && amnestyEligible > 0;

  const amnestyBody = useMemo(
    () =>
      projectId === null || amnestyCutoffAt === null
        ? null
        : ({ projectId, column: 'triage', before: amnestyCutoffAt } as const),
    [projectId, amnestyCutoffAt],
  );

  const countAmnesty = (): void => {
    if (amnestyBody === null) return;
    setAmnestyError(null);
    setAmnesty({ phase: 'counting' });
    bulkArchiveBoardTasks({ ...amnestyBody, dryRun: true })
      .then((r) => setAmnesty({ phase: 'confirm', matched: r.matched }))
      .catch((e: unknown) => {
        setAmnestyError(e instanceof Error ? e.message : String(e));
        setAmnesty({ phase: 'idle' });
      });
  };

  const runAmnesty = (): void => {
    if (amnestyBody === null) return;
    setAmnesty({ phase: 'running' });
    bulkArchiveBoardTasks(amnestyBody)
      .then(() => {
        setAmnesty({ phase: 'idle' });
        // The endpoint emits no WS frames (one per row would flood the bus), so
        // this reload IS how the initiating tab learns what happened. Archived
        // is dropped so its lazy fetch re-runs and shows the new arrivals.
        setArchived(null);
        board.reload();
      })
      .catch((e: unknown) => {
        setAmnestyError(e instanceof Error ? e.message : String(e));
        setAmnesty({ phase: 'idle' });
      });
  };

  /** Carry a card into Planning Mode: the title and prompt become the idea, so
   * a suggestion too big to just Run becomes the seed of a plan in one hop. */
  const planTask = (task: BoardTask): void => {
    const idea = `${task.title}\n\n${task.prompt}`;
    navigate(`/p/${encodeURIComponent(slug)}/planning?idea=${encodeURIComponent(idea)}`);
  };

  /**
   * Park or un-park a card from its own card — the Working-lane verb. Goes
   * through `patchTask` (not `moveTask`): pausing is a flag, not a column, and
   * the card must NOT jump lanes when it is paused. `patchTask` rejects rather
   * than toasting, so the failure is routed into the board's one action-error
   * strip by hand.
   */
  const togglePause = (task: BoardTask): void => {
    board.patchTask(task.id, { userPaused: !task.userPaused }).catch((e: unknown) => {
      board.setActionError(e instanceof Error ? e.message : String(e));
    });
  };

  // Same lazy fetch the Archived column used to own; only its trigger moved to
  // the history strip. Fetched once per mount — the amnesty resets it to null
  // when it archives rows so the next expand sees the new arrivals.
  const toggleHistory = (): void => {
    setHistoryOpen((v) => !v);
    if (archived === null && projectId !== null) {
      setArchiveLoading(true);
      fetchBoardTasks(projectId, 'archived')
        .then(setArchived)
        .catch(() => setArchived([]))
        .finally(() => setArchiveLoading(false));
    }
  };

  const openTask = openId !== null ? board.tasks.find((t) => t.id === openId) ?? null : null;

  // The history strip's archived half renders from its own lazy fetch, so a
  // deleted row has to be dropped there too — the board list alone would leave
  // the card on screen until the next expand.
  const deleteTask = (id: number): Promise<void> =>
    board.deleteTask(id).then(() => {
      setArchived((prev) => (prev === null ? prev : prev.filter((t) => t.id !== id)));
    });

  /**
   * A card in a lane, wired to every verb any lane might offer. TaskCard picks
   * which of them actually render from the card's own column, so the lane bodies
   * below stay pure layout and there is one place to change what a card can do.
   */
  const liveCard = (t: BoardTask): JSX.Element => {
    const worktree = t.worktreePath;
    return (
      <TaskCard
        key={t.id}
        task={t}
        onOpen={() => setOpenId(t.id)}
        onMove={(to) => board.moveTask(t.id, to)}
        onPlan={() => planTask(t)}
        onTogglePause={() => togglePause(t)}
        onOpenTerminal={
          openTerminal !== null && worktree !== null
            ? () => openTerminal(t.externalId, worktree)
            : undefined
        }
      />
    );
  };

  /** A card in the history strip: a record, so it opens and it can be moved back
   * out, but it carries none of the lane verbs. */
  const historyCard = (t: BoardTask): JSX.Element => (
    <TaskCard key={t.id} task={t} onOpen={() => setOpenId(t.id)} onMove={(to) => board.moveTask(t.id, to)} />
  );

  if (projLoading) return <Loading label="workspace…" />;
  if (project === null) {
    return (
      <div className="px-4 py-8 desk:px-8">
        <Empty>unknown project — pick one from the switcher</Empty>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col px-3 py-4 desk:px-5">
      {board.error !== null && (
        <div className="mb-2">
          <ErrorBox message={board.error} onRetry={board.reload} />
        </div>
      )}
      {board.actionError !== null && (
        <div
          role="alert"
          className="mb-2 flex items-center gap-2 rounded-lg border border-red/40 bg-red/10 px-3 py-1.5 font-mono text-[11px] text-red"
        >
          <span className="min-w-0 flex-1">{board.actionError}</span>
          <button
            type="button"
            onClick={board.clearActionError}
            aria-label="dismiss"
            className="text-red/70 transition-colors hover:text-red"
          >
            ×
          </button>
        </div>
      )}

      {amnestyError !== null && (
        <div
          role="alert"
          className="mb-2 flex items-center gap-2 rounded-lg border border-red/40 bg-red/10 px-3 py-1.5 font-mono text-[11px] text-red"
        >
          <span className="min-w-0 flex-1">{amnestyError}</span>
          <button
            type="button"
            onClick={() => setAmnestyError(null)}
            aria-label="dismiss"
            className="text-red/70 transition-colors hover:text-red"
          >
            ×
          </button>
        </div>
      )}

      {/* Inbox amnesty: only above the threshold, because under it Triage is
          still a list a human can read and clearing it wholesale is the wrong
          offer. Two steps by construction — the number in the confirm comes
          from the server's dry run, never from the count in the pitch. */}
      {showAmnesty && (
        <div className="mb-2 flex flex-wrap items-center gap-2 rounded-lg border border-amber/40 bg-amber/10 px-3 py-1.5 text-[11.5px] text-amber">
          {amnesty.phase === 'confirm' ? (
            <>
              <span className="min-w-0 flex-1">
                Archive <span className="font-mono font-semibold">{amnesty.matched}</span> captured
                card{amnesty.matched === 1 ? '' : 's'}? They stay findable in Archived.
              </span>
              <button
                type="button"
                onClick={runAmnesty}
                className="rounded-lg border border-amber/60 bg-amber/20 px-2.5 py-1 font-mono text-[11px] font-semibold transition-colors hover:bg-amber/30"
              >
                Yes, archive {amnesty.matched}
              </button>
              <button
                type="button"
                onClick={() => setAmnesty({ phase: 'idle' })}
                className="rounded-lg border border-transparent px-2 py-1 font-mono text-[11px] text-amber/80 transition-colors hover:border-amber/40"
              >
                cancel
              </button>
            </>
          ) : (
            <>
              <span className="min-w-0 flex-1">
                <span className="font-mono font-semibold">{amnestyEligible}</span> captured card
                {amnestyEligible === 1 ? '' : 's'} in Triage {amnestyEligible === 1 ? 'has' : 'have'}{' '}
                passed the auto-archive date.
              </span>
              <button
                type="button"
                disabled={amnesty.phase !== 'idle'}
                onClick={countAmnesty}
                className="rounded-lg border border-amber/50 px-2.5 py-1 font-mono text-[11px] transition-colors hover:bg-amber/20 disabled:opacity-50"
              >
                {amnesty.phase === 'counting'
                  ? 'counting…'
                  : amnesty.phase === 'running'
                    ? 'archiving…'
                    : 'Archive them'}
              </button>
            </>
          )}
        </div>
      )}

      {/* Board ⇄ Graph toggle. */}
      <div className="mb-3 flex items-center gap-2">
        <div className="flex items-center gap-1" role="group" aria-label="board view">
          {(['board', 'graph'] as const).map((v) => (
            <button
              key={v}
              type="button"
              onClick={() => setView(v)}
              aria-pressed={view === v}
              className={`rounded-lg border px-2.5 py-1 font-mono text-[11px] capitalize transition-colors ${
                view === v
                  ? 'border-line-strong bg-surface2 text-brand'
                  : 'border-transparent text-ink-dim hover:bg-surface2/50 hover:text-ink'
              }`}
            >
              {v === 'board' ? '▤ Board' : '⋈ Graph'}
            </button>
          ))}
        </div>

        {(labelOptions.length > 0 || labelFilter !== null) && (
          <div className="flex items-center gap-1">
            <select
              value={labelFilter ?? ''}
              onChange={(e) => setLabelFilter(e.target.value === '' ? null : e.target.value)}
              aria-label="filter by label"
              className={`rounded-lg border px-2 py-1 font-mono text-[11px] outline-none transition-colors ${
                labelFilter !== null
                  ? 'border-brand/50 bg-brand/10 text-brand'
                  : 'border-line bg-surface text-ink-dim hover:bg-surface2/50'
              }`}
            >
              <option value="">label: any</option>
              {labelOptions.map(({ label, count }) => (
                <option key={label} value={label}>
                  {count === 0 ? `${label} (no cards)` : label}
                </option>
              ))}
            </select>
            {labelFilter !== null && (
              <button
                type="button"
                onClick={() => setLabelFilter(null)}
                aria-label="clear label filter"
                data-tip="clear label filter"
                className="text-[13px] leading-none text-ink-faint transition-colors hover:text-ink"
              >
                ×
              </button>
            )}
          </div>
        )}
      </div>

      {board.loading ? (
        <Loading label="board…" />
      ) : view === 'graph' ? (
        <TaskGraph tasks={board.tasks} onOpen={setOpenId} />
      ) : (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex min-h-0 flex-1 gap-3 overflow-x-auto pb-2">
            {BOARD_LANES.map((lane) => (
              <section
                key={lane}
                aria-label={`${LANE_TITLES[lane]} lane`}
                className="flex min-w-[232px] flex-1 basis-0 flex-col rounded-xl border border-line bg-surface/40"
              >
                <div className="flex items-center gap-2 px-3 pt-3 pb-2">
                  <span className="font-mono text-[10.5px] tracking-[0.1em] text-ink-dim uppercase">
                    {LANE_TITLES[lane]}
                  </span>
                  <span className="font-mono text-[10px] text-ink-faint">
                    {lane === 'inbox'
                      ? lanes.inbox.length
                      : lane === 'working'
                        ? lanes.queued.length + lanes.running.length
                        : lanes.review.length}
                  </span>
                </div>
                <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto px-2 pb-2">
                  {lane === 'inbox' && (
                    <>
                      {projectId !== null && (
                        <button
                          type="button"
                          onClick={() => setComposing(true)}
                          className="w-full rounded-lg border border-dashed border-line bg-transparent px-2.5 py-2 text-left text-[12px] text-ink-faint transition-colors hover:border-ink-dim hover:bg-field hover:text-ink"
                        >
                          + New task
                        </button>
                      )}
                      {lanes.inbox.map(liveCard)}
                    </>
                  )}

                  {/* Working is the only lane with internal structure, because
                      "accepted" covers two states a reviewer must not confuse:
                      still cancellable, and already burning a slot. The queue is
                      shown in the dispatcher's own candidate order so the top
                      card IS the next one to start. */}
                  {lane === 'working' && (
                    <>
                      {lanes.queued.length > 0 && (
                        <>
                          <GroupLabel>Queued — waiting for a dispatch slot</GroupLabel>
                          {lanes.queued.map(liveCard)}
                        </>
                      )}
                      {lanes.running.length > 0 && (
                        <>
                          <GroupLabel>Running</GroupLabel>
                          {lanes.running.map(liveCard)}
                        </>
                      )}
                      {lanes.queued.length === 0 && lanes.running.length === 0 && <Note>empty</Note>}
                    </>
                  )}

                  {lane === 'review' && (
                    <>
                      {lanes.review.map(liveCard)}
                      {lanes.review.length === 0 && <Note>empty</Note>}
                    </>
                  )}
                </div>
              </section>
            ))}
          </div>

          {/* History: done + archived, collapsed. They are not a lane — nothing
              is waiting on them — but they must stay reachable, and the counts
              are the cheapest honest signal that the board is being worked. */}
          <section aria-label="history" className="mt-1 shrink-0 rounded-xl border border-line bg-surface/20">
            <button
              type="button"
              onClick={toggleHistory}
              aria-expanded={historyOpen}
              className="flex w-full items-center gap-2 px-3 py-2 text-left"
            >
              <span className="font-mono text-[10.5px] tracking-[0.1em] text-ink-dim uppercase">
                History
              </span>
              <span aria-hidden="true" className="font-mono text-[9px] text-ink-faint">
                {historyOpen ? '▾' : '▸'}
              </span>
              <span className="font-mono text-[10px] text-ink-faint">
                {lanes.done.length} done
                {/* An em-dash rather than 0 until the lazy fetch has run: the
                    board genuinely does not know the archived count yet, and
                    printing 0 would be a claim it cannot make. */}
                {' · '}
                {archived === null ? '—' : archived.length} archived
              </span>
            </button>
            {historyOpen && (
              // Two columns only (done/archived, no third lane) so they split the
              // strip's full width 50/50 — a grid, not the fixed-width flex-none
              // columns a kanban lane uses, because there is no card-count reason
              // for either half to stay narrow while the other has room to spare.
              <div className="grid max-h-[38vh] grid-cols-2 gap-3 border-t border-line px-2 py-2">
                <div className="flex min-w-0 flex-col gap-2 overflow-y-auto">
                  <GroupLabel>Done</GroupLabel>
                  {lanes.done.length > 0 ? lanes.done.map(historyCard) : <Note>empty</Note>}
                </div>
                <div className="flex min-w-0 flex-col gap-2 overflow-y-auto">
                  <GroupLabel>Archived</GroupLabel>
                  {archiveLoading ? (
                    <Note>loading…</Note>
                  ) : archived !== null && archived.length > 0 ? (
                    archived.map(historyCard)
                  ) : (
                    <Note>empty</Note>
                  )}
                </div>
              </div>
            )}
          </section>
        </div>
      )}

      {composing && projectId !== null && (
        <NewTaskModal
          projectId={projectId}
          projectSlug={project.slug}
          initialText={compose}
          onCreated={board.addTask}
          onClose={() => setComposing(false)}
        />
      )}

      {openTask !== null && (
        <TaskModal
          task={openTask}
          onClose={() => setOpenId(null)}
          onPatch={(patch) => board.patchTask(openTask.id, patch)}
          onDelete={() => deleteTask(openTask.id)}
        />
      )}
    </div>
  );
}
