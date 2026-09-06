// The task modal's edit state: one draft of the eight user-owned fields, saved
// automatically, guarded against overwriting a change that landed underneath it.
//
// Why a hook and not a Save button (board redesign v2, phase 2): the button was
// the only thing standing between "I typed a sentence" and "the card says the
// sentence", and it was disabled until `dirty` — so the common outcome of
// editing a card was closing the modal with the edit still in the textarea. The
// modal now saves on blur and on ⌘S.
//
// Autosave needs a guard the button did not. The board holds a live WS
// subscription, so `task` is replaced whenever anything touches the row — a
// re-run appending reviewer feedback to the prompt, the dispatcher stamping the
// playbook it picked, another window. The draft is seeded once per card (keyed
// on id, deliberately: re-seeding on every frame would overwrite what is being
// typed mid-keystroke), which means a blur seconds later would PATCH a field
// whose server value has since moved and silently undo it. So:
//
//   - a field the user has NOT touched follows the server (adopted on arrival),
//   - a field the user HAS touched, whose server value moved since the draft was
//     seeded, is refused with the names of the conflicting fields rather than
//     saved over.
//
// There is no ETag or version column on /api/board/tasks and phase 2 does not
// change the API, so the baseline is the client's own copy of the values it
// seeded from. That catches every conflict this UI can create (the modal is the
// only editor) and cannot catch a write that lands between the check and the
// PATCH — a race two orders of magnitude narrower than the one it closes.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { BoardTask, TaskPriority } from '../../api/types';
import type { PatchBoardTaskInput } from '../../api';

/**
 * The user-editable half of a card, in the shape the controls bind to: `model`
 * carries the literal 'default' rather than null, `playbook` and `agent` carry
 * '' rather than null, because that is what a <select> can hold as a value.
 */
export interface TaskDraft {
  title: string;
  prompt: string;
  priority: TaskPriority;
  /** 'default' = no card-level override; the playbook's model wins. */
  model: string;
  /** '' = no explicit choice; the dispatcher profiles one at admission. */
  playbook: string;
  /** '' = a plain run with no agent persona. */
  agent: string;
  fileScope: string[];
  dependencies: string[];
}

/** Every draft field, in the order a conflict message lists them. */
const DRAFT_KEYS = [
  'title',
  'prompt',
  'priority',
  'model',
  'playbook',
  'agent',
  'fileScope',
  'dependencies',
] as const satisfies ReadonlyArray<keyof TaskDraft>;

/** Sets one draft field. Synchronous: `commit` right after sees the new value. */
export type DraftSetter = <K extends keyof TaskDraft>(key: K, value: TaskDraft[K]) => void;

/** The server's copy of a card, in draft shape. */
export function seedDraft(task: BoardTask): TaskDraft {
  return {
    title: task.title,
    prompt: task.prompt,
    priority: task.priority,
    model: task.model ?? 'default',
    playbook: task.playbook ?? '',
    agent: task.agent ?? '',
    fileScope: task.fileScope,
    dependencies: task.dependencies,
  };
}

/** Value equality for a draft field — every field is a string or a string list. */
function eq(a: TaskDraft[keyof TaskDraft], b: TaskDraft[keyof TaskDraft]): boolean {
  if (typeof a === 'string' && typeof b === 'string') return a.trim() === b.trim();
  return JSON.stringify(a) === JSON.stringify(b);
}

/**
 * Copies `next[k]` into `out`/`outBase` when the SERVER moved that field and the
 * user has not touched it. Generic over the key so the two writes typecheck;
 * called once per key by the reconcile effect below.
 */
function adopt<K extends keyof TaskDraft>(
  k: K,
  cur: TaskDraft,
  base: TaskDraft,
  next: TaskDraft,
  out: TaskDraft,
  outBase: TaskDraft,
): boolean {
  if (eq(next[k], base[k])) return false; // the server did not move it
  if (!eq(cur[k], base[k])) return false; // the user is editing it — keep the edit
  out[k] = next[k];
  outBase[k] = next[k];
  return true;
}

/** Fields whose draft value differs from the baseline the draft was seeded from. */
function dirtyKeys(draft: TaskDraft, base: TaskDraft): Array<keyof TaskDraft> {
  return DRAFT_KEYS.filter((k) => !eq(draft[k], base[k]));
}

