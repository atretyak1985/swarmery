// @vitest-environment jsdom
//
// useTriage: polls the active run every 2 s while one runs, stops when it ends,
// fires onRunEnd once, and leaves no timer behind on unmount.

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api/triage';
import type { TriageRun, TriageVerdict } from '../../api/triage';
import { TRIAGE_POLL_MS, useTriage } from './useTriage';

vi.mock('../../api/triage', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/triage')>();
  return {
    ...actual,
    fetchActiveTriageRun: vi.fn(),
    fetchTriageRun: vi.fn(),
    fetchTriageVerdicts: vi.fn(),
    fetchTriageAudit: vi.fn(),
    startTriageRun: vi.fn(),
  };
});

const active = vi.mocked(api.fetchActiveTriageRun);
const finished = vi.mocked(api.fetchTriageRun);
const verdicts = vi.mocked(api.fetchTriageVerdicts);
const audit = vi.mocked(api.fetchTriageAudit);
const startRun = vi.mocked(api.startTriageRun);

function run(over: Partial<TriageRun> = {}): TriageRun {
  return {
    id: 7,
    trigger: 'operator',
    scopeProjectId: null,
    kinds: [],
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

function verdict(id: number, state: TriageVerdict['state'], createdAt: string): TriageVerdict {
  return {
    id,
    runId: 7,
    kind: 'lesson',
    class: '',
    ref: String(id),
    itemKey: '',
    title: '',
    value: 'dismiss',
    reason: '',
    payload: null,
    prior: null,
    state,
    createdAt,
    decidedAt: null,
    projectId: null,
  };
}

/** Let pending promise callbacks run without moving the clock. */
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
  vi.setSystemTime(new Date('2026-10-05T12:00:00Z'));
  active.mockReset();
  finished.mockReset();
  verdicts.mockReset();
  audit.mockReset();
  startRun.mockReset();
  verdicts.mockResolvedValue([]);
  audit.mockResolvedValue({ answered: 0, agree: 0, byQuestion: [] });
});
afterEach(() => {
  vi.useRealTimers();
});

