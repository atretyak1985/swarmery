// Fake /api/ws feed for VITE_MOCK=1 — periodically emits contract-shaped
// WSMessages so live UI paths (upserts, appends, approvals) are exercised
// offline. There is only ever ONE mock socket (lib/ws.ts shares a single
// connection app-wide), so it also owns the approvals scenario: a
// permission_requested pushed ~3 s after load and the expiry sweep.

import type { Event, Session, WSMessage } from '../api/types';
import { mockSessions } from './data';
import {
  injectMockPermissionRequest,
  setMockPermissionEmitter,
  sweepMockExpiry,
} from './approvals';

const INJECT_DELAY_MS = 3_000;

const MOCK_ACTIONS = [
  { toolName: 'Bash', payload: { command: 'go test ./internal/ingest/...' } },
  { toolName: 'Read', payload: { file_path: 'internal/api/handlers.go' } },
  { toolName: 'Edit', payload: { file_path: 'web/src/pages/Overview.tsx' } },
] as const;

function mockEvent(tick: number): Event {
  const action = MOCK_ACTIONS[tick % MOCK_ACTIONS.length]!;
  return {
    id: 9_000 + tick,
    turnId: null,
    ts: new Date().toISOString(),
    type: 'tool_call',
    toolName: action.toolName,
    parentEventId: null,
    status: null,
    durationMs: null,
    payload: action.payload,
  };
}

export interface MockSocket {
  close: () => void;
}

export function createMockSocket(onMessage: (msg: WSMessage) => void): MockSocket {
  let tick = 0;

  // Approvals demo (phase 2): store transitions fan out through this socket;
  // one new pending request arrives shortly after load (badge appears live).
  setMockPermissionEmitter(onMessage);
  const inject = setTimeout(injectMockPermissionRequest, INJECT_DELAY_MS);

  const interval = setInterval(() => {
    tick += 1;
    sweepMockExpiry();
    const active = mockSessions.find((s) => s.status === 'active');
    if (!active) return;

    if (tick % 3 === 0) {
      // Occasionally flip the second active session between idle and active.
      const other = mockSessions.find((s) => s.id !== active.id && s.status !== 'completed');
      if (other) {
        const flipped: Session = {
          ...other,
          status: other.status === 'idle' ? 'active' : other.status,
        };
        onMessage({ type: 'session_updated', payload: flipped });
        return;
      }
    }
    onMessage({ type: 'session_updated', payload: { ...active } });
    // step-10 contract shape: event_appended carries {sessionId, event}.
    onMessage({
      type: 'event_appended',
      payload: { sessionId: active.id, event: mockEvent(tick) },
    });
  }, 5000);

  return {
    close: () => {
      clearTimeout(inject);
      clearInterval(interval);
      setMockPermissionEmitter(null);
    },
  };
}
