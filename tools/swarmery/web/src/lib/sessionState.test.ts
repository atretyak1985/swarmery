// sessionState() — the tri-state collapse. Pure logic, no DOM.
// web/tsconfig.json EXCLUDES *.test.ts, so treat the casts as documentation.

import { describe, expect, it } from 'vitest';
import type { Session, SessionStatus } from '../api/types';
import { STUCK_AFTER_MS, sessionState } from './sessionState';

const NOW = Date.parse('2026-10-06T12:00:00Z');

function makeSession(status: SessionStatus, quietMs: number, procState: string | null): Session {
  return {
    id: 1,
    projectId: 1,
    projectSlug: 'p',
    projectName: 'p',
    sessionUuid: 'u-1',
    model: 'opus',
    gitBranch: null,
    cwd: null,
    status,
    startedAt: new Date(NOW - quietMs - 60_000).toISOString(),
    endedAt: new Date(NOW - quietMs).toISOString(),
    title: null,
    source: 'jsonl' as Session['source'],
    procState,
  } as Session;
}

describe('sessionState', () => {
  it('reads awaiting_reply as running, however long it has been quiet', () => {
    expect(sessionState(makeSession('awaiting_reply', 4 * 60_000, 'running'), NOW)).toBe('running');
    expect(sessionState(makeSession('awaiting_reply', STUCK_AFTER_MS * 6, 'running'), NOW)).toBe(
      'running',
    );
  });

  it('keeps the existing mapping for the other statuses', () => {
    expect(sessionState(makeSession('waiting_approval', 0, null), NOW)).toBe('running');
    expect(sessionState(makeSession('completed', 0, null), NOW)).toBe('done');
    expect(sessionState(makeSession('idle', STUCK_AFTER_MS + 1, 'running'), NOW)).toBe('stuck');
    expect(sessionState(makeSession('idle', STUCK_AFTER_MS + 1, null), NOW)).toBe('done');
  });
});
