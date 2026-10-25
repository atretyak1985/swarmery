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

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useId, useRef, useState } from 'react';
import type { BoardColumn, BoardTask } from '../../api/types';
import type { CloseNoChangeReason, PatchBoardTaskInput } from '../../api';
import {
  closeBoardTaskNoChange,
  discardBoardTask,
  landBoardTask,
  rerunBoardTask,
  verifyBoardTask,
} from '../../api';
import { ConfirmDialog } from '../../components/ui';
import {
  BOARD_COLUMNS,
  BOARD_LANES,
  COLUMN_LABELS,
  laneOf,
  LANE_TITLES,
} from '../boardModel';
import { safeHref } from '../../lib/safeHref';
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

/**
 * Whether a card can be closed as "no change needed". Not while it runs (the
 * server answers 409), and not once it is already filed in done or archived —
 * there is nothing left to close.
 */
function closableNoChange(task: BoardTask): boolean {
  const running = task.boardColumn === 'in_progress' || task.status === 'running';
  return !running && task.boardColumn !== 'done' && task.boardColumn !== 'archived';
}

const CLOSE_REASONS: readonly CloseNoChangeReason[] = ['already_fixed', 'not_a_defect', 'duplicate'];

const FIELD_LABEL = 'mb-1 block font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase';
const ERROR_BOX =
  'rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] whitespace-pre-wrap text-red';
