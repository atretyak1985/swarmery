// How this card will RUN — playbook, model, agent, and the four knobs behind
// `advanced` (board redesign v2, phase 2).
//
// The old modal gave these seven controls the same weight as the title and the
// prompt: priority and model side by side, then agent, then playbook, then two
// chip editors, all of it above the fold and none of it explained. On the card
// that motivated this phase — captured from a session, twelve days in the Inbox,
// never dispatched — every one of them was at its default and none of them was
// the question the reader had.
//
// So the panel is one line by default, and the line says what would happen if
// the card ran now. It is not a "collapse" of the same controls: the summary IS
// the answer for the reader who is not here to change the recipe, which is
// almost every reader.
//
// The three knobs are not mutually exclusive and the panel does not pretend they
// are (an earlier sketch had a XOR validation). They compose, in a fixed order,
// and the explainer below says how — `model` beating the playbook's model, and
// `agent` prefixing the persona onto every stage, are both facts you could
// previously only learn from internal/dispatch/service.go.

import { useState } from 'react';
import type { AgentRosterRow, Playbook, TaskPriority } from '../../api/types';
import { AgentHint, AgentSelect } from '../AgentPicker';
import { TASK_MODELS, TASK_PRIORITIES } from '../boardModel';
import { PlaybookHint, PlaybookSelect } from '../PlaybookPicker';
import { ChipEditor, FieldLabel } from '../TaskFields';
import type { DraftSetter, TaskDraft } from './useTaskDraft';

/** The three choices the summary line reads, in draft shape ('' / 'default' = unset). */
export interface RunConfigChoice {
  readonly playbook: string;
  readonly model: string;
  readonly agent: string;
}

/** '' and 'default' are the two ways a select says "no explicit choice". */
function unset(v: string): boolean {
  return v === '' || v === 'default';
}

/**
 * Where the model that will actually run comes from, in words.
 *
 * Four states, and the reason this is a function rather than a template string:
 * `model` on the card OVERRIDES the playbook's model (dispatch/service.go), so
 * "sonnet" alone never says whether sonnet is this card's decision or the
 * recipe's — and those need opposite actions from a reader who wants a different
 * model. With no override AND no chosen playbook there is no answer to give: the
 * dispatcher profiles a recipe at admission and the model arrives with it.
 */
function modelPhrase(choice: RunConfigChoice, playbooks: readonly Playbook[]): string {
  if (!unset(choice.model)) return `${choice.model} (card override)`;
  if (unset(choice.playbook)) return 'model chosen at dispatch';
  const pb = playbooks.find((p) => p.name === choice.playbook);
  if (pb === undefined || pb.model === '') return 'default model';
  return `${pb.model} (from playbook)`;
}

/**
 * The collapsed one-liner: "standard · sonnet (from playbook) · no agent".
 * Pure, and exported, so the three-way model question above is unit-testable
 * without rendering a select.
 */
export function runConfigSummary(choice: RunConfigChoice, playbooks: readonly Playbook[]): string {
  const playbook = unset(choice.playbook) ? 'auto playbook' : choice.playbook;
  const agent = unset(choice.agent) ? 'no agent' : `@${choice.agent}`;
  return `${playbook} · ${modelPhrase(choice, playbooks)} · ${agent}`;
}

/** What each knob does and which one wins — the answer service.go used to hold. */
function KnobExplainer(): JSX.Element {
  return (
    <p className="font-mono text-[10px] leading-relaxed text-ink-faint">
      The playbook sets the stages and the permission mode the run spawns under.{' '}
      <span className="text-ink-dim">model</span> overrides the model the playbook would use.{' '}
      <span className="text-ink-dim">agent</span> is orthogonal to both: it prefixes every stage's
      prompt with that persona, it does not replace the stages.
    </p>
  );
}

export function RunConfig({
  draft,
  setField,
  commit,
  playbooks,
  agents,
}: {
  draft: TaskDraft;
  setField: DraftSetter;
  /** Autosave: a select's change IS its commit, so both fire in one handler. */
  commit: () => void;
  playbooks: Playbook[];
  agents: AgentRosterRow[];
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const summary = runConfigSummary(draft, playbooks);
  // Every control below writes the draft and saves in the same handler: a
  // <select> or a chip has no "finished editing" moment a blur could stand for.
  const set: DraftSetter = (key, value) => {
    setField(key, value);
    commit();
  };
  return (
    <div className="rounded-[8px] border border-line">
      <button
        type="button"
        aria-expanded={open}
        aria-label="run config"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-2.5 py-1.5 text-left"
      >
        <span className="shrink-0 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
          run
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-2">{summary}</span>
        <span aria-hidden="true" className="shrink-0 font-mono text-[10px] text-ink-faint">
          {open ? '▾' : '▸'}
        </span>
      </button>

      {open && (
        <div className="flex flex-col gap-3 border-t border-line px-2.5 py-2.5">
          <div>
            <FieldLabel>playbook</FieldLabel>
            <PlaybookSelect
              playbooks={playbooks}
              value={draft.playbook}
              onChange={(v) => set('playbook', v)}
            />
            <PlaybookHint playbooks={playbooks} value={draft.playbook} />
          </div>

          <div>
            <FieldLabel>agent</FieldLabel>
            <AgentSelect agents={agents} value={draft.agent} onChange={(v) => set('agent', v)} />
            <AgentHint agents={agents} value={draft.agent} />
          </div>

          <KnobExplainer />

          <div>
            <button
              type="button"
              aria-expanded={advanced}
              aria-label="advanced"
              onClick={() => setAdvanced((v) => !v)}
              className="font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase transition-colors hover:text-ink-dim"
            >
              {advanced ? '▾' : '▸'} advanced
            </button>
            {advanced && (
              <div className="mt-2 flex flex-col gap-3">
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <FieldLabel>model</FieldLabel>
                    <select
                      value={draft.model}
                      onChange={(e) => set('model', e.target.value)}
                      aria-label="model"
                      className="w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink outline-none focus:border-ink-dim"
                    >
                      {TASK_MODELS.map((m) => (
                        <option key={m} value={m}>
                          {m}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <FieldLabel>priority</FieldLabel>
                    <select
                      value={draft.priority}
                      onChange={(e) => set('priority', e.target.value as TaskPriority)}
                      aria-label="priority"
                      className="w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink outline-none focus:border-ink-dim"
                    >
                      {TASK_PRIORITIES.map((p) => (
                        <option key={p} value={p}>
                          {p}
                        </option>
                      ))}
                    </select>
                  </div>
                </div>
                <ChipEditor
                  label="file scope"
                  values={draft.fileScope}
                  placeholder="add a path glob + Enter"
                  onChange={(v) => set('fileScope', v)}
                />
                <ChipEditor
                  label="dependencies"
                  values={draft.dependencies}
                  placeholder="add a T-id + Enter"
                  onChange={(v) => set('dependencies', v)}
                />
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