/** The patch body for a set of dirty fields — nothing else is sent. */
export function draftPatch(draft: TaskDraft, keys: ReadonlyArray<keyof TaskDraft>): PatchBoardTaskInput {
  const patch: PatchBoardTaskInput = {};
  for (const k of keys) {
    if (k === 'title') patch.title = draft.title.trim();
    if (k === 'prompt') patch.prompt = draft.prompt.trim();
    if (k === 'priority') patch.priority = draft.priority;
    // 'default' is a UI sentinel, not a model name: it clears the override.
    if (k === 'model') patch.model = draft.model === 'default' ? null : draft.model;
    if (k === 'playbook') patch.playbook = draft.playbook; // "" clears back to auto
    if (k === 'agent') patch.agent = draft.agent; // "" clears back to a plain run
    if (k === 'fileScope') patch.fileScope = draft.fileScope;
    if (k === 'dependencies') patch.dependencies = draft.dependencies;
  }
  return patch;
}

/** How a refused save reads. Exported so the test names the same sentence. */
export function conflictMessage(keys: ReadonlyArray<keyof TaskDraft>): string {
  return `not saved — ${keys.join(', ')} changed on the server while you were editing. Close and reopen the card to pick up the new version.`;
}

export interface TaskDraftState {
  draft: TaskDraft;
  setField: DraftSetter;
  /** Saves every dirty field, or refuses with a conflict/validation message. */
  commit: () => void;
  saving: boolean;
  /** The last save error — a server message, a conflict, or a validation refusal. */
  saveError: string | null;
}

export function useTaskDraft(
  task: BoardTask,
  onPatch: (patch: PatchBoardTaskInput) => Promise<BoardTask>,
): TaskDraftState {
  // draftRef mirrors the state so a control can setField(...) and commit() in
  // one handler — a <select>'s change IS its commit, and reading the state
  // variable there would send the previous value.
  const draftRef = useRef<TaskDraft>(seedDraft(task));
  const baseRef = useRef<TaskDraft>(seedDraft(task));
  const [draft, setDraft] = useState<TaskDraft>(draftRef.current);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  // A DIFFERENT card: everything resets, including the error from the last one.
  useEffect(() => {
    draftRef.current = seedDraft(task);
    baseRef.current = seedDraft(task);
    setDraft(draftRef.current);
    setSaveError(null);
    // Keyed on the card, NOT on `task`: every WS frame mints a new object for
    // the same row, and re-seeding on those would overwrite live typing.
  }, [task.id]);

  // The same card, changed on the server: adopt what the user is not editing.
  useEffect(() => {
    const next = seedDraft(task);
    const out = { ...draftRef.current };
    const outBase = { ...baseRef.current };
    let changed = false;
    for (const k of DRAFT_KEYS) {
      if (adopt(k, draftRef.current, baseRef.current, next, out, outBase)) changed = true;
    }
    if (!changed) return;
    draftRef.current = out;
    baseRef.current = outBase;
    setDraft(out);
  }, [task]);

  const setField = useCallback<DraftSetter>((key, value) => {
    const next = { ...draftRef.current, [key]: value };
    draftRef.current = next;
    setDraft(next);
  }, []);

  const commit = useCallback((): void => {
    const cur = draftRef.current;
    const base = baseRef.current;
    const dirty = dirtyKeys(cur, base);
    if (dirty.length === 0) {
      setSaveError(null);
      return;
    }
    if (cur.title.trim() === '' || cur.prompt.trim() === '') {
      setSaveError('title and prompt cannot be empty');
      return;
    }
    // The baseline is what the draft was seeded from; `seedDraft(task)` is what
    // the server holds now. A field in both lists is a field two people edited.
    const serverNow = seedDraft(task);
    const conflicts = dirty.filter((k) => !eq(serverNow[k], base[k]));
    if (conflicts.length > 0) {
      setSaveError(conflictMessage(conflicts));
      return;
    }
    setSaving(true);
    setSaveError(null);
    onPatch(draftPatch(cur, dirty))
      .then((saved) => {
        // The save moved the baseline: what the server holds now is what the
        // next commit measures against, so an edit made while this was in
        // flight is dirty against the SAVED copy rather than a conflict with
        // it. The draft itself is left alone — it already holds what was sent,
        // and any field the server normalized arrives through the reconcile
        // effect above when the board pushes the updated row.
        baseRef.current = seedDraft(saved);
      })
      .catch((e: unknown) => setSaveError(e instanceof Error ? e.message : String(e)))
      .finally(() => setSaving(false));
  }, [onPatch, task]);

  return { draft, setField, commit, saving, saveError };
}
