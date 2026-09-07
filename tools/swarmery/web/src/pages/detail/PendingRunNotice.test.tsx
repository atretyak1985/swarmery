// @vitest-environment jsdom
//
// The two honest states of a session that the daemon minted but ingest has not
// seen yet. Dev-only suite (see PlanRunCard.test.tsx for how to run it):
//   npx vitest run --environment jsdom src/pages/detail/PendingRunNotice.test.tsx

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PendingSession } from '../../api/types';
import { PendingRunNotice, pendingSourceLabel } from './PendingRunNotice';

afterEach(cleanup);

function pending(over: Partial<PendingSession> = {}): PendingSession {
  return {
    pending: true,
    sessionUuid: 'b1fdc524-d5b2-4737-9ab3-3e924d0d56e5',
    source: 'phase',
    running: true,
    startedAt: new Date(Date.now() - 5_000).toISOString(),
    label: 'Phase 5 — Lesson as part of DoD',
    taskId: 498,
    refId: 16256,
    projectSlug: '-Volumes-Work-english-grammar',
    ...over,
  };
}

describe('PendingRunNotice', () => {
  it('shows a waiting state, not an error, while the run is still going', () => {
    render(<PendingRunNotice run={pending()} onRetry={() => {}} />);
    expect(screen.getByTestId('pending-run')).toBeTruthy();
    expect(screen.getByRole('status').textContent).toContain('phase run is starting');
    expect(screen.getByText('Phase 5 — Lesson as part of DoD')).toBeTruthy();
    expect(screen.queryByText(/retry/i)).toBeNull();
  });

  it('turns into a retryable error once the run ended without a transcript', () => {
    const onRetry = vi.fn();
    render(<PendingRunNotice run={pending({ running: false })} onRetry={onRetry} />);
    expect(screen.queryByTestId('pending-run')).toBeNull();
    expect(screen.getByText(/ended without writing a transcript/)).toBeTruthy();
    fireEvent.click(screen.getByText(/retry/i));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it('names every run kind the daemon can mint, with a fallback for new ones', () => {
    expect(pendingSourceLabel('phase')).toBe('phase run');
    expect(pendingSourceLabel('plan')).toBe('plan run');
    expect(pendingSourceLabel('dispatch')).toBe('dispatched task');
    expect(pendingSourceLabel('verify')).toBe('verification run');
    expect(pendingSourceLabel('planning')).toBe('planning session');
    expect(pendingSourceLabel('revision')).toBe('plan revision');
    expect(pendingSourceLabel('something-new')).toBe('run');
  });
});
