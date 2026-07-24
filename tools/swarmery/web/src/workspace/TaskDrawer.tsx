// Task detail drawer (fusion phase 4): a right-side drawer over the board.
// Editable: title, prompt, priority, model, file scope (chips), dependencies
// (chips of T-ids). Actions: Move to Todo, Pause/Resume (user_paused), Archive.
// Read-only (dispatcher-owned): branch, worktree path, dispatch error, verdict
// + detail, and a link to the linked session's list. Every mutation goes
// through the board's patchTask so the card + status bar stay in sync.

import { useEffect, useMemo, useRef, useState } from 'react';
import type { BoardTask, TaskPriority } from '../api/types';
import type { PatchBoardTaskInput } from '../api';
import { fmtAgo } from '../lib/format';
import { PlaybookHint, PlaybookSelect, usePlaybooks } from './PlaybookPicker';
import { useWorkspaceTerminal } from './ProjectWorkspaceLayout';

const PRIORITIES: TaskPriority[] = ['urgent', 'high', 'normal', 'low'];
// Model tokens the dispatcher passes to `claude --model`; default = inherit.
const MODELS = ['default', 'fable', 'opus', 'sonnet', 'haiku'] as const;

/** An editable list-of-strings field rendered as removable chips + an add input. */
function ChipEditor({
  label,
  values,
  placeholder,
  onChange,
}: {
  label: string;
  values: string[];
  placeholder: string;
  onChange: (next: string[]) => void;
}): JSX.Element {
  const [draft, setDraft] = useState('');
  const add = (): void => {
    const v = draft.trim();
    if (v === '' || values.includes(v)) {
      setDraft('');
      return;
    }
    onChange([...values, v]);
    setDraft('');
  };
  return (
    <div>
      <FieldLabel>{label}</FieldLabel>
      <div className="flex flex-wrap gap-1.5">
        {values.map((v) => (
          <span
            key={v}
            className="flex items-center gap-1 rounded-full border border-line bg-field px-2 py-0.5 font-mono text-[10.5px] text-ink-2"
          >
            {v}
            <button
              type="button"
              aria-label={`remove ${v}`}
              onClick={() => onChange(values.filter((x) => x !== v))}
              className="text-ink-faint transition-colors hover:text-red"
            >
              ×
            </button>
          </span>
        ))}
      </div>
      <input
        type="text"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            add();
          }
        }}
        onBlur={add}
        placeholder={placeholder}
        aria-label={label}
        className="mt-1.5 w-full rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11px] text-ink outline-none placeholder:text-ink-faint focus:border-ink-dim"
      />
    </div>
  );
}

function FieldLabel({ children }: { children: string }): JSX.Element {
  return (
    <div className="mb-1 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">{children}</div>
  );
}

function ReadOnlyRow({ label, value }: { label: string; value: string }): JSX.Element {
  return (
    <div className="flex items-baseline gap-2 py-1 font-mono text-[10.5px]">
      <span className="w-[92px] shrink-0 tracking-[0.08em] text-ink-faint uppercase">{label}</span>
      <span className="min-w-0 flex-1 break-all text-ink-2">{value}</span>
    </div>
  );
}

