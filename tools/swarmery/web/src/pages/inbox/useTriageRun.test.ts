// @vitest-environment jsdom
//
// useTriageRun: the run half both triggers share (Inbox, Health → Friction).
// start forwards its options; a busy start follows the active run; a fast run
// reports once; a failed "active" request lets the run's own status decide; the
// poll stops and onRunEnd fires once; enabled = false makes no requests.

import { act, renderHook } from '../../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api/triage';
import type { TriageRun } from '../../api/triage';
import { TRIAGE_POLL_MS, useTriageRun } from './useTriageRun';

vi.mock('../../api/triage', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/triage')>();
  return {
    ...actual,
    fetchActiveTriageRun: vi.fn(),
    fetchTriageRun: vi.fn(),
    startTriageRun: vi.fn(),
  };
});

const active = vi.mocked(api.fetchActiveTriageRun);
const finished = vi.mocked(api.fetchTriageRun);
const startRun = vi.mocked(api.startTriageRun);

function run(over: Partial<TriageRun> = {}): TriageRun {
  return {
    id: 7,
    trigger: 'operator',
    scopeProjectId: null,
    kinds: ['friction', 'agent'],
    status: 'running',
    total: 10,
    done: 2,
    applied: 0,
    suggested: 0,
    skipped: 0,
    failed: 0,
    rejected: 0,
    costUsd: 0,
    sessionUuids: [],
    error: '',
    startedAt: '2026-10-05T09:00:00Z',
    finishedAt: null,
    ...over,
  };
}

async function flush(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  active.mockReset();
  finished.mockReset();
  startRun.mockReset();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('useTriageRun', () => {
  it('start forwards its options with the scope', async () => {
    active.mockResolvedValueOnce(null).mockResolvedValue(run());
    startRun.mockResolvedValue({ id: 7 });
    const { result, unmount } = renderHook(() => useTriageRun('shop'));
    await flush();
    const opts = { kinds: ['friction', 'agent'], cap: 6 };
    await act(async () => {
      await result.current.start(opts);
    });
    expect(startRun).toHaveBeenCalledWith('shop', opts);
    expect(result.current.run?.id).toBe(7);
    unmount();
  });

  it('a busy start follows the active run and shows no error', async () => {
    active.mockResolvedValueOnce(null).mockResolvedValue(run({ id: 3, done: 4, total: 9 }));
    startRun.mockRejectedValue(new api.TriageBusyError(3));
    const { result, unmount } = renderHook(() => useTriageRun(null));
    await flush();
    await act(async () => {
      await result.current.start({ kinds: ['friction', 'agent'], cap: 6 });
    });
    expect(startRun).toHaveBeenCalledTimes(1);
    expect(result.current.startError).toBeNull();
    expect(result.current.run?.id).toBe(3);
    expect(result.current.run?.done).toBe(4);
    unmount();
  });

  it('any other start failure is the error, and no run is followed', async () => {
    active.mockResolvedValue(null);
    startRun.mockRejectedValue(new Error('POST /api/triage/runs: 503'));
    const { result, unmount } = renderHook(() => useTriageRun(null));
    await flush();
    await act(async () => {
      await result.current.start();
    });
    expect(result.current.startError).toContain('503');
    expect(result.current.run).toBeNull();
    unmount();
  });

  it('a run that ended before the first poll reports its result once', async () => {
    const onRunEnd = vi.fn();
    active.mockResolvedValue(null);
    startRun.mockResolvedValue({ id: 7 });
    finished.mockResolvedValue(run({ status: 'ok', done: 10, applied: 3, finishedAt: '2026-10-05T12:00:01Z' }));
    const { result, unmount } = renderHook(() => useTriageRun(null, onRunEnd));
    await flush();
    await act(async () => {
      await result.current.start();
    });
    await advance(TRIAGE_POLL_MS * 3);
    expect(finished).toHaveBeenCalledTimes(1);
    expect(result.current.lastRun?.applied).toBe(3);
    expect(result.current.run).toBeNull();
    expect(onRunEnd).toHaveBeenCalledTimes(1);
    unmount();
  });

  it('a failed "active" request after a start lets the run\'s own status decide', async () => {
    const onRunEnd = vi.fn();
    active.mockResolvedValueOnce(null); // mount
    startRun.mockResolvedValue({ id: 7 });
    active.mockRejectedValueOnce(new Error('network')); // right after the start
    finished.mockResolvedValueOnce(run({ done: 1 })); // still running
    active.mockResolvedValue(run({ done: 3 })); // the poller
    const { result, unmount } = renderHook(() => useTriageRun(null, onRunEnd));
    await flush();
    await act(async () => {
      await result.current.start();
    });
    expect(result.current.run?.done).toBe(1);
    expect(result.current.lastRun).toBeNull();
    expect(onRunEnd).not.toHaveBeenCalled();
    await advance(TRIAGE_POLL_MS);
    expect(result.current.run?.done).toBe(3);
    unmount();
  });

  it('polls while a run is active, then stops and calls onRunEnd once', async () => {
    const onRunEnd = vi.fn();
    active
      .mockResolvedValueOnce(run()) // mount
      .mockResolvedValueOnce(run({ done: 6 })) // poll 1
      .mockResolvedValue(null); // poll 2: over
    finished.mockResolvedValue(run({ status: 'ok', done: 10, finishedAt: '2026-10-05T12:00:05Z' }));
    const { result, unmount } = renderHook(() => useTriageRun(null, onRunEnd));
    await flush();
    expect(result.current.run?.done).toBe(2);
    await advance(TRIAGE_POLL_MS);
    expect(result.current.run?.done).toBe(6);
    expect(onRunEnd).not.toHaveBeenCalled();
    await advance(TRIAGE_POLL_MS);
    expect(result.current.run).toBeNull();
    expect(result.current.lastRun?.status).toBe('ok');
    expect(onRunEnd).toHaveBeenCalledTimes(1);
    await advance(TRIAGE_POLL_MS * 5);
    expect(active).toHaveBeenCalledTimes(3);
    expect(onRunEnd).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
    unmount();
  });

  it('enabled = false makes no requests', async () => {
    const { result, unmount } = renderHook(() => useTriageRun(null, undefined, false));
    await flush();
    await advance(TRIAGE_POLL_MS * 3);
    expect(active).not.toHaveBeenCalled();
    expect(result.current.run).toBeNull();
    unmount();
  });
});
