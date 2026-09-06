// Task detail modal: the card's full editor, centred over the board.
//
// This file used to BE the editor — 688 lines holding every field, every verb
// and the review loop at one level of importance, so the card that opened it
// most often (captured, never dispatched) showed three rows of em-dashes about a
// run that never happened. It is now the shell; each panel under `task/` carries
// its own reasoning. Saving is automatic (task/useTaskDraft) on blur, on a
// select's change and on ⌘S — the Save button is gone, which is what lets
// TaskActions have a single primary.

import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import type { BoardTask } from '../api/types';
import type { PatchBoardTaskInput } from '../api';
import { fmtAgo } from '../lib/format';
import { useAgentRoster } from './AgentPicker';
import { stateLabel } from './boardModel';
import { usePlaybooks } from './PlaybookPicker';
import { useWorkspaceTerminal } from './ProjectWorkspaceLayout';
import { RunConfig } from './task/RunConfig';
import { hasRunLog, RunLog } from './task/RunLog';
import { TaskActions } from './task/TaskActions';
import { TaskBrief } from './task/TaskBrief';
import { useTaskDraft } from './task/useTaskDraft';

const TABS = [
  ['brief', 'brief'],
  ['log', 'run log'],
] as const;
const TAB = 'border-b px-2.5 py-1 font-mono text-[10.5px] tracking-[0.08em] uppercase transition-colors';
const TAB_ON = 'border-brand text-brand';
const TAB_OFF = 'border-transparent text-ink-faint hover:text-ink-dim';

export function TaskModal({
  task,
  onClose,
  onPatch,
  onDelete,
}: {
  task: BoardTask;
  onClose: () => void;
  /** Returns the patch promise so the modal can surface a save error. */
  onPatch: (patch: PatchBoardTaskInput) => Promise<BoardTask>;
  /** Permanent delete; rejects with the server's message (the 409 on a running
   * task) so the confirm dialog can stay open and show it. */
  onDelete: () => Promise<void>;
}): JSX.Element {
  const [tab, setTab] = useState<'brief' | 'log'>('brief');
  const [confirming, setConfirming] = useState(false);
  const closeRef = useRef<HTMLButtonElement>(null);
  const { playbooks } = usePlaybooks(task.projectId);
  const { agents } = useAgentRoster(task.projectId, task.projectSlug);
  const openTerminal = useWorkspaceTerminal();
  const { draft, setField, commit, saveError } = useTaskDraft(task, onPatch);
  // The tab is offered only for a card with a run behind it; `active` keeps the
  // body honest if that history vanishes underneath a selected tab.
  const logged = hasRunLog(task);
  const active = logged ? tab : 'brief';
  const wt = task.worktreePath;
  const terminal = openTerminal !== null && wt !== null ? () => openTerminal(task.externalId, wt) : undefined;

  // Initial focus is MOUNT-ONLY, deliberately split from the key listener below:
  // the two used to share one effect keyed on [onClose], and Board.tsx passes an
  // inline arrow, so every task_updated frame yanked focus back mid-typing.
  useEffect(() => {
    closeRef.current?.focus();
    setTab('brief');
  }, [task.id]);

  // Escape closes the modal — but not while a confirm is up, or one key would
  // dismiss both layers. ⌘S is the explicit save for an edit not yet blurred.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && !confirming) {
        onClose();
        return;
      }
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
        e.preventDefault();
        commit();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [onClose, confirming, commit]);

  return (
    <div
      className="fixed inset-0 z-40 flex items-center justify-center bg-bg/70 p-4"
      role="dialog" aria-modal="true" aria-label="task detail" onClick={onClose}
    >
      <div
        className="flex max-h-full w-full max-w-3xl flex-col overflow-hidden rounded-xl border border-line bg-bg shadow-[0_0_40px_rgba(0,0,0,0.5)]"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-line px-4 py-2.5">
          <span className="font-mono text-[10.5px] text-ink-faint">{task.externalId}</span>
          <span className="font-mono text-[10.5px] text-ink-2">· {stateLabel(task)}</span>
          <span className="font-mono text-[10px] text-ink-faint">· created {fmtAgo(task.createdAt)}</span>
          {/* The card's micro-plan: the same unit of work under the plans-flow
              honesty contract (ticked criteria + a Completion Report). */}
          {task.planExternalId !== null && (
            <Link
              to="/plans"
              data-tip={`plan ${task.planExternalId} — acceptance criteria and completion report`}
              className="rounded border border-line px-1 py-px font-mono text-[9px] text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
            >
              plan
            </Link>
          )}
          <button
            ref={closeRef} type="button" onClick={onClose} aria-label="close"
            className="ml-auto text-[15px] leading-none text-ink-dim transition-colors hover:text-ink"
          >
            ×
          </button>
        </div>

        {logged && (
          <div className="flex gap-1 border-b border-line px-3 pt-1.5">
            {TABS.map(([id, label]) => (
              <button
                key={id} type="button" aria-selected={active === id} onClick={() => setTab(id)}
                className={`${TAB} ${active === id ? TAB_ON : TAB_OFF}`}
              >
                {label}
              </button>
            ))}
          </div>
        )}

        <div className="flex flex-col gap-3 overflow-y-auto px-4 py-3">
          {active === 'brief' ? (
            <>
              <TaskBrief task={task} draft={draft} setField={setField} commit={commit} />
              <RunConfig draft={draft} setField={setField} commit={commit} playbooks={playbooks} agents={agents} />
            </>
          ) : (
            <RunLog task={task} onOpenTerminal={terminal} />
          )}
          {saveError !== null && (
            <div className="font-mono text-[10.5px] whitespace-pre-wrap text-red">{saveError}</div>
          )}
        </div>

        <TaskActions task={task} onPatch={onPatch} onDelete={onDelete} onClose={onClose} onConfirming={setConfirming} />
      </div>
    </div>
  );
}