export function TaskDrawer({
  task,
  onClose,
  onPatch,
}: {
  task: BoardTask;
  onClose: () => void;
  /** Returns the patch promise so the drawer can surface a save error. */
  onPatch: (patch: PatchBoardTaskInput) => Promise<BoardTask>;
}): JSX.Element {
  const [title, setTitle] = useState(task.title);
  const [prompt, setPrompt] = useState(task.prompt);
  const [priority, setPriority] = useState<TaskPriority>(task.priority);
  const [model, setModel] = useState<string>(task.model ?? 'default');
  const [playbook, setPlaybook] = useState<string>(task.playbook ?? '');
  const [fileScope, setFileScope] = useState<string[]>(task.fileScope);
  const [dependencies, setDependencies] = useState<string[]>(task.dependencies);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const closeRef = useRef<HTMLButtonElement>(null);
  const { playbooks } = usePlaybooks(task.projectId);
  const openTerminal = useWorkspaceTerminal();

  // Re-seed local edit state when a different task is opened into the drawer.
  useEffect(() => {
    setTitle(task.title);
    setPrompt(task.prompt);
    setPriority(task.priority);
    setModel(task.model ?? 'default');
    setPlaybook(task.playbook ?? '');
    setFileScope(task.fileScope);
    setDependencies(task.dependencies);
    setSaveError(null);
  }, [task]);

  useEffect(() => {
    closeRef.current?.focus();
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  const dirty = useMemo(
    () =>
      title.trim() !== task.title ||
      prompt.trim() !== task.prompt ||
      priority !== task.priority ||
      (model === 'default' ? task.model !== null : model !== task.model) ||
      playbook !== (task.playbook ?? '') ||
      JSON.stringify(fileScope) !== JSON.stringify(task.fileScope) ||
      JSON.stringify(dependencies) !== JSON.stringify(task.dependencies),
    [title, prompt, priority, model, playbook, fileScope, dependencies, task],
  );

  const run = (patch: PatchBoardTaskInput): void => {
    setBusy(true);
    setSaveError(null);
    onPatch(patch)
      .catch((e: unknown) => setSaveError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  const save = (): void => {
    if (title.trim() === '' || prompt.trim() === '') {
      setSaveError('title and prompt cannot be empty');
      return;
    }
    run({
      title: title.trim(),
      prompt: prompt.trim(),
      priority,
      model: model === 'default' ? null : model,
      playbook, // "" clears back to the default recipe
      fileScope,
      dependencies,
    });
  };

  const blocked = task.paused || task.userPaused;

  return (
    <div className="fixed inset-0 z-40 flex justify-end" role="dialog" aria-modal="true" aria-label="task detail">
      <button
        type="button"
        aria-label="close drawer"
        onClick={onClose}
        className="flex-1 cursor-default bg-black/40"
      />
      <div className="flex h-full w-full max-w-[420px] flex-col overflow-y-auto border-l border-line bg-bg shadow-[0_0_40px_rgba(0,0,0,0.5)]">
        <div className="flex items-center gap-2 border-b border-line px-4 py-3">
          <span className="font-mono text-[10.5px] text-ink-faint">{task.externalId}</span>
          {task.verifyVerdict !== null && (
            <span className="font-mono text-[10px] text-ink-dim uppercase">· {task.verifyVerdict}</span>
          )}
          <button
            ref={closeRef}
            type="button"
            onClick={onClose}
            aria-label="close"
            className="ml-auto text-[15px] leading-none text-ink-dim transition-colors hover:text-ink"
          >
            ×
          </button>
        </div>

        <div className="flex flex-col gap-4 px-4 py-4">
          <div>
            <FieldLabel>title</FieldLabel>
            <input
              type="text"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              aria-label="title"
              className="w-full rounded-[8px] border border-line bg-field px-2.5 py-1.5 text-[13px] text-ink outline-none focus:border-ink-dim"
            />
          </div>

          <div>
            <FieldLabel>prompt</FieldLabel>
            <textarea
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              rows={5}
              aria-label="prompt"
              className="w-full resize-y rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11.5px] leading-relaxed text-ink outline-none focus:border-ink-dim"
            />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <FieldLabel>priority</FieldLabel>
              <select
                value={priority}
                onChange={(e) => setPriority(e.target.value as TaskPriority)}
                aria-label="priority"
                className="w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink outline-none focus:border-ink-dim"
              >
                {PRIORITIES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <FieldLabel>model</FieldLabel>
              <select
                value={model}
                onChange={(e) => setModel(e.target.value)}
                aria-label="model"
                className="w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink outline-none focus:border-ink-dim"
              >
                {MODELS.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div>
            <FieldLabel>playbook</FieldLabel>
            <PlaybookSelect playbooks={playbooks} value={playbook} onChange={setPlaybook} />
            <PlaybookHint playbooks={playbooks} value={playbook} />
          </div>

          <ChipEditor
            label="file scope"
            values={fileScope}
            placeholder="add a path glob + Enter"
            onChange={setFileScope}
          />
          <ChipEditor
            label="dependencies"
            values={dependencies}
            placeholder="add a T-id + Enter"
            onChange={setDependencies}
          />

          {saveError !== null && <div className="font-mono text-[10.5px] text-red">{saveError}</div>}

          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              disabled={!dirty || busy}
              onClick={save}
              className="rounded-lg border border-brand/50 bg-brand/10 px-3 py-1.5 text-[12px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:cursor-not-allowed disabled:opacity-40"
            >
              Save
            </button>
            {task.boardColumn !== 'todo' && task.boardColumn !== 'done' && (
              <button
                type="button"
                disabled={busy}
                onClick={() => run({ boardColumn: 'todo' })}
                className="rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-40"
              >
                Move to Todo
              </button>
            )}
            <button
              type="button"
              disabled={busy}
              onClick={() => run({ userPaused: !task.userPaused })}
              className="rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-40"
            >
              {task.userPaused ? 'Resume' : 'Pause'}
            </button>
            {task.boardColumn !== 'archived' && (
              <button
                type="button"
                disabled={busy}
                onClick={() => run({ boardColumn: 'archived' })}
                className="rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] text-ink-dim transition-colors hover:bg-surface2 disabled:opacity-40"
              >
                Archive
              </button>
            )}
          </div>

          {/* Read-only dispatcher-owned state. */}
          <div className="mt-1 border-t border-line pt-3">
            <FieldLabel>dispatcher</FieldLabel>
            <ReadOnlyRow label="status" value={task.status} />
            {blocked && <ReadOnlyRow label="paused" value={task.userPaused ? 'by user' : 'by system'} />}
            <ReadOnlyRow label="branch" value={task.branch ?? '—'} />
            <ReadOnlyRow label="worktree" value={task.worktreePath ?? '—'} />
            {openTerminal !== null && task.worktreePath !== null && (
              <button
                type="button"
                onClick={() => openTerminal(task.externalId, task.worktreePath as string)}
                className="mt-1 flex items-center gap-1.5 rounded-md border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:border-line-strong hover:bg-surface2 hover:text-ink"
              >
                <span aria-hidden="true">❯_</span>
                Open terminal in worktree
              </button>
            )}
            {task.retryCount > 0 && <ReadOnlyRow label="retries" value={String(task.retryCount)} />}
            {task.dispatchError !== null && (
              <div className="mt-1 rounded-md border border-red/30 bg-red/5 px-2 py-1.5 font-mono text-[10.5px] text-red">
                {task.dispatchError}
              </div>
            )}
            {task.verifyVerdict !== null && (
              <div className="mt-1.5">
                <ReadOnlyRow label="verdict" value={task.verifyVerdict} />
                {task.verifyDetail !== null && <ReadOnlyRow label="detail" value={task.verifyDetail} />}
              </div>
            )}
            {task.branch !== null && task.projectSlug !== null && (
              <a
                href={`/sessions?scope=${task.projectSlug}`}
                className="mt-1.5 inline-block font-mono text-[10.5px] text-ink-dim underline transition-colors hover:text-ink"
              >
                ❯ linked sessions →
              </a>
            )}
            <div className="mt-2 font-mono text-[10px] text-ink-faint">created {fmtAgo(task.createdAt)}</div>
          </div>
        </div>
      </div>
    </div>
  );
}
