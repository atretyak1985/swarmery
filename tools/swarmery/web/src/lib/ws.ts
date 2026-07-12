// Live updates over /api/ws with reconnect + exponential backoff (1s → 30s
// cap). On every successful REconnect the caller's `onReconnect` fires so list
// state can be refetched (messages may have been missed while offline).
// In mock mode a fake socket emits fixture messages instead.

import { useEffect, useRef } from 'react';
import type { Session, WSMessage } from '../api/types';
import { MOCK } from '../api';
import { createMockSocket } from '../mock/ws';

const BACKOFF_BASE_MS = 1_000;
const BACKOFF_CAP_MS = 30_000;

function wsUrl(): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/ws`;
}

export function useLiveUpdates(
  onMessage: (msg: WSMessage) => void,
  onReconnect: () => void,
): void {
  const messageRef = useRef(onMessage);
  const reconnectRef = useRef(onReconnect);
  messageRef.current = onMessage;
  reconnectRef.current = onReconnect;

  useEffect(() => {
    if (MOCK) {
      const socket = createMockSocket((msg) => messageRef.current(msg));
      return () => socket.close();
    }

    let ws: WebSocket | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let attempt = 0;
    let everConnected = false;
    let disposed = false;

    const connect = (): void => {
      if (disposed) return;
      ws = new WebSocket(wsUrl());

      ws.onopen = () => {
        attempt = 0;
        if (everConnected) reconnectRef.current();
        everConnected = true;
      };

      ws.onmessage = (ev: MessageEvent) => {
        try {
          messageRef.current(JSON.parse(String(ev.data)) as WSMessage);
        } catch {
          // Malformed frame — ignore; contract violations surface in evals.
        }
      };

      ws.onclose = () => {
        if (disposed) return;
        const delayMs = Math.min(BACKOFF_BASE_MS * 2 ** attempt, BACKOFF_CAP_MS);
        attempt += 1;
        timer = setTimeout(connect, delayMs);
      };

      ws.onerror = () => {
        ws?.close();
      };
    };

    connect();
    return () => {
      disposed = true;
      if (timer !== null) clearTimeout(timer);
      ws?.close();
    };
  }, []);
}

/** Upsert a WS session payload into a list (newest sessions first). */
export function applySessionMessage(sessions: Session[], msg: WSMessage): Session[] {
  if (msg.type === 'event_appended') return sessions;
  const incoming = msg.payload;
  const idx = sessions.findIndex((s) => s.id === incoming.id);
  if (idx === -1) return [incoming, ...sessions];
  return sessions.map((s) => (s.id === incoming.id ? incoming : s));
}