const NOTE_BOX =
  'rounded-lg border border-green/25 bg-green/5 px-2.5 py-2 font-mono text-[11px] break-all text-green';

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
  const { t } = useLingui();
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
  // Close-no-change state. Inline, not a dialog: the operator writes evidence,
  // and that reads better in place than in an overlay.
  const [closeOpen, setCloseOpen] = useState(false);
  const [closeReason, setCloseReason] = useState<CloseNoChangeReason>('already_fixed');
  const [closeEvidence, setCloseEvidence] = useState('');
  const [closeBusy, setCloseBusy] = useState(false);
  const [closeError, setCloseError] = useState<string | null>(null);
  const [closeNote, setCloseNote] = useState<string | null>(null);
  const closeToggleRef = useRef<HTMLButtonElement>(null);
  const closeReasonRef = useRef<HTMLSelectElement>(null);
  const closeReasonId = useId();
  const closeEvidenceId = useId();

  // Opening the form moves focus into it, so a keyboard user lands on the
  // first field rather than having to tab past the rest of the action bar.
  useEffect(() => {
    if (closeOpen) closeReasonRef.current?.focus();
  }, [closeOpen]);

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
      return t`re-verification started — the verdict will appear here when it finishes`;
    });
  };

  const rerun = (): void => {
    const text = feedback.trim();
    if (text === '') {
      setReviewError(t`feedback is required — a re-run with no notes would repeat the same work`);
      return;
    }
    runReview(async () => {
      await rerunBoardTask(task.id, text);
      setFeedback('');
      return t`sent back to todo with your feedback appended to the prompt`;
    });
  };

  const land = (): void => {
    runReview(async () => {
      const res = await landBoardTask(task.id);
      const prUrl = res.prUrl;
      return t`pull request opened: ${prUrl}`;
    });
  };

  const discard = (): void => {
    runReview(async () => {
      const res = await discardBoardTask(task.id);
      const branch = res.branch;
      return res.deleted
        ? t`branch ${branch} deleted — card archived`
        : t`branch ${branch} was already gone — card archived`;
    });
  };

  const reasonLabels: Record<CloseNoChangeReason, string> = {
    already_fixed: t`already fixed`,
    not_a_defect: t`not a defect`,
    duplicate: t`duplicate`,
  };

  const cancelClose = (): void => {
    setCloseOpen(false);
    setCloseReason('already_fixed');
    setCloseEvidence('');
    setCloseError(null);
    closeToggleRef.current?.focus();
  };

  /**
   * Closes the card with no change. The server's 4xx sentence is shown
   * verbatim, as the review actions do; on success the board's WS frame moves
   * the card to done, which also hides this action.
   */
  const closeNoChange = (): void => {
    const evidence = closeEvidence.trim();
    if (evidence === '') return;
    setCloseBusy(true);
    setCloseError(null);
    setCloseNote(null);
    closeBoardTaskNoChange(task.id, { reason: closeReason, evidence })
      .then(() => {
        setCloseOpen(false);
        setCloseEvidence('');
        setCloseReason('already_fixed');
        setCloseNote(t`closed with no change needed — card moved to done`);
      })
      .catch((e: unknown) => setCloseError(e instanceof Error ? e.message : String(e)))
      .finally(() => setCloseBusy(false));
  };

  const pauseLabel = task.userPaused ? t`▶ Resume` : t`❙❙ Pause`;
  const externalId = task.externalId;
  const taskBranch = task.branch ?? '—';
  const taskTitle = task.title;
  const togglePause = (): void => run({ userPaused: !task.userPaused });
  const resultHref = safeHref(task.resultNote);

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
            <Trans>◇ Review…</Trans>
          </button>
        )}
        {closableNoChange(task) && (
          <button
            ref={closeToggleRef}
            type="button"
            aria-expanded={closeOpen}
            onClick={() => (closeOpen ? cancelClose() : setCloseOpen(true))}
            title={t`close the card without a change — already fixed, not a defect, or a duplicate`}
            className={PLAIN}
          >
            {/* A colon, not an em dash: the modal's never-run test reads any
              * em dash in the body as a placeholder standing in for a fact. */}
            <Trans>Close: no change needed</Trans>
          </button>
        )}
        <button
          type="button"
          aria-label={t`more actions`}
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
            <Trans>move to</Trans>
            <select
              value={task.boardColumn}
              aria-label={t`move task to column`}
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
              <optgroup label={t`History`}>
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
              <Trans>Archive</Trans>
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
            <Trans>Delete</Trans>
          </button>
        </div>
      )}

      {error !== null && <div className="font-mono text-[10.5px] text-red">{error}</div>}

      {closeOpen && closableNoChange(task) && (
        <form
          aria-label={t`close with no change needed`}
          onSubmit={(e) => {
            e.preventDefault();
            closeNoChange();
          }}
          className="mt-1 flex flex-col gap-2 border-t border-line pt-3"
        >
          <div>
            <label htmlFor={closeReasonId} className={FIELD_LABEL}>
              <Trans>reason</Trans>
            </label>
            <select
              id={closeReasonId}
              ref={closeReasonRef}
              value={closeReason}
              onChange={(e) => setCloseReason(e.target.value as CloseNoChangeReason)}
              className="rounded-md border border-line bg-field px-1.5 py-[3px] font-mono text-[11px] text-ink-dim outline-none focus:border-ink-dim"
            >
              {CLOSE_REASONS.map((r) => (
                <option key={r} value={r}>
                  {reasonLabels[r]}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label htmlFor={closeEvidenceId} className={FIELD_LABEL}>
              <Trans>evidence</Trans>
            </label>
            <textarea
              id={closeEvidenceId}
              value={closeEvidence}
              onChange={(e) => setCloseEvidence(e.target.value)}
              rows={3}
              required
              placeholder={t`what shows no change is needed — the fixing commit, the duplicate card, the reasoning`}
              className="w-full resize-y rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11.5px] leading-relaxed text-ink outline-none focus:border-ink-dim"
            />
          </div>
          {closeError !== null && (
            <div role="alert" className={ERROR_BOX}>
              {closeError}
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            <button
              type="submit"
              disabled={closeBusy || closeEvidence.trim() === ''}
              title={
                closeEvidence.trim() === ''
                  ? t`write the evidence first — a close with no evidence cannot be audited`
                  : t`close the card and move it to done`
              }
              className={PRIMARY}
            >
              <Trans>Confirm</Trans>
            </button>
            <button type="button" onClick={cancelClose} className={PLAIN}>
              <Trans>Cancel</Trans>
            </button>
          </div>
        </form>
      )}
      {closeNote !== null && <div className={NOTE_BOX}>{closeNote}</div>}

      {reviewOpen && reviewable(task) && (
        <div className="mt-1 flex flex-col gap-2 border-t border-line pt-3">
          <FieldLabel>{t`review`}</FieldLabel>

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
                  ? t`the worktree was reclaimed, so there is nothing left to grade — re-run the card instead`
                  : t`run verification again against the worktree`
              }
              className={PLAIN}
            >
              <Trans>Re-verify</Trans>
            </button>
            {task.worktreePath === null && (
              <span className="font-mono text-[10px] text-ink-faint">
                <Trans>worktree reclaimed — nothing to re-grade</Trans>
              </span>
            )}
          </div>

          {/* Re-run needs its notes before it can do anything, so the textarea
            * sits with the button rather than behind a dialog. */}
          <div>
            <FieldLabel>{t`reviewer feedback`}</FieldLabel>
            <textarea
              value={feedback}
              onChange={(e) => setFeedback(e.target.value)}
              rows={3}
              aria-label={t`reviewer feedback`}
              placeholder={t`what to fix on the next pass — appended to the prompt`}
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
                  ? t`this card has no run branch — there is nothing to push`
                  : t`push the branch and open a pull request`
              }
              className={PRIMARY}
            >
              <Trans>Land</Trans>
            </button>
            <button
              type="button"
              disabled={reviewBusy || feedback.trim() === ''}
              onClick={rerun}
              title={
                feedback.trim() === ''
                  ? t`write the feedback first — a re-run with no notes repeats the same work`
                  : t`append the feedback to the prompt and send the card back to todo`
              }
              className={PLAIN}
            >
              <Trans>Re-run with feedback</Trans>
            </button>
            <button
              type="button"
              disabled={reviewBusy}
              onClick={() => setConfirmDiscard(true)}
              className={`${DANGER} ml-auto`}
            >
              <Trans>Discard</Trans>
            </button>
          </div>

          {/* The landed PR. `result_note` also carries a dispatcher sentinel
            * line on a no-op exit, so it is only linked when it IS a URL. */}
          {task.resultNote !== null && task.resultNote !== '' && (
            <div className="font-mono text-[10.5px] break-all text-ink-dim">
              {resultHref !== null ? (
                <a
                  href={resultHref}
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
            title={t`Land ${externalId}?`}
            confirmLabel={t`land`}
            busy={reviewBusy}
            onConfirm={land}
            onCancel={() => setConfirmLand(false)}
          >
            <Trans>
              Pushes <span className="font-mono text-[12px] text-ink">{taskBranch}</span> to{' '}
              <span className="font-mono text-[12px] text-ink">origin</span> and opens a pull request with{' '}
              <span className="font-mono">gh</span>, then moves the card to done. The branch is kept.
            </Trans>
            {reviewError !== null && (
              <div className="mt-2.5 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] whitespace-pre-wrap text-red">
                {reviewError}
              </div>
            )}
          </ConfirmDialog>

          <ConfirmDialog
            open={confirmDiscard}
            title={t`Discard ${externalId}?`}
            confirmLabel={t`discard`}
            danger
            busy={reviewBusy}
            onConfirm={discard}
            onCancel={() => setConfirmDiscard(false)}
          >
            <Trans>
              Deletes the branch <span className="font-mono text-[12px] text-ink">{taskBranch}</span> and every
              commit on it, reclaims the worktree, and archives the card. The work is not recoverable.
            </Trans>
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
        title={t`Delete ${externalId}?`}
        confirmLabel={t`delete`}
        danger
        busy={deleting}
        onConfirm={remove}
        onCancel={() => {
          setConfirmDelete(false);
          setDeleteError(null);
        }}
      >
        <Trans>
          <span className="font-mono text-[12px] text-ink">{taskTitle}</span> is removed permanently — this
          cannot be undone. To keep it out of the way without losing it, use{' '}
          <span className="font-mono">Archive</span> instead.
        </Trans>
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
  const { t } = useLingui();
  switch (task.boardColumn) {
    case 'triage':
      return (
        <button
          type="button"
          disabled={busy}
          onClick={onRun}
          title={t`accept into the Working queue — the dispatcher picks it up`}
          className={PRIMARY}
        >
          <Trans>▶ Run</Trans>
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
          <Trans>◇ Review…</Trans>
        </button>
      );
    case 'done':
      return (
        <button type="button" disabled={busy} onClick={onArchive} className={PRIMARY}>
          <Trans>Archive</Trans>
        </button>
      );
    case 'archived':
      return (
        <button
          type="button"
          disabled={busy}
          onClick={onRestore}
          title={t`put the card back in the Inbox`}
          className={PRIMARY}
        >
          <Trans>↩ Restore</Trans>
        </button>
      );
  }
}
