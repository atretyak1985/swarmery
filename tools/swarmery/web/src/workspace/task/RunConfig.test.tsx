// @vitest-environment jsdom
//
// The run-config panel (board redesign v2 phase 2): the collapsed summary line
// and where the model in it comes from.
//
// The summary's whole job is the question a bare "sonnet" cannot answer —
// whether that model is THIS card's override or the playbook's default — so the
// three states are asserted one by one, plus the fourth (nothing chosen at all,
// where the honest answer is "at dispatch").
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/workspace/task/RunConfig.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
// web/tsconfig.json EXCLUDES *.test.tsx, so `npm run build` does NOT type-check
// this file — the runner reports type errors as runtime failures instead.

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Playbook } from '../../api/types';
import { RunConfig, runConfigSummary } from './RunConfig';
import type { TaskDraft } from './useTaskDraft';

function makePlaybook(over: Partial<Playbook> = {}): Playbook {
  return {
    name: 'standard',
    description: 'the default recipe',
    model: 'sonnet',
    verify: 'normal',
    permissionMode: '',
    source: 'builtin',
    stages: [],
    path: '',
    ...over,
  };
}

function makeDraft(over: Partial<TaskDraft> = {}): TaskDraft {
  return {
    title: 'a task',
    prompt: 'a task',
    priority: 'normal',
    model: 'default',
    playbook: '',
    agent: '',
    fileScope: [],
    dependencies: [],
    ...over,
  };
}

afterEach(cleanup);

describe('runConfigSummary', () => {
  const playbooks = [makePlaybook(), makePlaybook({ name: 'plan-first', model: 'opus' })];

  it('names the playbook as the source when the card has no model of its own', () => {
    expect(runConfigSummary({ playbook: 'standard', model: 'default', agent: '' }, playbooks)).toBe(
      'standard · sonnet (from playbook) · no agent',
    );
  });

  it("marks the card's own model as an override — it beats the playbook's", () => {
    expect(runConfigSummary({ playbook: 'standard', model: 'opus', agent: '' }, playbooks)).toBe(
      'standard · opus (card override) · no agent',
    );
  });

  it('promises nothing about the model when no playbook was chosen either', () => {
    // The dispatcher profiles a recipe at admission and the model arrives with
    // it, so naming 'standard' here would be a guess printed as a fact.
    expect(runConfigSummary({ playbook: '', model: 'default', agent: '' }, playbooks)).toBe(
      'auto playbook · model chosen at dispatch · no agent',
    );
  });

  it('falls back to the default model for a playbook that names none', () => {
    const quiet = [makePlaybook({ name: 'review-heavy', model: '' })];
    expect(runConfigSummary({ playbook: 'review-heavy', model: 'default', agent: '' }, quiet)).toBe(
      'review-heavy · default model · no agent',
    );
  });

  it('reads an unknown playbook name as one with no model rather than crashing', () => {
    // A project playbook the fetch has not returned yet still renders selected.
    expect(runConfigSummary({ playbook: 'ours', model: 'default', agent: '' }, playbooks)).toBe(
      'ours · default model · no agent',
    );
  });

  it('shows the agent as an orthogonal third fact, not a replacement', () => {
    expect(
      runConfigSummary({ playbook: 'plan-first', model: 'default', agent: 'debugger' }, playbooks),
    ).toBe('plan-first · opus (from playbook) · @debugger');
  });
});

describe('RunConfig panel', () => {
  it('is collapsed by default and shows the summary line', () => {
    render(
      <RunConfig
        draft={makeDraft()}
        setField={vi.fn()}
        commit={vi.fn()}
        playbooks={[makePlaybook()]}
        agents={[]}
      />,
    );
    const toggle = screen.getByRole('button', { name: 'run config' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(toggle.textContent).toContain('auto playbook · model chosen at dispatch · no agent');
    // Nothing editable until it is opened — that is the point of the panel.
    expect(screen.queryByLabelText('playbook')).toBeNull();
    expect(screen.queryByLabelText('model')).toBeNull();
  });

  it('opens to the playbook and agent, keeping the four knobs behind advanced', () => {
    render(
      <RunConfig
        draft={makeDraft()}
        setField={vi.fn()}
        commit={vi.fn()}
        playbooks={[makePlaybook()]}
        agents={[]}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'run config' }));
    expect(screen.getByLabelText('playbook')).not.toBeNull();
    expect(screen.getByLabelText('agent')).not.toBeNull();
    expect(screen.queryByLabelText('model')).toBeNull();
    expect(screen.queryByLabelText('priority')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'advanced' }));
    expect(screen.getByLabelText('model')).not.toBeNull();
    expect(screen.getByLabelText('priority')).not.toBeNull();
    expect(screen.getByLabelText('file scope')).not.toBeNull();
    expect(screen.getByLabelText('dependencies')).not.toBeNull();
  });

  it('saves in the same handler that changes a select — a select has no blur to wait for', () => {
    const setField = vi.fn();
    const commit = vi.fn();
    render(
      <RunConfig
        draft={makeDraft()}
        setField={setField}
        commit={commit}
        playbooks={[makePlaybook()]}
        agents={[]}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'run config' }));
    fireEvent.change(screen.getByLabelText('playbook'), { target: { value: 'standard' } });
    expect(setField).toHaveBeenCalledWith('playbook', 'standard');
    expect(commit).toHaveBeenCalledTimes(1);
  });

  it('explains which knob overrides which — the fact that lived in the dispatcher', () => {
    render(
      <RunConfig
        draft={makeDraft()}
        setField={vi.fn()}
        commit={vi.fn()}
        playbooks={[makePlaybook()]}
        agents={[]}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'run config' }));
    const text = document.body.textContent ?? '';
    expect(text).toContain('stages and the permission mode');
    expect(text).toContain('overrides the model the playbook would use');
    expect(text).toContain("prefixes every stage's prompt");
  });
});
