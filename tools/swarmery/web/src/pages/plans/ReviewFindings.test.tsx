// @vitest-environment jsdom
//
// ReviewFindings: the Review tab's findings table — rounds oldest first, each
// finding's columns and resolution, the round chips (verdict, source,
// incomplete, unparsable) and the D21 legacy badge.

import { cleanup, render, screen, within } from '../../test/render';
import { afterEach, describe, expect, it } from 'vitest';
import type { ReviewBlock } from '../../api/types';
import { hasReviewFindings, showsReviewTab } from './landingModel';
import { ReviewFindings } from './ReviewFindings';

afterEach(cleanup);

const ROUND1: ReviewBlock = {
  round: '1',
  verdict: 'FAIL',
  complete: true,
  scopeLeft: [],
  checkedCallers: [],
  resolves: [],
  unparsable: false,
  source: 'backfill',
  writtenAt: '',
  findings: [
    {
      id: 'R1-1',
      type: 'silent-failure',
      file: 'internal/x.go',
      line: 84,
      criterion: 'SC-3',
      testCaught: true,
      evidence: 'TestClassifyRedactsBeforeTruncating',
      note: 'secret survived the cut',
      resolvedBy: '2',
    },
    {
      id: 'R1-2',
      type: 'contract',
      file: 'web/a.ts',
      line: null,
      criterion: '',
      testCaught: false,
      evidence: 'commit abc1234',
      note: '',
      resolvedBy: null,
    },
  ],
};

const ROUND2: ReviewBlock = {
  ...ROUND1,
  round: '2',
  verdict: 'PASS',
  complete: false,
  scopeLeft: ['DiffView/TaskDiff', 'PhaseCard'],
  resolves: ['R1-1'],
  unparsable: true,
  source: 'daemon',
  findings: [],
};

describe('ReviewFindings', () => {
  it('renders each round with its findings, oldest first', () => {
    render(<ReviewFindings blocks={[ROUND1, ROUND2]} legacy={false} />);
    const top = screen.getByRole('region', { name: 'Review findings' });
    const rounds = within(top).getAllByRole('region');
    expect(rounds.map((r) => r.getAttribute('aria-label'))).toEqual(['Round 1', 'Round 2']);

    const r1 = rounds[0]!;
    expect(within(r1).getByText('FAIL')).toBeTruthy();
    expect(within(r1).getByText('backfilled')).toBeTruthy();
    const rows = within(r1).getAllByRole('row').slice(1); // minus the header
    expect(rows).toHaveLength(2);
    const first = within(rows[0]!);
    expect(first.getByText('R1-1')).toBeTruthy();
    expect(first.getByText('silent-failure')).toBeTruthy();
    expect(first.getByText('internal/x.go:84')).toBeTruthy();
    expect(first.getByText('SC-3')).toBeTruthy();
    expect(first.getByText('yes')).toBeTruthy();
    expect(first.getByText('TestClassifyRedactsBeforeTruncating')).toBeTruthy();
    expect(first.getByText('secret survived the cut')).toBeTruthy();
    expect(first.getByText('resolved in round 2')).toBeTruthy();
    const second = within(rows[1]!);
    expect(second.getByText('web/a.ts')).toBeTruthy();
    expect(second.getByText('no')).toBeTruthy();
    expect(second.getByText('open')).toBeTruthy();

    const r2 = rounds[1]!;
    expect(within(r2).getByText('PASS')).toBeTruthy();
    expect(within(r2).getByText('daemon review')).toBeTruthy();
    expect(within(r2).getByText('incomplete')).toBeTruthy();
    expect(within(r2).getByText('unparsable')).toBeTruthy();
    expect(within(r2).getByText('not reviewed: DiffView/TaskDiff, PhaseCard')).toBeTruthy();
    expect(within(r2).getByText('No findings.')).toBeTruthy();
    expect(screen.queryByText('legacy review')).toBeNull();
  });

  it('shows the legacy badge for a phase finished before findings existed', () => {
    render(<ReviewFindings blocks={[]} legacy />);
    const top = screen.getByRole('region', { name: 'Review findings' });
    expect(within(top).getByText('legacy review')).toBeTruthy();
    expect(within(top).queryAllByRole('table')).toHaveLength(0);
  });

  it('renders nothing when there is no round and no badge', () => {
    const { container } = render(<ReviewFindings blocks={[]} legacy={false} />);
    expect(container.innerHTML).toBe('');
  });

  it('holds an incomplete latest round: badge, reason and the two operator options (D20)', () => {
    const hold = 'round 2: review-incomplete: reviewer-timed-out — killed by the hard timeout';
    render(<ReviewFindings blocks={[ROUND1, ROUND2]} legacy={false} hold={hold} />);
    const top = screen.getByRole('region', { name: 'Review findings' });
    expect(within(top).getByText('review incomplete')).toBeTruthy();
    expect(within(top).getByText('2 review rounds')).toBeTruthy();
    const banner = within(top).getByRole('status', { name: 'Review incomplete' });
    expect(within(banner).getByText('Review incomplete — not a passed review')).toBeTruthy();
    expect(within(banner).getByText(hold)).toBeTruthy();
    expect(banner.textContent).toContain('repeat the review, or accept it as is');
    expect(banner.textContent).toContain('at most 2 review rounds');
  });

  it('shows no hold for a complete latest round, and counts one round in the singular', () => {
    render(<ReviewFindings blocks={[ROUND1, { ...ROUND2, round: 'plan' }]} legacy={false} hold="" />);
    expect(screen.queryByRole('status', { name: 'Review incomplete' })).toBeNull();
    expect(screen.queryByText('review incomplete')).toBeNull();
    expect(screen.getByText('1 review round')).toBeTruthy();
  });

  it('names the plan round', () => {
    render(<ReviewFindings blocks={[{ ...ROUND2, round: 'plan', unparsable: false }]} legacy={false} />);
    expect(screen.getByRole('region', { name: 'Plan round' })).toBeTruthy();
  });
});

describe('showsReviewTab', () => {
  const never = { runState: 'idle' as const, runSessionUuid: null, landing: null };
  it('opens the Review tab for findings or the legacy badge on a never-run phase', () => {
    expect(showsReviewTab(never)).toBe(false);
    expect(hasReviewFindings({ reviewBlocks: [ROUND1] })).toBe(true);
    expect(showsReviewTab({ ...never, reviewBlocks: [ROUND1] })).toBe(true);
    expect(showsReviewTab({ ...never, reviewBlocks: [], legacyReview: true })).toBe(true);
    expect(showsReviewTab({ ...never, runState: 'done' })).toBe(true);
  });
});
