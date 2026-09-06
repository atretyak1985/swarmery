// New-task modal — the board's creation form, replacing the inline quick-entry
// input it grew out of (title + recipe only).
//
// Two fields are visible: the title and what needs doing. Everything else the
// dispatcher acts on — agent, priority, model, playbook, file scope,
// dependencies, labels, landing column — is settable at intake but lives under
// `advanced`, collapsed. That is the whole shape of this component: writing
// down a task is the common case, and it used to cost ten controls, nine of
// which have a working default. The collapsed section is NOT rendered (not
// merely hidden), so what is off-screen is also out of the tab order.
//
// Hidden state stays legible: the disclosure names every advanced value that
// is no longer at its default, and a `?compose=@agent:` deep link opens the
// section so the resolved agent is never a silent setting.
//
// Deep link: Agent Hub "Run now" still navigates to /p/{slug}/board?compose=@<agent>:
// — Board passes that text in as `initialText` and the modal resolves the
// leading "@<name>:" against the loaded roster: a hit pre-selects the agent and
// the remainder becomes the title; an unknown name is left in the text so
// nothing is silently dropped.

import { useEffect, useRef, useState } from 'react';
import type { AgentRosterRow, BoardColumn, BoardTask, TaskPriority } from '../api/types';
import { createBoardTask } from '../api';
import { AgentHint, AgentSelect, useAgentRoster } from './AgentPicker';
import { COLUMN_LABELS, LANE_TITLES, laneOf, TASK_MODELS, TASK_PRIORITIES } from './boardModel';
import { PlaybookHint, PlaybookSelect, usePlaybooks } from './PlaybookPicker';
import { ChipEditor, FieldLabel } from './TaskFields';

/** Columns a fresh card may land in: park it, or hand it to the dispatcher. */
const TARGET_COLUMNS: BoardColumn[] = ['triage', 'todo'];

const COLUMN_HINT: Record<string, string> = {
  triage: 'parked — dispatch it later from the board',
  todo: 'start immediately — the dispatcher picks it up',
};

/**
 * Split a `?compose=` seed into an agent selection + the remaining title.
 * The longest matching roster name wins so plugin-qualified names
 * ("pack:agent") are not truncated at their first colon. Unknown names stay in
 * the title verbatim.
 */
export function parseCompose(
  text: string,
  agents: AgentRosterRow[],
): { agent: string; title: string } {
  const raw = text.trimStart();
  if (!raw.startsWith('@')) return { agent: '', title: text.trim() };
  const body = raw.slice(1);
  const hit = agents
    .filter((a) => body.startsWith(`${a.name}:`))
    .sort((a, b) => b.name.length - a.name.length)[0];
  if (hit === undefined) return { agent: '', title: text.trim() };
  return { agent: hit.name, title: body.slice(hit.name.length + 1).trim() };
}

