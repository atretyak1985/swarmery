// useReverify (phase-reverify plan, phase 2): the in-flight state of the
// "Verify again" request. After a 202 the phase stays busy through a grace
// period and the page refetches twice — the server writes its `running`
// verification row from a goroutine, so the first refetch may still read
// verifyRunning=false and the button must not flash back to enabled.

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ACCEPT_GRACE_MS, useReverify } from './useReverify';

vi.mock('../../api/phasereport', () => ({ reverifyPhase: vi.fn() }));
import { reverifyPhase } from '../../api/phasereport';

const reverifyMock = vi.mocked(reverifyPhase);

beforeEach(() => {
  vi.useFakeTimers();
  reverifyMock.mockReset();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('useReverify', () => {
  it('stays busy through the grace period after a 202 and refetches twice', async () => {
    reverifyMock.mockResolvedValueOnce({ status: 'verifying' });
    const onAccepted = vi.fn();
    const onRefused = vi.fn();
    const { result } = renderHook(() => useReverify({ onAccepted, onRefused }));

    act(() => result.current.start(7, 11));
    expect(result.current.busy.has(11)).toBe(true);

    await act(async () => {
      await Promise.resolve();
    });
    expect(onAccepted).toHaveBeenCalledTimes(1);
    expect(result.current.busy.has(11)).toBe(true);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACCEPT_GRACE_MS);
    });
    expect(onAccepted).toHaveBeenCalledTimes(2);
    expect(result.current.busy.has(11)).toBe(false);
    expect(onRefused).not.toHaveBeenCalled();
  });

  it('calls the latest handlers after the grace, not the ones captured at press time', async () => {
    reverifyMock.mockResolvedValueOnce({ status: 'verifying' });
    const first = vi.fn();
    const second = vi.fn();
    const onRefused = vi.fn();
    const { result, rerender } = renderHook(({ onAccepted }) => useReverify({ onAccepted, onRefused }), {
      initialProps: { onAccepted: first },
    });

    act(() => result.current.start(7, 11));
    await act(async () => {
      await Promise.resolve();
    });
    expect(first).toHaveBeenCalledTimes(1);

    // The page switched project during the grace: its reload is a new callback.
    rerender({ onAccepted: second });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACCEPT_GRACE_MS);
    });
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);
  });

  it('clears busy at once on a refusal and reports it', async () => {
    const err = new Error('a run or a re-verification is already active for this phase');
    reverifyMock.mockRejectedValueOnce(err);
    const onAccepted = vi.fn();
    const onRefused = vi.fn();
    const { result } = renderHook(() => useReverify({ onAccepted, onRefused }));

    act(() => result.current.start(7, 11));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(onRefused).toHaveBeenCalledWith(7, err);
    expect(onAccepted).not.toHaveBeenCalled();
    expect(result.current.busy.has(11)).toBe(false);
  });
});
