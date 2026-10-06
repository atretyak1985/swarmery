import { useEffect, useState } from 'react';
import type { Session } from '../api/types';

/** Simplified visible state: the whole UI speaks only these three. */
export type SessionState = 'running' | 'stuck' | 'done';

/** Quiet time after which an alive-but-silent session counts as stuck.
 * Matches the server's idle→completed threshold (ingest.DefaultThresholds: 30 min). */
export const STUCK_AFTER_MS = 30 * 60_000;

const ALIVE = new Set(['running', 'orphaned']);

/** Milliseconds since the session's last transcript activity. */
export function quietMs(s: Session, nowMs: number): number {
  return nowMs - Date.parse(s.endedAt ?? s.startedAt);
}

/**
 * Collapse status × procState × quiet-time into the tri-state.
 * INVARIANT this leans on: `endedAt` is "last transcript activity" — ingest
 * advances it on every batch (internal/ingest/ingest.go), even for live rows —
 * so `nowMs - endedAt` is how long the session has been silent.
 */
export function sessionState(s: Session, nowMs: number): SessionState {
  if (s.status === 'completed' || s.status === 'killed') return 'done';
  // awaiting_reply: the process is alive and waiting on the operator — running.
  if (s.status === 'active' || s.status === 'waiting_approval' || s.status === 'awaiting_reply') {
    return 'running';
  }
  // idle — decide by quiet time + process liveness
  if (quietMs(s, nowMs) < STUCK_AFTER_MS) return 'running';
  return s.procState != null && ALIVE.has(s.procState) ? 'stuck' : 'done';
}

/** Re-render tick so `stuck` appears without a WS event at the 30-min boundary. */
export function useNowMs(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
  return now;
}
