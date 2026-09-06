// The card's verbs (board redesign v2, phase 2): ONE primary action per lane,
// everything else behind "…".
//
// The old modal put six buttons in a row — Save, Move to Queued, Pause, Archive,
// Delete, and in review four more — with no ranking between them, so the reader
// had to know which one their card was for. A card in the Inbox is there to be
// run; a card in Review is there to be decided; a card that is done is there to
// be filed. That is the primary. The rest are still one click away, they are
// just no longer competing with it.
//
// Save is gone entirely — the modal autosaves (useTaskDraft) — which is what
// makes a single primary possible at all: the button that used to be first was
// the one that only existed because saving was manual.
//
// The four review decisions keep calling exactly the API functions they called
// before the split (verifyBoardTask / rerunBoardTask / discardBoardTask /
// landBoardTask, with the same arguments and the same confirm-before-Land and
// confirm-before-Discard gates). The plan's own risk table names "the split
// breaks the review loop" as the high-impact risk of this phase; the tests next
// to this file are the mitigation.

import { useEffect, useState } from 'react';
import type { BoardColumn, BoardTask } from '../../api/types';
import type { PatchBoardTaskInput } from '../../api';
import { discardBoardTask, landBoardTask, rerunBoardTask, verifyBoardTask } from '../../api';
import { ConfirmDialog } from '../../components/ui';
import {
  BOARD_COLUMNS,
  BOARD_LANES,
  COLUMN_LABELS,
  laneOf,
  LANE_TITLES,
} from '../boardModel';
import { FieldLabel } from '../TaskFields';

const BTN = 'rounded-lg px-3 py-1.5 text-[12px] transition-colors disabled:opacity-40';
const PRIMARY = `${BTN} border border-brand/50 bg-brand/10 font-semibold text-brand hover:bg-brand/20 disabled:cursor-not-allowed`;
const PLAIN = `${BTN} border border-line bg-surface text-ink-2 hover:bg-surface2 disabled:cursor-not-allowed`;
const DANGER = `${BTN} border border-red/40 bg-red/5 text-red hover:bg-red/15`;

/** The two history columns, grouped apart in the move menu (`laneOf` is null). */
const HISTORY_COLUMNS: BoardColumn[] = ['done', 'archived'];

/**
 * The lanes whose cards can still be decided in the review loop. `done` is in
 * the set on purpose: a shipped-but-wrong card must stay re-runnable and its
 * branch discardable, which is why the loop was never keyed on `in_review`
 * alone.
 */
function reviewable(task: BoardTask): boolean {
  return task.boardColumn === 'in_review' || task.boardColumn === 'done';
}

