// Fake /api/ws feed for VITE_MOCK=1 — periodically emits contract-shaped
// WSMessages so live UI paths (upserts, appends) are exercised offline.

import type { Session, WSMessage } from '../api/types';
import { mockSessions } from './data';

export interface MockSocket {
  close: () => void;
}

export function createMockSocket(onMessage: (msg: WSMessage) => void): MockSocket {
  let tick = 0;

  const interval = setInterval(() => {
    tick += 1;
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
  }, 5000);

  return {
    close: () => clearInterval(interval),
  };
}
