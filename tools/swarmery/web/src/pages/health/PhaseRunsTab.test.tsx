// @vitest-environment jsdom
//
// PhaseRunsTab: the window the inputs start from and refetch on, every row with
// its estimate suffix and cost, the highlighted reopen rows, the fallback line,
// and the error path.

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api/phasereport';
import type { PhaseRunsReport } from '../../api/phasereport';
import { PhaseRunsTab, isReopenRow, rowLabel } from './PhaseRunsTab';

vi.mock('../../api/phasereport', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/phasereport')>();
  return { ...actual, fetchPhaseRunsReport: vi.fn() };
});

const REPORT: PhaseRunsReport = {
  from: '2026-09-01T00:00:00Z',
  to: '2026-09-30T23:59:59Z',
  rows: [
    { key: 'runs', label: 'phase runs', n: 91, estimated: false },
    { key: 'noop_push_pr', label: 'noop · waiting on push/PR', n: 16, estimated: true },
    { key: 'noop_cost', label: 'noop cost', n: 48, costUsd: 34.853, estimated: false },
    { key: 'reopens', label: 'reopened phases', n: 2, estimated: false },
    { key: 'reopens_operator', label: 'reopened · caught by operator', n: 2, estimated: false },
  ],
  fallbackRows: { n: 3, byOutcome: { noop: 1, completed: 2 } },
  notes: ['phase_reviews is absent: review runs read 0'],
};

afterEach(cleanup);
beforeEach(() => {
  vi.mocked(api.fetchPhaseRunsReport).mockReset();
});

describe('PhaseRunsTab', () => {
  it('fetches Health’s window and renders every row', async () => {
    vi.mocked(api.fetchPhaseRunsReport).mockResolvedValue(REPORT);
    render(<PhaseRunsTab from="2026-09-01" to="2026-09-30" />);
    expect(api.fetchPhaseRunsReport).toHaveBeenCalledWith('2026-09-01', '2026-09-30');

    const table = await screen.findByRole('table');
    const row = (label: string): HTMLElement => {
      const tr = within(table).getByRole('rowheader', { name: label }).closest('tr');
      if (tr === null) throw new Error(`no row ${label}`);
      return tr;
    };
    expect(within(row('phase runs')).getByText('91')).toBeTruthy();
    expect(within(row('noop · waiting on push/PR (est.)')).getByText('16')).toBeTruthy();
    expect(within(row('noop cost')).getByText('$34.85')).toBeTruthy();
    // The reopen rows are highlighted; the others are not.
    expect(row('reopened phases').className).toContain('bg-amber');
    expect(row('reopened · caught by operator').className).toContain('bg-amber');
    expect(row('phase runs').className).not.toContain('bg-amber');

    expect(screen.getByText(/3 more run\(s\) ended in the window with no actuals row \(completed 2, noop 1\)/)).toBeTruthy();
    expect(screen.getByText('phase_reviews is absent: review runs read 0')).toBeTruthy();
  });

  it('refetches when a date input changes', async () => {
    vi.mocked(api.fetchPhaseRunsReport).mockResolvedValue(REPORT);
    render(<PhaseRunsTab from="2026-09-01" to="2026-09-30" />);
    await screen.findByRole('table');
    fireEvent.change(screen.getByLabelText('from'), { target: { value: '2026-09-15' } });
    await waitFor(() => {
      expect(api.fetchPhaseRunsReport).toHaveBeenLastCalledWith('2026-09-15', '2026-09-30');
    });
    fireEvent.change(screen.getByLabelText('to'), { target: { value: '2026-09-20' } });
    await waitFor(() => {
      expect(api.fetchPhaseRunsReport).toHaveBeenLastCalledWith('2026-09-15', '2026-09-20');
    });
  });

  it('shows the server’s error', async () => {
    vi.mocked(api.fetchPhaseRunsReport).mockRejectedValue(new Error('phasereport: bad window: to is before from'));
    render(<PhaseRunsTab from="2026-09-30" to="2026-09-01" />);
    expect((await screen.findByRole('alert')).textContent).toBe('phasereport: bad window: to is before from');
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('labels and reopen rows', () => {
    expect(rowLabel({ key: 'x', label: 'a', n: 1, estimated: true })).toBe('a (est.)');
    expect(rowLabel({ key: 'x', label: 'a', n: 1, estimated: false })).toBe('a');
    expect(isReopenRow({ key: 'reopens_none', label: '', n: 0, estimated: false })).toBe(true);
    expect(isReopenRow({ key: 'reopened', label: '', n: 0, estimated: false })).toBe(false);
  });
});
