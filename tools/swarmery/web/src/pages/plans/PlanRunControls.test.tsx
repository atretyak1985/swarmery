// @vitest-environment jsdom
//
// The run-plan panel. The claims: the agent picker offers only agents that may be
// handed a whole plan (frontmatter `planExecutor: true`), and the orchestrate mode
// is offered and runs with what the operator picked.

import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SystemItem } from '../../api/types';
import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { PlanRunDialog } from './PlanRunControls';

const fetchSystemItems = vi.hoisted(() => vi.fn());
vi.mock('../../api/system', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api/system')>()),
  fetchSystemItems,
}));

const agent = (name: string, planExecutor: boolean): SystemItem => ({ name, planExecutor }) as unknown as SystemItem;

afterEach(() => {
  cleanup();
  fetchSystemItems.mockReset();
});

describe('PlanRunDialog', () => {
  it('lists only plan-executor agents', async () => {
    fetchSystemItems.mockResolvedValue([
      agent('tech-lead', true),
      agent('code-reviewer', false),
      agent('implementation-agent', true),
      agent('tech-lead', true), // same name in two scopes — listed once
    ]);
    const { container } = render(
      <PlanRunDialog initialAgent="" initialMode="orchestrate" onRun={() => undefined} onCancel={() => undefined} />,
    );
    await waitFor(() => {
      const opts = [...container.querySelectorAll('#plan-run-agents option')].map((o) => o.getAttribute('value'));
      expect(opts).toEqual(['implementation-agent', 'tech-lead']);
    });
  });

  it('starts with the mode it was given and runs with it', async () => {
    fetchSystemItems.mockResolvedValue([]);
    const onRun = vi.fn();
    render(<PlanRunDialog initialAgent="tech-lead" initialMode="orchestrate" onRun={onRun} onCancel={() => undefined} />);
    expect(screen.getByRole('radio', { name: 'orchestrate' }).getAttribute('aria-checked')).toBe('true');
    fireEvent.click(screen.getByRole('button', { name: '▶ Run' }));
    await waitFor(() => expect(onRun).toHaveBeenCalledWith('tech-lead', 'orchestrate'));
  });
});
