// @vitest-environment jsdom
//
// The phase panel's Runs tab Review block (Phase 6 step 3): the LATEST phase
// review (item [0] of the per-phase endpoint) with its verdict, detail and fix
// round, findings collapsed; nothing at all for a phase never reviewed.

import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Review } from '../../api/reviews';
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

function stubFetch(body: unknown, status = 200): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status }));
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

async function renderBlock(): Promise<void> {
  render(<RunsReviewBlock taskId={9} phaseId={207} version="done|run-3|t" />);
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('RunsReviewBlock', () => {
  it('shows the latest review: verdict, fix round, detail, findings collapsed', async () => {
    const fetchMock = stubFetch([
      review(),
      review({
        id: 2,
        verdict: 'fail',
        fixRound: 0,
        findings: '- P1 a.go:1 — wrong\nVERDICT: FAIL',
      }),
    ]);
    await renderBlock();
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
    stubFetch([
      review({
        verdict: 'inconclusive',
        fixRound: 0,
        detail: 'reviewer-mutated-tree: abc→def',
        findings: 'P0 x.go:1 — a\nP1 y.go:2 — b\nVERDICT: FAIL',
      }),
    ]);
    await renderBlock();
    const block = screen.getByRole('region', { name: 'code review' });
    expect(block.textContent).toContain('review inconclusive');
    expect(block.textContent).toContain('first review');
    expect(block.textContent).toContain('reviewer-mutated-tree: abc→def');
    expect(block.querySelector('summary')?.textContent).toBe('findings (2)');
  });

  it('renders nothing for a phase that was never reviewed', async () => {
    stubFetch([]);
    await renderBlock();
    expect(screen.queryByRole('region', { name: 'code review' })).toBeNull();
  });
});
