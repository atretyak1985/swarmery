// @vitest-environment jsdom
//
// The Inbox `review` kind (Phase 6 step 3, SC-13): the card and the detail body
// of a plan branch review, the client-side finding count, and the typed client's
// wire calls against a mocked fetch — the list URL the Inbox reads and the ack
// POST that takes an item out of it.

import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ackReview, countFindings, fetchPhaseReviews, fetchUnackedPlanReviews, type Review } from '../../api/reviews';
import { toItems } from './inboxModel';
import { ReviewDetail, ReviewRow } from './ReviewItem';

const NOW = Date.parse('2026-10-10T12:00:00Z');

const review = (over: Partial<Review> = {}): Review => ({
  id: 12,
  scope: 'plan',
  taskId: 9,
  planTitle: 'Order line items',
  projectSlug: 'shop',
  phaseId: null,
  phaseName: '',
  sessionUuid: 'rev-1',
  runSessionUuid: '',
  branchSetKey: 'abc',
  verdict: 'fail',
  detail: '',
  findings: [
    'P0 db/0012_line_items.sql:4 — phase 2 queries `qty`, phase 1 created `quantity`',
    '- **P1** web/orders.ts:31 — the client still sends `total` as a string',
    'Non-blocking: no P0/P1 in the docs.',
    'VERDICT: FAIL',
  ].join('\n'),
  fixRound: 0,
  costUsd: null,
  treeBefore: 't1',
  treeAfter: 't1',
  startedAt: '2026-10-10T10:00:00Z',
  finishedAt: '2026-10-10T10:06:00Z',
  ackedAt: null,
  ...over,
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('countFindings', () => {
  it('counts lines that start with a severity, not lines that mention one', () => {
    expect(countFindings(review().findings)).toBe(2);
    expect(countFindings('Zero findings: nothing blocks.\nVERDICT: PASS')).toBe(0);
    expect(countFindings('1. P1 a.go:1 — x\n2) [P0] b.go:2 — y\n  * P1 c.go:3 — z')).toBe(3);
    expect(countFindings('')).toBe(0);
  });
});

describe('review inbox item', () => {
  it('normalises a review into an item that waits but is never urgent', () => {
    const [item] = toItems({ reviews: [review()] });
    expect(item?.kind).toBe('review');
    expect(item?.key).toBe('review:12');
    expect(item?.title).toBe('Plan review: Order line items');
    expect(item?.context).toBe('failed · 2 findings');
    expect(item?.urgent).toBe(false);
    expect(item?.ageIso).toBe('2026-10-10T10:00:00Z');
    expect(toItems({ reviews: [review({ planTitle: '', findings: 'VERDICT: PASS', verdict: 'pass' })] })[0]?.title).toBe(
      'Plan review: plan #9',
    );
  });

  it('renders the card: verdict badge, findings count, plan title, started time', () => {
    render(<ReviewRow review={review()} selected={false} now={NOW} />);
    expect(screen.getByText('Plan review: Order line items')).toBeTruthy();
    expect(screen.getByText('review failed')).toBeTruthy();
    expect(screen.getByText(/2 findings · started 2 h ago/)).toBeTruthy();
  });

  it('renders the detail: findings monospace and scrollable, a link to the plan', () => {
    render(
      <MemoryRouter>
        <ReviewDetail review={review({ verdict: 'inconclusive', detail: 'not-verifiable: 302 files' })} now={NOW} />
      </MemoryRouter>,
    );
    const block = screen.getByLabelText('review findings');
    expect(block.tagName).toBe('PRE');
    expect(block.className).toContain('overflow-auto');
    expect(block.className).toContain('font-mono');
    expect(block.textContent).toContain('phase 2 queries `qty`');
    expect(screen.getByText('review inconclusive')).toBeTruthy();
    expect(screen.getByText('not-verifiable: 302 files')).toBeTruthy();
    expect(screen.getByRole('link', { name: /open the plan/ }).getAttribute('href')).toBe('/p/shop/plans?task=9');
  });

  it('has no plan link once the plan is gone', () => {
    render(
      <MemoryRouter>
        <ReviewDetail review={review({ projectSlug: '' })} now={NOW} />
      </MemoryRouter>,
    );
    expect(screen.queryByRole('link', { name: /open the plan/ })).toBeNull();
  });
});

describe('reviews client (mocked fetch)', () => {
  function stubFetch(body: unknown, status = 200): ReturnType<typeof vi.fn> {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status }));
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  it('lists the unacked plan reviews', async () => {
    const fetchMock = stubFetch([review()]);
    const rows = await fetchUnackedPlanReviews();
    expect(fetchMock).toHaveBeenCalledWith('/api/reviews?scope=plan&unacked=1');
    expect(rows.map((r) => r.id)).toEqual([12]);
  });

  it('acks with a POST and returns the acked row', async () => {
    const fetchMock = stubFetch(review({ ackedAt: '2026-10-10T12:00:00Z' }));
    const acked = await ackReview(12);
    expect(fetchMock).toHaveBeenCalledWith('/api/reviews/12/ack', { method: 'POST' });
    expect(acked?.ackedAt).toBe('2026-10-10T12:00:00Z');
  });

  it("surfaces the daemon's error on a failed ack", async () => {
    stubFetch({ error: 'review not found' }, 404);
    await expect(ackReview(99)).rejects.toThrow('review not found');
  });

  it('reads one phase’s reviews', async () => {
    const fetchMock = stubFetch([]);
    await fetchPhaseReviews(9, 207);
    expect(fetchMock).toHaveBeenCalledWith('/api/epics/9/phases/207/reviews');
  });
});
