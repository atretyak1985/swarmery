// @vitest-environment jsdom
//
// PhaseReopens: the Runs tab's one "Reopen…" action (plan D-31) — a Done
// phase offers it with an explanatory sentence, any other phase explains why
// it is withheld instead — the Reopen form itself (ticked criteria from the
// server, the request it sends, the refetch after it), and the refusal path.
// The reopen HISTORY moved to the attempt groups' captions (runsModel.ts);
// this component no longer renders it.

import { cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api/phasereport';
import type { PhaseReopen } from '../../api/types';
import { PhaseReopens } from './PhaseReopens';

vi.mock('../../api/phasereport', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/phasereport')>();
  return { ...actual, fetchPhaseReopens: vi.fn(), reopenPhase: vi.fn() };
});

const REOPEN: PhaseReopen = {
  id: 4,
  reason: 'DELETE left orphaned rows',
  fixUrl: 'https://example.test/pr/9',
  caughtBy: 'review',
  criteria: ['DELETE removes it'],
  createdAt: '2026-10-09T12:00:00Z',
};

afterEach(cleanup);
beforeEach(() => {
  vi.mocked(api.fetchPhaseReopens).mockReset();
  vi.mocked(api.reopenPhase).mockReset();
});

describe('PhaseReopens', () => {
  it('a not-reopenable phase explains why instead of offering the action', () => {
    render(<PhaseReopens taskId={1} phaseId={2} canReopen={false} onReopened={vi.fn()} />);
    expect(screen.getByText(/Reopen… is offered once this phase is Done/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Reopen…' })).toBeNull();
  });

  it('a reopenable phase offers the action with an explanatory sentence', () => {
    render(<PhaseReopens taskId={1} phaseId={2} canReopen onReopened={vi.fn()} />);
    expect(screen.getByRole('button', { name: 'Reopen…' })).toBeTruthy();
    expect(screen.getByText(/Sends this phase back for another attempt/)).toBeTruthy();
  });

  it('reopens a Done phase: ticked criteria, the request, the refetch', async () => {
    vi.mocked(api.fetchPhaseReopens).mockResolvedValue({
      reopens: [],
      ticked: ['POST creates a line item', 'DELETE removes it'],
    });
    vi.mocked(api.reopenPhase).mockResolvedValue({ reopen: REOPEN, unticked: 1 });
    const onReopened = vi.fn();
    render(<PhaseReopens taskId={1} phaseId={2} canReopen onReopened={onReopened} />);

    fireEvent.click(screen.getByRole('button', { name: 'Reopen…' }));
    const form = screen.getByRole('form', { name: 'reopen this phase' });
    const submit = within(form).getByRole('button', { name: 'Reopen and untick' }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);

    fireEvent.click(await within(form).findByRole('checkbox', { name: 'DELETE removes it' }));
    fireEvent.change(within(form).getByLabelText('what slipped through'), { target: { value: ' orphans ' } });
    fireEvent.change(within(form).getByLabelText('fix URL (optional)'), {
      target: { value: 'https://example.test/pr/9' },
    });
    fireEvent.change(within(form).getByLabelText('caught by'), { target: { value: 'review' } });
    expect(submit.disabled).toBe(false);
    fireEvent.click(submit);

    await waitFor(() => expect(onReopened).toHaveBeenCalledTimes(1));
    expect(api.fetchPhaseReopens).toHaveBeenCalledWith(1, 2);
    expect(api.reopenPhase).toHaveBeenCalledWith(1, 2, {
      reason: 'orphans',
      fixUrl: 'https://example.test/pr/9',
      caughtBy: 'review',
      criteria: ['DELETE removes it'],
    });
    expect(screen.queryByRole('form', { name: 'reopen this phase' })).toBeNull();
  });

  it('shows the server’s refusal and keeps the form open', async () => {
    vi.mocked(api.fetchPhaseReopens).mockResolvedValue({ reopens: [], ticked: ['A'] });
    vi.mocked(api.reopenPhase).mockRejectedValue(new Error('none of the named criteria is a ticked criterion'));
    const onReopened = vi.fn();
    render(<PhaseReopens taskId={1} phaseId={2} canReopen onReopened={onReopened} />);
    fireEvent.click(screen.getByRole('button', { name: 'Reopen…' }));
    fireEvent.click(await screen.findByRole('checkbox', { name: 'A' }));
    fireEvent.change(screen.getByLabelText('what slipped through'), { target: { value: 'x' } });
    fireEvent.click(screen.getByRole('button', { name: 'Reopen and untick' }));
    expect((await screen.findByRole('alert')).textContent).toBe('none of the named criteria is a ticked criterion');
    expect(onReopened).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'cancel' }));
    expect(screen.getByRole('button', { name: 'Reopen…' })).toBeTruthy();
  });

  it('says so when the doc has nothing ticked', async () => {
    vi.mocked(api.fetchPhaseReopens).mockResolvedValue({ reopens: [], ticked: [] });
    render(<PhaseReopens taskId={1} phaseId={2} canReopen onReopened={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Reopen…' }));
    expect(await screen.findByText(/No ticked criteria/)).toBeTruthy();
  });
});
