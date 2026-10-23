// @vitest-environment jsdom
//
// DecisionTable: the plan tab's decision → phase → evidence table — one row per
// id with its phases and a chip (glyph + word, never colour alone), the per-chip
// counts and the plan review state in the header, the inline evidence expander,
// the not-verified banner (never an empty table) and the empty state.

import { cleanup, fireEvent, render, screen, within } from '../../test/render';
import { afterEach, describe, expect, it } from 'vitest';
import type { DecisionTable as DecisionTableData } from '../../api/types';
import { DecisionTable } from './DecisionTable';

afterEach(cleanup);

const TABLE: DecisionTableData = {
  planReview: { status: 'pass', reason: '', detail: '', at: '2026-10-23T09:00:00Z' },
  rows: [
    {
      id: 'D-2',
      kind: 'decision',
      text: 'One table or a column?',
      answer: 'a column',
      status: 'decided',
      chip: 'evidence',
      reason: '',
      phases: [1, 3],
      evidence: [
        {
          kind: 'report',
          status: 'evidence',
          source: 'doc',
          phaseId: 207,
          phaseSeq: 1,
          excerpt: 'D-2: added the column',
          reason: '',
        },
      ],
    },
    {
      id: 'SC-4',
      kind: 'criterion',
      text: 'The table renders inline',
      answer: '',
      status: 'open',
      chip: 'remaining',
      reason: '',
      phases: [],
      evidence: [],
    },
    {
      id: 'D-5',
      kind: 'decision',
      text: 'Backfill old plans?',
      answer: 'no',
      status: 'decided',
      chip: 'not-implemented',
      reason: 'old plans are not rewritten',
      phases: [2],
      evidence: [
        {
          kind: 'report',
          status: 'not-implemented',
          source: 'doc',
          phaseId: null,
          phaseSeq: null,
          excerpt: 'not implemented: D-5 — old plans are not rewritten',
          reason: 'old plans are not rewritten',
        },
      ],
    },
    {
      id: 'SC-7',
      kind: 'criterion',
      text: 'Drift is a finding',
      answer: '',
      status: 'open',
      chip: 'not-verified',
      reason: 'reviewer-timed-out',
      phases: [4],
      evidence: [],
    },
  ],
  counts: { evidence: 1, remaining: 1, notImplemented: 1, notVerified: 1 },
};

function rowOf(id: string): HTMLElement {
  const cell = screen.getByRole('cell', { name: id });
  const row = cell.closest('tr');
  if (row === null) throw new Error(`no row for ${id}`);
  return row;
}

describe('DecisionTable', () => {
  it('renders one row per id with its phases and a distinct, worded chip', () => {
    render(<DecisionTable table={TABLE} />);
    const table = screen.getByRole('table');
    expect(within(table).getAllByRole('row')).toHaveLength(5); // header + 4

    const d2 = rowOf('D-2');
    expect(d2.textContent).toContain('One table or a column?');
    expect(d2.textContent).toContain('→ a column');
    expect(d2.textContent).toContain('P1, P3');
    expect(d2.querySelector('[data-chip="evidence"]')?.textContent).toContain('evidence');

    const sc4 = rowOf('SC-4');
    expect(sc4.textContent).toContain('—');
    expect(sc4.querySelector('[data-chip="remaining"]')?.textContent).toContain('remaining');
    // Nothing to expand on a bare remaining row.
    expect(within(sc4).queryByRole('button')).toBeNull();

    expect(rowOf('D-5').querySelector('[data-chip="not-implemented"]')?.textContent).toContain('not implemented');
    expect(rowOf('SC-7').querySelector('[data-chip="not-verified"]')?.textContent).toContain('not verified');

    // Four distinct chips, each with its own glyph — not colour alone.
    const glyphs = ['evidence', 'remaining', 'not-implemented', 'not-verified'].map(
      (c) => document.querySelector(`tbody [data-chip="${c}"] [aria-hidden="true"]`)?.textContent,
    );
    expect(new Set(glyphs).size).toBe(4);
  });

  it('shows the per-chip counts and the plan review state in the header', () => {
    render(<DecisionTable table={TABLE} />);
    const counts = screen.getByRole('list', { name: 'counts per chip' });
    expect(within(counts).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      '✓1 evidence',
      '○1 remaining',
      '⊘1 not implemented',
      '?1 not verified',
    ]);
    expect(document.querySelector('[data-plan-review="pass"]')?.textContent).toBe('plan review passed');
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('expands a row inline to its evidence and reason', () => {
    render(<DecisionTable table={TABLE} />);
    const toggle = within(rowOf('D-5')).getByRole('button', { name: 'show D-5' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(screen.queryByText(/not implemented: D-5/)).toBeNull();

    fireEvent.click(toggle);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(toggle.textContent).toBe('hide D-5');
    expect(screen.getByText('not implemented: D-5 — old plans are not rewritten')).toBeTruthy();
    expect(screen.getByText('reason: old plans are not rewritten')).toBeTruthy();

    fireEvent.click(within(rowOf('D-2')).getByRole('button', { name: 'show D-2' }));
    const details = document.getElementById('decision-evidence-D-2');
    expect(details?.textContent).toContain('D-2: added the column');
    expect(details?.textContent).toContain('P1');

    fireEvent.click(toggle);
    expect(screen.queryByText('not implemented: D-5 — old plans are not rewritten')).toBeNull();
  });

  it('says "not verified" with the reason for an inconclusive plan review, never an empty table', () => {
    render(
      <DecisionTable
        table={{
          ...TABLE,
          planReview: { status: 'not-verified', reason: 'reviewer-timed-out', detail: 'reviewer-timed-out: 30m', at: '' },
        }}
      />,
    );
    expect(document.querySelector('[data-plan-review="not-verified"]')?.textContent).toBe('not verified');
    expect(screen.getByRole('status').textContent).toContain('Not verified: reviewer-timed-out');
    expect(screen.getByRole('table')).toBeTruthy();
  });

  it('keeps the not-verified banner on a plan with no rows', () => {
    render(
      <DecisionTable
        table={{
          planReview: { status: 'not-verified', reason: '', detail: '', at: '' },
          rows: [],
          counts: { evidence: 0, remaining: 0, notImplemented: 0, notVerified: 0 },
        }}
      />,
    );
    expect(screen.getByRole('status').textContent).toContain('Not verified: no reason recorded');
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows an empty state for a plan with no decisions or criteria', () => {
    render(
      <DecisionTable
        table={{
          planReview: { status: 'none', reason: '', detail: '', at: '' },
          rows: [],
          counts: { evidence: 0, remaining: 0, notImplemented: 0, notVerified: 0 },
        }}
      />,
    );
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByRole('list', { name: 'counts per chip' })).toBeNull();
    expect(screen.getByText(/declares no decisions and no spec criteria/)).toBeTruthy();
    expect(document.querySelector('[data-plan-review="none"]')?.textContent).toBe('no plan review yet');
  });
});
