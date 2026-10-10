// @vitest-environment jsdom
//
// PhaseReopens: the Runs tab's "Reopened" history with the gate that caught each
// defect, the Reopen form on a Done phase (ticked criteria from the server, the
// request it sends, the refetch after it), and the refusal path.

import { cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api/phasereport';
import type { PhaseReopen } from '../../api/types';
import { PhaseReopens, caughtByText } from './PhaseReopens';

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
  it('renders the reopen history with who caught it', () => {
    render(<PhaseReopens taskId={1} phaseId={2} reopens={[REOPEN]} canReopen={false} onReopened={vi.fn()} />);
    const section = screen.getByRole('region', { name: 'Reopened' });
    expect(within(section).getByText('Reopened · 1')).toBeTruthy();
    expect(within(section).getByText('caught by review')).toBeTruthy();
    expect(within(section).getByText('DELETE left orphaned rows')).toBeTruthy();
    expect(within(section).getByText('DELETE removes it')).toBeTruthy();
    expect(within(section).getByRole('link', { name: 'fix →' }).getAttribute('href')).toBe(REOPEN.fixUrl);
    // Not Done: no Reopen action.
    expect(screen.queryByRole('button', { name: 'Reopen…' })).toBeNull();
  });

  it('renders nothing for a never-reopened phase that is not Done', () => {
    const { container } = render(
      <PhaseReopens taskId={1} phaseId={2} reopens={[]} canReopen={false} onReopened={vi.fn()} />,
    );
    expect(container.innerHTML).toBe('');
  });

  it('reopens a Done phase: ticked criteria, the request, the refetch', async () => {
    vi.mocked(api.fetchPhaseReopens).mockResolvedValue({
      reopens: [],
      ticked: ['POST creates a line item', 'DELETE removes it'],
    });
    vi.mocked(api.reopenPhase).mockResolvedValue({ reopen: REOPEN, unticked: 1 });
    const onReopened = vi.fn();
    render(<PhaseReopens taskId={1} phaseId={2} reopens={[]} canReopen onReopened={onReopened} />);

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
    render(<PhaseReopens taskId={1} phaseId={2} reopens={[]} canReopen onReopened={onReopened} />);
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
    render(<PhaseReopens taskId={1} phaseId={2} reopens={[]} canReopen onReopened={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Reopen…' }));
    expect(await screen.findByText(/No ticked criteria/)).toBeTruthy();
  });

  it('caughtByText', () => {
    expect(caughtByText('none')).toBe('caught by nobody');
    expect(caughtByText('verifier')).toBe('caught by verifier');
  });
});
