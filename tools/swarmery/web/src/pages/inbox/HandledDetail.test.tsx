// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '../../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { TriageVerdict } from '../../api/triage';
import { HandledDetail } from './HandledDetail';

function verdict(over: Partial<TriageVerdict>): TriageVerdict {
  return {
    id: 1,
    runId: 7,
    kind: 'lesson',
    class: '',
    ref: '1',
    itemKey: '',
    title: 'A lesson',
    value: 'not-useful',
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

afterEach(cleanup);

describe('HandledDetail', () => {
  it('says what the agent did and why, in words', () => {
    render(<HandledDetail verdict={verdict({})} busy={false} error={null} onUndo={vi.fn()} />);
    expect(screen.getByText('A lesson')).toBeTruthy();
    expect(screen.getByText('Duplicate.')).toBeTruthy();
    expect(screen.getByText('not useful')).toBeTruthy();
  });

  it('keeps a classifier label as it is, and an unknown kind by its name', () => {
    render(
      <HandledDetail
        verdict={verdict({ kind: 'mystery', value: 'zap', reason: '' })}
        busy={false}
        error={null}
        onUndo={vi.fn()}
      />,
    );
    expect(screen.getByText(/mystery/)).toBeTruthy();
    cleanup();
    render(
      <HandledDetail verdict={verdict({ kind: 'classifier', value: 'refactor' })} busy={false} error={null} onUndo={vi.fn()} />,
    );
    expect(screen.getByText('refactor')).toBeTruthy();
  });

  it('undo is the main action', () => {
    const onUndo = vi.fn();
    render(<HandledDetail verdict={verdict({})} busy={false} error={null} onUndo={onUndo} />);
    fireEvent.click(screen.getByRole('button', { name: /undo/ }));
    expect(onUndo).toHaveBeenCalledTimes(1);
  });

  it('a failed undo shows its reason, and a running one holds the button', () => {
    render(<HandledDetail verdict={verdict({})} busy error="not undoable" onUndo={vi.fn()} />);
    expect(screen.getByRole('alert').textContent).toContain('not undoable');
    expect((screen.getByRole('button', { name: /undo/ }) as HTMLButtonElement).disabled).toBe(true);
  });
});