export function TaskActions({
  task,
  onPatch,
  onDelete,
  onClose,
  onConfirming,
}: {
  task: BoardTask;
  onPatch: (patch: PatchBoardTaskInput) => Promise<BoardTask>;
  onDelete: () => Promise<void>;
  onClose: () => void;
  /**
   * Reports whether one of the three confirm dialogs is up. The modal above
   * owns the Escape key, and one Escape must not dismiss both layers — the
   * reader would lose the dialog they were reading. The dialogs live here now,
   * so the flag has to travel.
   */
  onConfirming?: ((open: boolean) => void) | undefined;
}): JSX.Element {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [menu, setMenu] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  // Review-loop state. `reviewBusy` is one flag for all four actions: they are
  // mutually exclusive decisions about the same card, and letting a user click
  // Discard while Land is mid-push is not a state worth supporting.
  const [reviewOpen, setReviewOpen] = useState(false);
  const [feedback, setFeedback] = useState('');
  const [reviewBusy, setReviewBusy] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);
  const [reviewNote, setReviewNote] = useState<string | null>(null);
  const [confirmLand, setConfirmLand] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);

  const confirming = confirmDelete || confirmLand || confirmDiscard;
  useEffect(() => {
    if (onConfirming !== undefined) onConfirming(confirming);
  }, [confirming]);

  const run = (patch: PatchBoardTaskInput): void => {
    setBusy(true);
    setError(null);
    setMenu(false);
    onPatch(patch)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  const remove = (): void => {
    setDeleting(true);
    setDeleteError(null);
    onDelete()
      // The row is gone — close the whole modal, not just the dialog.
      .then(onClose)
      .catch((e: unknown) => {
        setDeleteError(e instanceof Error ? e.message : String(e));
        setDeleting(false);
      });
  };

  /**
   * Runs one review action, funnelling every server 4xx into `reviewError`. The
   * server's messages are written to be read (a 422 from Land carries the exact
   * commands to finish by hand), so they are shown verbatim rather than
   * re-worded here. The board's WS subscription refreshes the card itself.
   */
  const runReview = (action: () => Promise<string | null>): void => {
    setReviewBusy(true);
    setReviewError(null);
    setReviewNote(null);
    action()
      .then((note) => {
        setReviewNote(note);
        setConfirmLand(false);
        setConfirmDiscard(false);
      })
      .catch((e: unknown) => setReviewError(e instanceof Error ? e.message : String(e)))
      .finally(() => setReviewBusy(false));
  };

  const reverify = (): void => {
    runReview(async () => {
      await verifyBoardTask(task.id);
      // 202: the verdict lands later, on a task_updated frame.
      return 're-verification started — the verdict will appear here when it finishes';
    });
  };

  const rerun = (): void => {
    const text = feedback.trim();
    if (text === '') {
      setReviewError('feedback is required — a re-run with no notes would repeat the same work');
      return;
    }
    runReview(async () => {
      await rerunBoardTask(task.id, text);
      setFeedback('');
      return 'sent back to todo with your feedback appended to the prompt';
    });
  };

  const land = (): void => {
    runReview(async () => {
      const res = await landBoardTask(task.id);
      return `pull request opened: ${res.prUrl}`;
    });
  };

  const discard = (): void => {
    runReview(async () => {
      const res = await discardBoardTask(task.id);
      return res.deleted
        ? `branch ${res.branch} deleted — card archived`
        : `branch ${res.branch} was already gone — card archived`;
    });
  };

  const pauseLabel = task.userPaused ? '▶ Resume' : '❙❙ Pause';
  const togglePause = (): void => run({ userPaused: !task.userPaused });

  return (
    <div className="flex flex-col gap-2 border-t border-line px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <PrimaryAction
          task={task}
          busy={busy}
          pauseLabel={pauseLabel}
          onRun={() => run({ boardColumn: 'todo' })}
          onTogglePause={togglePause}
          onArchive={() => run({ boardColumn: 'archived' })}
          onRestore={() => run({ boardColumn: 'triage' })}
          onReview={() => setReviewOpen((v) => !v)}
          reviewOpen={reviewOpen}
        />
        {/* A done card is still decidable — the loop is reachable without
          * making it the thing the lane is for. */}
        {task.boardColumn === 'done' && (
          <button type="button" onClick={() => setReviewOpen((v) => !v)} className={PLAIN}>
            ◇ Review…
          </button>
        )}
        <button
          type="button"
          aria-label="more actions"
          aria-expanded={menu}
          onClick={() => setMenu((v) => !v)}
          className={`${PLAIN} ml-auto`}
        >
          …
        </button>
      </div>

      {menu && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border border-line bg-surface px-2.5 py-2">
          <label className="flex items-center gap-1.5 font-mono text-[10.5px] text-ink-faint">
            move to
            <select
              value={task.boardColumn}
              aria-label="move task to column"
              onChange={(e) => {
                const to = e.target.value as BoardColumn;
                if (to !== task.boardColumn) run({ boardColumn: to });
              }}
              className="rounded-md border border-line bg-field px-1.5 py-[2px] font-mono text-[10.5px] text-ink-dim outline-none focus:border-ink-dim"
            >
              {BOARD_LANES.map((lane) => (
                <optgroup key={lane} label={LANE_TITLES[lane]}>
                  {BOARD_COLUMNS.filter((c) => laneOf(c) === lane).map((c) => (
                    <option key={c} value={c}>
                      {COLUMN_LABELS[c]}
                    </option>
                  ))}
                </optgroup>
              ))}
              <optgroup label="History">
                {HISTORY_COLUMNS.map((c) => (
                  <option key={c} value={c}>
                    {COLUMN_LABELS[c]}
                  </option>
                ))}
              </optgroup>
            </select>
          </label>
          {task.boardColumn !== 'archived' && (
            <button
              type="button"
              disabled={busy}
              onClick={() => run({ boardColumn: 'archived' })}
              className={PLAIN}
            >
              Archive
            </button>
          )}
          <button type="button" disabled={busy} onClick={togglePause} className={PLAIN}>
            {pauseLabel}
          </button>
          {/* Sits apart from the reversible actions: this one has no undo. */}
          <button
            type="button"
            disabled={busy || deleting}
            onClick={() => {
              setDeleteError(null);
              setMenu(false);
              setConfirmDelete(true);
            }}
            className={`${DANGER} ml-auto`}
          >
            Delete
          </button>
        </div>
      )}

      {error !== null && <div className="font-mono text-[10.5px] text-red">{error}</div>}

      {reviewOpen && reviewable(task) && (
        <div className="mt-1 flex flex-col gap-2 border-t border-line pt-3">
          <FieldLabel>review</FieldLabel>

          {/* A card whose worktree was reclaimed cannot be re-graded — the
            * verifier has nothing to run against. Disabled WITH the reason,
            * rather than hidden: the button's absence would read as a bug. */}
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={reviewBusy || task.worktreePath === null}
              onClick={reverify}
              title={
                task.worktreePath === null
                  ? 'the worktree was reclaimed, so there is nothing left to grade — re-run the card instead'
                  : 'run verification again against the worktree'
              }
              className={PLAIN}
            >
              Re-verify
            </button>
            {task.worktreePath === null && (
              <span className="font-mono text-[10px] text-ink-faint">
                worktree reclaimed — nothing to re-grade
              </span>
            )}
          </div>

          {/* Re-run needs its notes before it can do anything, so the textarea
            * sits with the button rather than behind a dialog. */}
          <div>
            <FieldLabel>reviewer feedback</FieldLabel>
            <textarea
              value={feedback}
              onChange={(e) => setFeedback(e.target.value)}
              rows={3}
              aria-label="reviewer feedback"
              placeholder="what to fix on the next pass — appended to the prompt"
              className="w-full resize-y rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11.5px] leading-relaxed text-ink outline-none focus:border-ink-dim"
            />
          </div>

          {reviewError !== null && (
            <div className="rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] whitespace-pre-wrap text-red">
              {reviewError}
            </div>
          )}
          {reviewNote !== null && (
            <div className="rounded-lg border border-green/25 bg-green/5 px-2.5 py-2 font-mono text-[11px] break-all text-green">
              {reviewNote}
            </div>
          )}

          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              disabled={reviewBusy || task.branch === null}
              onClick={() => setConfirmLand(true)}
              title={
                task.branch === null
                  ? 'this card has no run branch — there is nothing to push'
                  : 'push the branch and open a pull request'
              }
              className={PRIMARY}
            >
              Land
            </button>
            <button
              type="button"
              disabled={reviewBusy || feedback.trim() === ''}
              onClick={rerun}
              title={
                feedback.trim() === ''
                  ? 'write the feedback first — a re-run with no notes repeats the same work'
                  : 'append the feedback to the prompt and send the card back to todo'
              }
              className={PLAIN}
            >
              Re-run with feedback
            </button>
            <button
              type="button"
              disabled={reviewBusy}
              onClick={() => setConfirmDiscard(true)}
              className={`${DANGER} ml-auto`}
            >
              Discard
            </button>
          </div>

          {/* The landed PR. `result_note` also carries a dispatcher sentinel
            * line on a no-op exit, so it is only linked when it IS a URL. */}
          {task.resultNote !== null && task.resultNote !== '' && (
            <div className="font-mono text-[10.5px] break-all text-ink-dim">
              {task.resultNote.startsWith('http') ? (
                <a
                  href={task.resultNote}
                  target="_blank"
                  rel="noreferrer"
                  className="underline transition-colors hover:text-ink"
                >
                  ❯ {task.resultNote}
                </a>
              ) : (
                task.resultNote
              )}
            </div>
          )}

          <ConfirmDialog
            open={confirmLand}
            title={`Land ${task.externalId}?`}
            confirmLabel="land"
            busy={reviewBusy}
            onConfirm={land}
            onCancel={() => setConfirmLand(false)}
          >
            Pushes <span className="font-mono text-[12px] text-ink">{task.branch ?? '—'}</span> to{' '}
            <span className="font-mono text-[12px] text-ink">origin</span> and opens a pull request
            with <span className="font-mono">gh</span>, then moves the card to done. The branch is
            kept.
            {reviewError !== null && (
              <div className="mt-2.5 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] whitespace-pre-wrap text-red">
                {reviewError}
              </div>
            )}
          </ConfirmDialog>

          <ConfirmDialog
            open={confirmDiscard}
            title={`Discard ${task.externalId}?`}
            confirmLabel="discard"
            danger
            busy={reviewBusy}
            onConfirm={discard}
            onCancel={() => setConfirmDiscard(false)}
          >
            Deletes the branch{' '}
            <span className="font-mono text-[12px] text-ink">{task.branch ?? '—'}</span> and every
            commit on it, reclaims the worktree, and archives the card. The work is not recoverable.
            {reviewError !== null && (
              <div className="mt-2.5 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] whitespace-pre-wrap text-red">
                {reviewError}
              </div>
            )}
          </ConfirmDialog>
        </div>
      )}

      <ConfirmDialog
        open={confirmDelete}
        title={`Delete ${task.externalId}?`}
        confirmLabel="delete"
        danger
        busy={deleting}
        onConfirm={remove}
        onCancel={() => {
          setConfirmDelete(false);
          setDeleteError(null);
        }}
      >
        <span className="font-mono text-[12px] text-ink">{task.title}</span> is removed permanently
        — this cannot be undone. To keep it out of the way without losing it, use{' '}
        <span className="font-mono">Archive</span> instead.
        {deleteError !== null && (
          <div className="mt-2.5 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red">
            {deleteError}
          </div>
        )}
      </ConfirmDialog>
    </div>
  );
}