describe('useTriage', () => {
  it('does not poll while no run is active', async () => {
    active.mockResolvedValue(null);
    const { unmount } = renderHook(() => useTriage(null));
    await flush();
    expect(active).toHaveBeenCalledTimes(1);
    await advance(TRIAGE_POLL_MS * 5);
    expect(active).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
    unmount();
  });

  it('polls every 2 s while a run is active, then fetches it once, reloads and calls onRunEnd once', async () => {
    const onRunEnd = vi.fn();
    active
      .mockResolvedValueOnce(run()) // mount
      .mockResolvedValueOnce(run({ done: 5 })) // poll 1
      .mockResolvedValue(null); // poll 2: over
    finished.mockResolvedValue(run({ status: 'ok', done: 10, finishedAt: '2026-10-05T12:00:05Z' }));
    const { result, unmount } = renderHook(() => useTriage(null, onRunEnd));
    await flush();
    expect(result.current.run?.done).toBe(2);

    await advance(TRIAGE_POLL_MS);
    expect(active).toHaveBeenCalledTimes(2);
    expect(result.current.run?.done).toBe(5);
    expect(onRunEnd).not.toHaveBeenCalled();

    const loadsBefore = verdicts.mock.calls.length;
    await advance(TRIAGE_POLL_MS);
    expect(active).toHaveBeenCalledTimes(3);
    expect(finished).toHaveBeenCalledTimes(1);
    expect(finished).toHaveBeenCalledWith(7);
    expect(result.current.run).toBeNull();
    expect(result.current.lastRun?.status).toBe('ok');
    expect(onRunEnd).toHaveBeenCalledTimes(1);
    expect(verdicts.mock.calls.length).toBeGreaterThan(loadsBefore);

    await advance(TRIAGE_POLL_MS * 5);
    expect(active).toHaveBeenCalledTimes(3);
    expect(onRunEnd).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
    unmount();
  });

  it('unmount clears the poll timer', async () => {
    active.mockResolvedValue(run());
    const { unmount } = renderHook(() => useTriage(null));
    await flush();
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
    await advance(TRIAGE_POLL_MS * 3);
    expect(active).toHaveBeenCalledTimes(1);
  });

  it('splits verdicts: open = suggested + sample, handled = applied in the last 7 days', async () => {
    active.mockResolvedValue(null);
    verdicts.mockImplementation((states) =>
      Promise.resolve(
        states.includes('applied')
          ? [verdict(3, 'applied', '2026-10-04T12:00:00Z'), verdict(4, 'applied', '2026-09-20T12:00:00Z')]
          : [verdict(1, 'suggested', '2026-10-05T09:00:00Z'), verdict(2, 'sample', '2026-10-05T09:00:00Z')],
      ),
    );
    const { result } = renderHook(() => useTriage('web'));
    await flush();
    expect(verdicts).toHaveBeenCalledWith(['suggested', 'sample'], 'web');
    expect(result.current.open.map((v) => v.id)).toEqual([1, 2]);
    expect(result.current.handled.map((v) => v.id)).toEqual([3]);
  });

  it('start: begins polling the run; a busy error is not shown, other errors are', async () => {
    active.mockResolvedValueOnce(null).mockResolvedValue(run());
    startRun.mockRejectedValueOnce(new api.TriageBusyError(7));
    const { result } = renderHook(() => useTriage(null));
    await flush();
    await act(async () => {
      await result.current.start();
    });
    expect(result.current.startError).toBeNull();
    expect(result.current.run?.id).toBe(7);

    startRun.mockRejectedValueOnce(new Error('POST /api/triage/runs: 503'));
    await act(async () => {
      await result.current.start();
    });
    expect(result.current.startError).toContain('503');
  });

  it('a run that already ended by the first poll still reports its result, once', async () => {
    const onRunEnd = vi.fn();
    active.mockResolvedValue(null);
    startRun.mockResolvedValue({ id: 7 });
    finished.mockResolvedValue(run({ status: 'ok', done: 10, applied: 4, finishedAt: '2026-10-05T12:00:01Z' }));
    const { result, unmount } = renderHook(() => useTriage(null, onRunEnd));
    await flush();
    const loadsBefore = verdicts.mock.calls.length;
    await act(async () => {
      await result.current.start();
    });
    await advance(TRIAGE_POLL_MS * 3);
    expect(finished).toHaveBeenCalledTimes(1);
    expect(finished).toHaveBeenCalledWith(7);
    expect(result.current.lastRun?.applied).toBe(4);
    expect(result.current.run).toBeNull();
    expect(onRunEnd).toHaveBeenCalledTimes(1);
    expect(verdicts.mock.calls.length).toBeGreaterThan(loadsBefore);
    unmount();
  });

  it('a new start forgets the previous run result before the new run reports', async () => {
    active.mockResolvedValue(null);
    startRun.mockResolvedValueOnce({ id: 7 });
    finished.mockResolvedValueOnce(run({ status: 'failed', error: 'budget exhausted' }));
    const { result, unmount } = renderHook(() => useTriage(null));
    await flush();
    await act(async () => {
      await result.current.start();
    });
    expect(result.current.lastRun?.status).toBe('failed');

    startRun.mockResolvedValueOnce({ id: 8 });
    active.mockResolvedValue(run({ id: 8 }));
    await act(async () => {
      await result.current.start();
    });
    expect(result.current.run?.id).toBe(8);
    expect(result.current.lastRun).toBeNull();
    unmount();
  });

  it('a failed "active" request right after a start does not report a running run as finished', async () => {
    const onRunEnd = vi.fn();
    active.mockResolvedValueOnce(null); // mount
    startRun.mockResolvedValue({ id: 7 });
    active.mockRejectedValueOnce(new Error('network')); // right after the start
    finished.mockResolvedValueOnce(run({ done: 1 })); // still running
    active.mockResolvedValue(run({ done: 3 })); // the poller
    const { result, unmount } = renderHook(() => useTriage(null, onRunEnd));
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

  it('enabled = false makes no requests', async () => {
    const { result, unmount } = renderHook(() => useTriage(null, undefined, false));
    await flush();
    expect(active).not.toHaveBeenCalled();
    expect(verdicts).not.toHaveBeenCalled();
    expect(result.current.open).toEqual([]);
    unmount();
  });
});
