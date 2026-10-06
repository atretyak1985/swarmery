// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as triage from '../../api/triage';
import type { TriageVerdict } from '../../api/triage';
import { HandledList } from './HandledList';

vi.mock('../../api/triage', () => ({ undoTriageVerdict: vi.fn(async () => ({})) }));

function verdict(over: Partial<TriageVerdict>): TriageVerdict {
  return {
    id: 1,
    runId: 7,
    kind: 'lesson',
    class: '',
    ref: '1',
    itemKey: '',
    title: 'A lesson',
    value: 'dismiss',
    reason: 'Duplicate.',
    payload: null,
    prior: null,
    state: 'applied',
    createdAt: new Date(Date.now() - 3600_000).toISOString(),
    decidedAt: null,
    projectId: null,
    ...over,
  };
}

beforeEach(() => vi.mocked(triage.undoTriageVerdict).mockClear());
afterEach(cleanup);

describe('HandledList', () => {
  it('shows the empty state', () => {
    render(<HandledList verdicts={[]} onChanged={vi.fn()} />);
    expect(screen.getByText('The agent has not closed anything in the last 7 days.')).toBeTruthy();
  });

  it('lists newest first with human wording, the label for the classifier, and the reason', () => {
    render(
      <HandledList
        onChanged={vi.fn()}
        verdicts={[
          verdict({ id: 1, title: 'Old one', value: 'not-useful', createdAt: new Date(Date.now() - 5 * 3600_000).toISOString() }),
          verdict({ id: 2, kind: 'classifier', title: 'Session X', value: 'refactor', reason: 'Only moved code.' }),
          verdict({ id: 3, kind: 'mystery', title: 'Odd', value: 'zap', createdAt: new Date(Date.now() - 60_000).toISOString() }),
        ]}
      />,
    );
    const items = screen.getAllByRole('listitem');
    expect(items).toHaveLength(3);
    expect(items[0]?.textContent).toContain('Odd');
    expect(items[0]?.textContent).toContain('mystery');
    expect(items[0]?.textContent).toContain('zap');
    expect(items[1]?.textContent).toContain('refactor');
    expect(items[1]?.textContent).toContain('Only moved code.');
    expect(items[2]?.textContent).toContain('not useful');
  });

  it('undo calls the endpoint then reloads', async () => {
    const reload = vi.fn();
    render(<HandledList verdicts={[verdict({ id: 9, title: 'Dup lesson' })]} onChanged={reload} />);
    fireEvent.click(screen.getByRole('button', { name: 'undo Dup lesson' }));
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
    expect(triage.undoTriageVerdict).toHaveBeenCalledWith(9);
  });

  it('a failed undo shows a dismissable alert', async () => {
    vi.mocked(triage.undoTriageVerdict).mockRejectedValueOnce(new Error('not undoable'));
    render(<HandledList verdicts={[verdict({ id: 9, title: 'Dup lesson' })]} onChanged={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'undo Dup lesson' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('not undoable');
    fireEvent.click(screen.getByRole('button', { name: 'dismiss' }));
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