/**
 * The ONE action the card's lane is for.
 *
 * Total over `BoardColumn` by construction — the switch returns on every arm, so
 * a new column gets a compile error here rather than a modal with no verb in it.
 */
function PrimaryAction({
  task,
  busy,
  pauseLabel,
  onRun,
  onTogglePause,
  onArchive,
  onRestore,
  onReview,
  reviewOpen,
}: {
  task: BoardTask;
  busy: boolean;
  pauseLabel: string;
  onRun: () => void;
  onTogglePause: () => void;
  onArchive: () => void;
  onRestore: () => void;
  onReview: () => void;
  reviewOpen: boolean;
}): JSX.Element {
  switch (task.boardColumn) {
    case 'triage':
      return (
        <button
          type="button"
          disabled={busy}
          onClick={onRun}
          title="accept into the Working queue — the dispatcher picks it up"
          className={PRIMARY}
        >
          ▶ Run
        </button>
      );
    case 'todo':
    case 'in_progress':
      // No Stop: nothing in the API stops a run in place (discard refuses while
      // the card is running), and a pause is the honest version of the verb —
      // the current stage finishes, nothing new starts.
      return (
        <button type="button" disabled={busy} onClick={onTogglePause} className={PRIMARY}>
          {pauseLabel}
        </button>
      );
    case 'in_review':
      return (
        <button type="button" aria-expanded={reviewOpen} onClick={onReview} className={PRIMARY}>
          ◇ Review…
        </button>
      );
    case 'done':
      return (
        <button type="button" disabled={busy} onClick={onArchive} className={PRIMARY}>
          Archive
        </button>
      );
    case 'archived':
      return (
        <button
          type="button"
          disabled={busy}
          onClick={onRestore}
          title="put the card back in the Inbox"
          className={PRIMARY}
        >
          ↩ Restore
        </button>
      );
  }
}