export function NewTaskModal({
  projectId,
  projectSlug,
  initialText = '',
  onCreated,
  onClose,
}: {
  projectId: number;
  /** DB slug of the project — scopes the agent roster to what the API accepts. */
  projectSlug: string | null;
  /** `?compose=` seed; parsed into agent + title once the roster resolves. */
  initialText?: string;
  /** Called with the created row so the board can insert it optimistically. */
  onCreated: (task: BoardTask) => void;
  onClose: () => void;
}): JSX.Element {
  const [title, setTitle] = useState(initialText.trim());
  const [prompt, setPrompt] = useState('');
  const [agent, setAgent] = useState('');
  const [model, setModel] = useState<string>('default');
  const [priority, setPriority] = useState<TaskPriority>('normal');
  const [playbook, setPlaybook] = useState('');
  const [fileScope, setFileScope] = useState<string[]>([]);
  const [dependencies, setDependencies] = useState<string[]>([]);
  const [labels, setLabels] = useState<string[]>([]);
  const [column, setColumn] = useState<BoardColumn>('triage');
  const [advanced, setAdvanced] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const { playbooks } = usePlaybooks(projectId);
  const { agents, loading: rosterLoading } = useAgentRoster(projectId, projectSlug);
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleRef = useRef<HTMLInputElement>(null);
  const composeApplied = useRef(false);

  // Resolve the compose seed ONCE, after the roster lands — before that no
  // "@name:" can be validated, and re-running it would fight the user's edits.
  useEffect(() => {
    if (composeApplied.current || rosterLoading || initialText === '') return;
    composeApplied.current = true;
    const parsed = parseCompose(initialText, agents);
    if (parsed.agent === '') return;
    setAgent(parsed.agent);
    setTitle(parsed.title);
    // The deep link SET something the collapsed form does not show. Open the
    // section rather than dispatch to an agent the operator never saw named.
    setAdvanced(true);
  }, [rosterLoading, agents, initialText]);

  useEffect(() => {
    titleRef.current?.focus();
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && !busy) onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [busy, onClose]);

  // Focus trap: Tab cycles inside the dialog instead of escaping to the board
  // behind it. Queried live so controls that appear/disable mid-edit count.
  const trapTab = (e: React.KeyboardEvent<HTMLDivElement>): void => {
    if (e.key !== 'Tab') return;
    const root = dialogRef.current;
    if (root === null) return;
    const focusable = [
      ...root.querySelectorAll<HTMLElement>('input, select, textarea, button, a[href]'),
    ].filter((el) => !el.hasAttribute('disabled') && el.tabIndex !== -1);
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (first === undefined || last === undefined) return;
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  // Which advanced values are no longer at their default. Named, not counted:
  // the disclosure is the only trace a collapsed override leaves, and "3 set"
  // would tell the operator that something is off without saying what.
  const overrides: string[] = [
    ...(agent !== '' ? ['agent'] : []),
    ...(priority !== 'normal' ? ['priority'] : []),
    ...(model !== 'default' ? ['model'] : []),
    ...(playbook !== '' ? ['playbook'] : []),
    ...(fileScope.length > 0 ? ['file scope'] : []),
    ...(dependencies.length > 0 ? ['dependencies'] : []),
    ...(labels.length > 0 ? ['labels'] : []),
    ...(column !== 'triage' ? ['column'] : []),
  ];

  const submit = (): void => {
    const t = title.trim();
    if (t === '' || busy) {
      if (t === '') setError('title is required');
      return;
    }
    const p = prompt.trim();
    setBusy(true);
    setError(null);
    createBoardTask({
      projectId,
      title: t,
      // An empty prompt means "the title is the request" — the intake contract
      // the board has always had.
      prompt: p === '' ? t : p,
      priority,
      boardColumn: column,
      fileScope,
      dependencies,
      labels,
      ...(model !== 'default' ? { model } : {}),
      ...(playbook !== '' ? { playbook } : {}),
      ...(agent !== '' ? { agent } : {}),
    })
      .then((task) => {
        onCreated(task);
        onClose();
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label="New task"
      onClick={busy ? undefined : onClose}
    >
      <div
        ref={dialogRef}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={trapTab}
        className="max-h-full w-full max-w-3xl overflow-y-auto rounded-xl border border-line bg-surface px-4 py-4"
      >
        <div className="flex items-center gap-2">
          <span className="font-display text-[14px] font-bold text-ink">New task</span>
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            aria-label="close"
            className="ml-auto text-[15px] leading-none text-ink-dim transition-colors hover:text-ink disabled:opacity-50"
          >
            ×
          </button>
        </div>

        <div className="mt-3 flex flex-col gap-3.5">
          <div>
            <FieldLabel>title</FieldLabel>
            <input
              ref={titleRef}
              type="text"
              value={title}
              disabled={busy}
              onChange={(e) => setTitle(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  submit();
                }
              }}
              placeholder="what should happen"
              aria-label="title"
              className="w-full rounded-[8px] border border-line bg-field px-2.5 py-1.5 text-[13px] text-ink outline-none placeholder:text-ink-faint focus:border-ink-dim disabled:opacity-50"
            />
          </div>

          <div>
            <FieldLabel>what needs doing</FieldLabel>
            <textarea
              value={prompt}
              disabled={busy}
              onChange={(e) => setPrompt(e.target.value)}
              rows={12}
              placeholder="the full request (empty = use the title)"
              aria-label="what needs doing"
              className="w-full resize-y rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11.5px] leading-relaxed text-ink outline-none placeholder:text-ink-faint focus:border-ink-dim disabled:opacity-50"
            />
          </div>

          <button
            type="button"
            onClick={() => setAdvanced((v) => !v)}
            aria-expanded={advanced}
            className="flex w-fit items-center gap-1.5 font-mono text-[10.5px] tracking-[0.08em] text-ink-faint uppercase transition-colors hover:text-ink-dim"
          >
            {/* The glyph is decorative — the word carries the meaning and
                aria-expanded carries the state — and the label is ONE text
                node: an accessible name is built by concatenating the trimmed
                text of each child, so a summary split across spans would be
                announced as "advanced· priority" however wide the flex gap
                renders it. */}
            <span aria-hidden="true">{advanced ? '−' : '+'}</span>
            <span>
              {advanced || overrides.length === 0
                ? 'advanced'
                : `advanced · ${overrides.join(', ')}`}
            </span>
          </button>

          {advanced && (
            <>
              <div>
                <FieldLabel>agent</FieldLabel>
                <AgentSelect agents={agents} value={agent} onChange={setAgent} disabled={busy} />
                <AgentHint agents={agents} value={agent} />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <FieldLabel>priority</FieldLabel>
                  <select
                    value={priority}
                    disabled={busy}
                    onChange={(e) => setPriority(e.target.value as TaskPriority)}
                    aria-label="priority"
                    className="w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink outline-none focus:border-ink-dim disabled:opacity-50"
                  >
                    {TASK_PRIORITIES.map((p) => (
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
                    disabled={busy}
                    onChange={(e) => setModel(e.target.value)}
                    aria-label="model"
                    className="w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink outline-none focus:border-ink-dim disabled:opacity-50"
                  >
                    {TASK_MODELS.map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </select>
                </div>
              </div>

              <div>
                <FieldLabel>playbook</FieldLabel>
                <PlaybookSelect
                  playbooks={playbooks}
                  value={playbook}
                  onChange={setPlaybook}
                  disabled={busy}
                />
                <PlaybookHint playbooks={playbooks} value={playbook} />
              </div>

              <ChipEditor
                label="file scope"
                values={fileScope}
                placeholder="add a path glob + Enter"
                disabled={busy}
                onChange={setFileScope}
              />
              <ChipEditor
                label="dependencies"
                values={dependencies}
                placeholder="add a T-id + Enter"
                disabled={busy}
                onChange={setDependencies}
              />
              <ChipEditor
                label="labels"
                values={labels}
                placeholder="add a label + Enter"
                disabled={busy}
                onChange={setLabels}
              />

              <div>
                <FieldLabel>column</FieldLabel>
                <select
                  value={column}
                  disabled={busy}
                  onChange={(e) => setColumn(e.target.value as BoardColumn)}
                  aria-label="column"
                  className="w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink outline-none focus:border-ink-dim disabled:opacity-50"
                >
                  {TARGET_COLUMNS.map((c) => (
                    <option key={c} value={c}>
                      {COLUMN_LABELS[c]}
                    </option>
                  ))}
                </select>
                <div className="mt-1 font-mono text-[10px] text-ink-faint">
                  {COLUMN_HINT[column] ?? ''}
                </div>
              </div>
            </>
          )}

          {error !== null && (
            <div
              role="alert"
              className="rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red"
            >
              {error}
            </div>
          )}
        </div>

        {/* One action. The label names the lane the card will appear in rather
            than saying "create task" over a column select the operator has to
            go find — and it tracks that select when advanced changes it, so it
            can never promise Inbox and deliver a dispatch. Cancelling is the ×,
            Escape, or the backdrop: three ways out already, none of them a
            button that competes with the one that does the work. */}
        <div className="mt-4 flex justify-end">
          <button
            type="button"
            onClick={submit}
            disabled={busy || title.trim() === ''}
            className="rounded-lg border border-brand/50 bg-brand/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:cursor-not-allowed disabled:opacity-40"
          >
            {busy ? '…' : `create in ${LANE_TITLES[laneOf(column) ?? 'inbox']}`}
          </button>
        </div>
      </div>
    </div>
  );
}
