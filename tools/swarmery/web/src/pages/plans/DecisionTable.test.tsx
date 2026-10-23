// @vitest-environment jsdom
//
// The plan tab's decision table: the four chips, the not-implemented reason, the
// not-verified banner of an INCONCLUSIVE plan review, nothing at all for a plan
// without decisions or a spec, and the fetch error.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DecisionTable as Table } from '../../api/evidence';
import { cleanup, render, screen, waitFor } from '../../test/render';
import { DecisionTable } from './DecisionTable';

const api = vi.hoisted(() => ({ fetchDecisionTable: vi.fn() }));

vi.mock('../../api/evidence', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/evidence')>();
  return { ...real, fetchDecisionTable: api.fetchDecisionTable };
});

afterEach(cleanup);
beforeEach(() => {
  api.fetchDecisionTable.mockReset();
});

const table: Table = {
  rows: [
    {
      id: 'D-2',
      kind: 'decision',
      text: 'Which format? → CSV',
      status: 'evidence',
      reason: '',
      claimedBy: [1],
      evidence: [{ kind: 'report', status: 'evidence', excerpt: 'CSV writer per D-2', phaseSeq: 1, phaseName: 'Export' }],
    },
    {
      id: 'D-5',
      kind: 'decision',
      text: 'Mirror to S3? → yes',
      status: 'not-implemented',
      reason: 'the bucket policy is pending',
      claimedBy: [1],
      evidence: [],
    },
    { id: 'SC-3', kind: 'criterion', text: 'import works', status: 'remaining', reason: '', claimedBy: [], evidence: [] },
    {
      id: 'SC-4',
      kind: 'criterion',
      text: 'row cap',
      status: 'not-verified',
      reason: 'reviewer-timed-out',
      claimedBy: [2],
      evidence: [],
    },
  ],
  planReview: { status: '', verdict: '', reasonClass: '', at: '' },
  counts: { evidence: 1, 'not-implemented': 1, remaining: 1, 'not-verified': 1 },
};

describe('DecisionTable', () => {
  it('renders every row with its chip, phases and proof', async () => {
    api.fetchDecisionTable.mockResolvedValue(table);
    const { container } = render(<DecisionTable taskId={9} />);
    expect(await screen.findByText('Which format? → CSV')).toBeTruthy();
    expect(api.fetchDecisionTable).toHaveBeenCalledWith(9);

    const chip = (id: string): Element | null => container.querySelector(`tr[data-row="${id}"] [data-status]`);
    expect(chip('D-2')?.getAttribute('data-status')).toBe('evidence');
    expect(chip('D-2')?.textContent).toBe('evidence');
    expect(chip('D-5')?.textContent).toBe('not implemented');
    expect(chip('SC-3')?.textContent).toBe('remaining');
    expect(chip('SC-4')?.textContent).toBe('not verified');

    expect(screen.getByText('P1 · report: CSV writer per D-2')).toBeTruthy();
    expect(screen.getByText('the bucket policy is pending')).toBeTruthy();
    expect(container.querySelector('tr[data-row="SC-3"]')?.textContent).toContain('—');
    expect(container.querySelector('tr[data-row="SC-4"]')?.textContent).toContain('P2');
  });

  it('flags an inconclusive plan review as not verified', async () => {
    api.fetchDecisionTable.mockResolvedValue({
      ...table,
      planReview: { status: 'not-verified', verdict: 'INCONCLUSIVE', reasonClass: 'reviewer-timed-out', at: 'x' },
    });
    render(<DecisionTable taskId={9} />);
    expect(await screen.findByText(/plan review not verified \(reviewer-timed-out\)/)).toBeTruthy();
  });

  it('renders nothing for a plan without decisions or a spec', async () => {
    api.fetchDecisionTable.mockResolvedValue({ ...table, rows: [], counts: {} });
    const { container } = render(<DecisionTable taskId={9} />);
    await waitFor(() => expect(api.fetchDecisionTable).toHaveBeenCalled());
    expect(container.querySelector('table')).toBeNull();
  });

  it('says so when the table cannot be read', async () => {
    api.fetchDecisionTable.mockRejectedValue(new Error('GET /api/epics/9/evidence: 500'));
    render(<DecisionTable taskId={9} />);
    expect(await screen.findByRole('alert')).toBeTruthy();
  });
});
