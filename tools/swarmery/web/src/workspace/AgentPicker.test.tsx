// @vitest-environment jsdom
//
// Agent picker (board redesign phase 4): what the <select> offers for a project.
//
//   1. The first option is the honest no-agent mode — "" runs the playbook's
//      stages with no persona, it is not "nothing picked yet".
//   2. Agents sit in one <optgroup> per role, in a fixed order, with the role
//      read from the API (never decided by the web app).
//   3. An agent from a pack the project does not enable is not offered.
//   4. No scope suffix after the name; a badge marks only a project override.
//   5. A filter box appears only past 12 agents.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/workspace/AgentPicker.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, describe, expect, it } from 'vitest';
import type { AgentRole, AgentRosterRow } from '../api/types';
import { i18n } from '../i18n';
import { AgentHint, AgentSelect, NO_AGENT_LABEL, type PickerAgent, selectableAgents } from './AgentPicker';

let nextID = 1;

function row(name: string, over: Partial<AgentRosterRow> & { role?: AgentRole } = {}): AgentRosterRow {
  const plugin = name.includes(':') ? name.slice(0, name.indexOf(':')) : null;
  return {
    id: nextID++,
    name,
    scope: 'global',
    projectSlug: null,
    origin: plugin === null ? 'local' : 'plugin',
    pluginName: plugin,
    model: null,
    path: `/agents/${name}.md`,
    description: null,
    role: 'domain',
    enabledInProject: true,
    improvable: true,
    runs30d: 0,
    successRate: null,
    failedShare: 0,
    cost30d: 0,
    lastActiveAt: null,
    ...over,
  };
}

/** A roster like a project that enables core only (web-pack is off). */
const ROSTER: AgentRosterRow[] = [
  row('core:tech-lead', { role: 'orchestrate' }),
  row('core:implementation-agent', { role: 'implement' }),
  row('core:code-reviewer', { role: 'review' }),
  row('core:researcher', { role: 'research' }),
  row('core:test-runner', { role: 'ops' }),
  row('web-pack:seo-specialist', { role: 'domain', enabledInProject: false }),
  row('my-helper', { role: 'domain' }),
  // The project's own definition of a core agent → an override.
  row('code-reviewer', { scope: 'project', projectSlug: 'alpha', role: 'review' }),
  // Another project's local agent never shows.
  row('beta-only', { scope: 'project', projectSlug: 'beta' }),
];

function renderPicker(agents: PickerAgent[], value = ''): HTMLSelectElement {
  render(<AgentSelect agents={agents} value={value} onChange={() => undefined} />);
  return screen.getByLabelText('agent') as HTMLSelectElement;
}

afterEach(cleanup);

describe('AgentSelect', () => {
  const agents = selectableAgents(ROSTER, 'alpha');

  it('opens with the no-agent option, value ""', () => {
    const select = renderPicker(agents);
    const first = select.options[0];
    expect(first?.value).toBe('');
    expect(first?.textContent).toBe(i18n._(NO_AGENT_LABEL));
    expect(i18n._(NO_AGENT_LABEL)).toBe('No agent — playbook stages only');
  });

  it('groups agents by role in the fixed order', () => {
    const select = renderPicker(agents);
    const groups = [...select.querySelectorAll('optgroup')].map((g) => g.label);
    expect(groups).toEqual(['orchestrate', 'implement', 'review', 'research', 'ops', 'domain']);
    const review = select.querySelector('optgroup[label="review"]');
    expect([...(review?.querySelectorAll('option') ?? [])].map((o) => o.value)).toEqual([
      'code-reviewer',
      'core:code-reviewer',
    ]);
  });

  it('drops agents of packs the project does not enable, and other projects’ agents', () => {
    const select = renderPicker(agents);
    const values = [...select.options].map((o) => o.value);
    expect(values).not.toContain('web-pack:seo-specialist');
    expect(values).not.toContain('beta-only');
    expect(values).toContain('my-helper');
  });

  it('shows no scope suffix, and marks only the project override', () => {
    const select = renderPicker(agents);
    const labels = [...select.options].map((o) => o.textContent ?? '');
    expect(labels.some((l) => /·\s*(global|project)/.test(l))).toBe(false);
    const marked = labels.filter((l) => l.includes('project override'));
    expect(marked).toEqual(['@code-reviewer (project override)']);
  });

  it('has no filter box at 12 agents or fewer', () => {
    renderPicker(agents);
    expect(screen.queryByLabelText('filter agents')).toBeNull();
  });

  it('adds a filter box past 12 agents and narrows the options', () => {
    const many = selectableAgents(
      Array.from({ length: 13 }, (_, i) => row(`core:agent-${String(i).padStart(2, '0')}`, { role: 'implement' })),
      'alpha',
    );
    const select = renderPicker(many, 'core:agent-00');
    const box = screen.getByLabelText('filter agents');
    fireEvent.change(box, { target: { value: 'agent-12' } });
    const values = [...select.options].map((o) => o.value);
    // "" + the match + the still-selected agent.
    expect(values).toEqual(['', 'core:agent-00', 'core:agent-12']);
  });
});

describe('AgentHint', () => {
  const agents = selectableAgents(ROSTER, 'alpha');

  it('shows the badge for a project override', () => {
    render(<AgentHint agents={agents} value="code-reviewer" />);
    expect(screen.getByText('project override')).toBeTruthy();
  });

  it('shows no badge for an inherited agent', () => {
    render(<AgentHint agents={agents} value="core:code-reviewer" />);
    expect(screen.queryByText('project override')).toBeNull();
  });
});
