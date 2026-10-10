// @vitest-environment jsdom
//
// The phase panel's Runs tab Review block (Phase 6 step 3): the LATEST phase
// review (item [0] of the epic payload's `phase.reviews`) with its verdict,
// detail and fix round, findings — fetched for that review only — collapsed;
// nothing at all for a phase never reviewed. A review that lands while the panel
// is open arrives with the refetched epic and replaces the shown one.

import { act, cleanup, render, screen } from '../../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Review } from '../../api/reviews';
import type { PhaseReviewSummary } from '../../api/types';
import { RunsReviewBlock } from './RunsReviewBlock';

const review = (over: Partial<Review> = {}): Review => ({
  id: 3,
  scope: 'phase',
  taskId: 9,
  planTitle: 'Order line items',
  projectSlug: 'shop',
  phaseId: 207,
  phaseName: 'Phase 2 — Line-item CRUD',
  sessionUuid: 'rev-3',
  runSessionUuid: 'run-3',
  branchSetKey: '',
  verdict: 'pass',
  detail: '',
  findings: 'Zero findings.\nVERDICT: PASS',
  fixRound: 1,
  costUsd: null,
  treeBefore: 't',
  treeAfter: 't',
  startedAt: new Date(Date.now() - 600_000).toISOString(),
  finishedAt: null,
  ackedAt: null,
  ...over,
});

/** The epic payload's shape of a review: no findings. */
const summary = (r: Review): PhaseReviewSummary => ({
  id: r.id,
  verdict: r.verdict,
  detail: r.detail,
  fixRound: r.fixRound,
  runSessionUuid: r.runSessionUuid,
  startedAt: r.startedAt,
  finishedAt: r.finishedAt,
});

function stubFetch(body: unknown, status = 200): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status }));
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function renderBlock(reviews: Review[]): Promise<ReturnType<typeof render>> {
  const view = render(<RunsReviewBlock taskId={9} phaseId={207} reviews={reviews.map(summary)} />);
  await flush();
  return view;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('RunsReviewBlock', () => {
  it('shows the latest review: verdict, fix round, detail, findings collapsed', async () => {
    const rows = [
      review(),
      review({
        id: 2,
        verdict: 'fail',
        fixRound: 0,
        findings: '- P1 a.go:1 — wrong\nVERDICT: FAIL',
      }),
    ];
    const fetchMock = stubFetch(rows);
    await renderBlock(rows);
    expect(fetchMock).toHaveBeenCalledWith('/api/epics/9/phases/207/reviews');
    const block = screen.getByRole('region', { name: 'code review' });
    expect(block.textContent).toContain('review passed');
    expect(block.textContent).toContain('review of the fix re-run');
    expect(block.textContent).not.toContain('review failed');
    const details = block.querySelector('details');
    expect(details?.open).toBe(false);
    expect(details?.querySelector('summary')?.textContent).toBe('findings (none blocking)');
    expect(details?.querySelector('pre')?.textContent).toContain('Zero findings.');
  });

  it('counts the findings of a failed review and shows its detail', async () => {
    const rows = [
      review({
        verdict: 'inconclusive',
        fixRound: 0,
        detail: 'reviewer-mutated-tree: abc→def',
        findings: 'P0 x.go:1 — a\nP1 y.go:2 — b\nVERDICT: FAIL',
      }),
    ];
    stubFetch(rows);
    await renderBlock(rows);
    const block = screen.getByRole('region', { name: 'code review' });
    expect(block.textContent).toContain('review inconclusive');
    expect(block.textContent).toContain('first review');
    expect(block.textContent).toContain('reviewer-mutated-tree: abc→def');
    expect(block.querySelector('summary')?.textContent).toBe('findings (2)');
  });

  it('renders nothing and fetches nothing for a phase that was never reviewed', async () => {
    const fetchMock = stubFetch([]);
    await renderBlock([]);
    expect(screen.queryByRole('region', { name: 'code review' })).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('shows a review that lands while the panel is open, from the refetched epic', async () => {
    const fail = review({ id: 2, verdict: 'fail', fixRound: 0, findings: '- P1 a.go:1 — wrong\nVERDICT: FAIL' });
    const fetchMock = stubFetch([fail]);
    const view = await renderBlock([fail]);
    let block = screen.getByRole('region', { name: 'code review' });
    expect(block.textContent).toContain('review failed');
    expect(block.querySelector('summary')?.textContent).toBe('findings (1)');

    // The fix re-run's PASS is recorded; the daemon notifies and the epic comes
    // back with it on top. Nothing else about the phase changed.
    const pass = review({ id: 3 });
    fetchMock.mockImplementation(async () => new Response(JSON.stringify([pass, fail]), { status: 200 }));
    view.rerender(<RunsReviewBlock taskId={9} phaseId={207} reviews={[pass, fail].map(summary)} />);
    await flush();

    block = screen.getByRole('region', { name: 'code review' });
    expect(block.textContent).toContain('review passed');
    expect(block.textContent).toContain('review of the fix re-run');
    expect(block.querySelector('summary')?.textContent).toBe('findings (none blocking)');
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('keeps the verdict when the findings cannot be loaded', async () => {
    const rows = [review({ verdict: 'fail', fixRound: 0 })];
    stubFetch({ error: 'boom' }, 500);
    await renderBlock(rows);
    const block = screen.getByRole('region', { name: 'code review' });
    expect(block.textContent).toContain('review failed');
    expect(block.textContent).toContain("couldn't load the findings: boom");
    expect(block.querySelector('details')).toBeNull();
  });
});
