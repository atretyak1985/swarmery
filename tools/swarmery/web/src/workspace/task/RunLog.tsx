// What HAPPENED when this card ran (board redesign v2, phase 2) — a tab that
// exists only for a card that has run.
//
// This is the block the phase was called for. The old modal rendered it
// unconditionally, so the card that had been sitting in the Inbox for twelve
// days showed "status queued / branch — / worktree —": three rows, no
// information, and the one word in them that looked like information (`queued`,
// the dispatcher's own column value) was not even the state the board had the
// card in. Now the tab is absent until there is a run to describe, and the state
// it does describe comes from `stateLabel`, never from the raw `status` field.
//
// `dispatchedPrompt` (0066) is the audit this panel exists to make reachable:
// the exact first-stage prompt the runner was handed, assembled from the title,
// the prompt and the captured quote. It is collapsed, because it is long and it
// is only interesting when the run did something unexpected.

import { useState } from 'react';
import type { BoardTask } from '../../api/types';
import { displaySlug, findProject } from '../../lib/projectSlug';
import { useScope } from '../../lib/scope';
import { stateLabel } from '../boardModel';
import { TaskDiff } from '../TaskDiff';
import { FieldLabel } from '../TaskFields';

/**
 * Whether this card has a run to show — the tab's whole existence test.
 *
 * Deliberately generous: any ONE of these means the dispatcher has touched the
 * card, and a card the dispatcher has touched has a history worth a tab even if
 * the run failed before it produced a branch (a `dispatchError` alone is the
 * case that matters most). Verify retries count for the same reason dispatch
 * retries do — a fix chain is run history.
 */
export function hasRunLog(task: BoardTask): boolean {
  return (
    task.branch !== null ||
    task.worktreePath !== null ||
    task.startPoint !== null ||
    task.retryCount > 0 ||
    task.verifyRetryCount > 0 ||
    task.verifyVerdict !== null ||
    task.dispatchError !== null ||
    task.dispatchedPrompt !== null
  );
}

/**
 * One labelled fact of the run. Unlike the row it replaces, it takes a
 * non-nullable value: the caller decides whether there is anything to say, so
 * this can no longer render the em-dash placeholder that filled the old panel.
 */
function LogRow({ label, value }: { label: string; value: string }): JSX.Element {
  return (
    <div className="flex items-baseline gap-2 py-1 font-mono text-[10.5px]">
      <span className="w-[92px] shrink-0 tracking-[0.08em] text-ink-faint uppercase">{label}</span>
      <span className="min-w-0 flex-1 break-all text-ink-2">{value}</span>
    </div>
  );
}

/** The prompt the runner actually received, behind a disclosure. */
function DispatchedPrompt({ prompt }: { prompt: string }): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <div className="mt-1.5">
      <button
        type="button"
        aria-expanded={open}
        aria-label="dispatched prompt"
        onClick={() => setOpen((v) => !v)}
        className="font-mono text-[10px] tracking-[0.08em] text-ink-faint uppercase transition-colors hover:text-ink-dim"
      >
        {open ? '▾' : '▸'} dispatched prompt
      </button>
      {open && (
        <pre className="mt-1.5 max-h-64 overflow-auto rounded-md border border-line bg-field px-2.5 py-2 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-dim">
          {prompt}
        </pre>
      )}
    </div>
  );
}

export function RunLog({
  task,
  onOpenTerminal,
}: {
  task: BoardTask;
  /** Open a terminal in this card's worktree; omitted when there is none. */
  onOpenTerminal?: (() => void) | undefined;
}): JSX.Element {
  // The linked-sessions link: the row carries the DB path slug, so use the
  // pretty one when the project resolves.
  const { projects } = useScope();
  const scopeProject = findProject(projects, task.projectSlug);
  const scopeSlug = scopeProject !== null ? displaySlug(scopeProject, projects) : task.projectSlug;
  return (
    <div className="flex flex-col gap-1">
      <FieldLabel>run</FieldLabel>
      <div>
        <LogRow label="state" value={stateLabel(task)} />
        {task.branch !== null && <LogRow label="branch" value={task.branch} />}
        {task.worktreePath !== null && <LogRow label="worktree" value={task.worktreePath} />}
        {task.startPoint !== null && <LogRow label="start point" value={task.startPoint} />}
        {/* Two budgets, two labels. A bare "retries: 3" could not say whether the
         * dispatcher healed a dead process three times or verification spawned
         * three fix cards — opposite situations needing opposite responses. */}
        {task.retryCount > 0 && <LogRow label="dispatch retries" value={String(task.retryCount)} />}
        {task.verifyRetryCount > 0 && (
          <LogRow label="verify retries" value={String(task.verifyRetryCount)} />
        )}
        {task.verifyVerdict !== null && <LogRow label="verdict" value={task.verifyVerdict} />}
        {task.verifyDetail !== null && <LogRow label="detail" value={task.verifyDetail} />}
      </div>

      {onOpenTerminal !== undefined && (
        <button
          type="button"
          onClick={onOpenTerminal}
          className="mt-1 flex w-fit items-center gap-1.5 rounded-md border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:border-line-strong hover:bg-surface2 hover:text-ink"
        >
          <span aria-hidden="true">❯_</span>
          Open terminal in worktree
        </button>
      )}

      {task.dispatchError !== null && (
        <div className="mt-1 rounded-md border border-red/30 bg-red/5 px-2 py-1.5 font-mono text-[10.5px] whitespace-pre-wrap text-red">
          {task.dispatchError}
        </div>
      )}

      {task.dispatchedPrompt !== null && task.dispatchedPrompt !== '' && (
        <DispatchedPrompt prompt={task.dispatchedPrompt} />
      )}

      {task.branch !== null && scopeSlug !== null && (
        <a
          href={`/sessions?scope=${scopeSlug}`}
          className="mt-1.5 inline-block w-fit font-mono text-[10.5px] text-ink-dim underline transition-colors hover:text-ink"
        >
          ❯ linked sessions →
        </a>
      )}

      <div className="mt-2">
        <TaskDiff taskId={task.id} />
      </div>
    </div>
  );
}
