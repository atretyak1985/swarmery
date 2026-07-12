// Live updates over /api/ws — ONE shared connection for the whole app.
// The app shell (nav badge) and the active page subscribe to the same
// socket via useLiveUpdates; the socket opens with the first subscriber and
// closes with the last. Reconnect uses exponential backoff (1s → 30s cap);
// on every successful REconnect every subscriber's `onReconnect` fires so
// list state can be refetched (messages may have been missed while offline).
// In mock mode a single fake socket emits fixture messages instead.

import { useEffect, useRef, type MutableRefObject } from 'react';
import type { PermissionRequest, Session, WSMessage } from '../api/types';
import { MOCK } from '../api';
import { createMockSocket, type MockSocket } from '../mock/ws';

const BACKOFF_BASE_MS = 1_000;
const BACKOFF_CAP_MS = 30_000;
const RECONCILE_MS = 60_000;

function wsUrl(): string {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/ws`;
}

interface Subscriber {
  message: MutableRefObject<(msg: WSMessage) => void>;
  reconnect: MutableRefObject<() => void>;
}

const subscribers = new Set<Subscriber>();
let socket: WebSocket | null = null;
let mockSocket: MockSocket | null = null;
let retryTimer: ReturnType<typeof setTimeout> | null = null;
let reconcileTimer: ReturnType<typeof setInterval> | null = null;
let attempt = 0;
let everConnected = false;

function broadcast(msg: WSMessage): void {
  for (const sub of subscribers) sub.message.current(msg);
}

function connect(): void {
  if (subscribers.size === 0) return;
  const ws = new WebSocket(wsUrl());
  socket = ws;

  ws.onopen = () => {
    attempt = 0;
    if (everConnected) for (const sub of subscribers) sub.reconnect.current();
    everConnected = true;
  };

  ws.onmessage = (ev: MessageEvent) => {
    try {
      broadcast(JSON.parse(String(ev.data)) as WSMessage);
    } catch {
      // Malformed frame — ignore; contract violations surface in evals.
    }
  };

  ws.onclose = () => {
    if (socket !== ws) return; // superseded or torn down
    socket = null;
    if (subscribers.size === 0) return;
    const delayMs = Math.min(BACKOFF_BASE_MS * 2 ** attempt, BACKOFF_CAP_MS);
    attempt += 1;
    retryTimer = setTimeout(() => {
      retryTimer = null;
      connect();
    }, delayMs);
  };

  ws.onerror = () => {
    ws.close();
  };
}

function ensureLive(): void {
  if (MOCK) {
    mockSocket ??= createMockSocket(broadcast);
  } else if (socket === null && retryTimer === null) {
    connect();
  }
  // Convergence net: even if a live-update frame is lost (bus overflow, tab
  // throttling), a periodic refetch guarantees the UI reaches the API truth
  // within a minute.
  reconcileTimer ??= setInterval(() => {
    for (const sub of subscribers) sub.reconnect.current();
  }, RECONCILE_MS);
}

function teardownIfIdle(): void {
  if (subscribers.size > 0) return;
  if (retryTimer !== null) {
    clearTimeout(retryTimer);
    retryTimer = null;
  }
  if (reconcileTimer !== null) {
    clearInterval(reconcileTimer);
    reconcileTimer = null;
  }
  mockSocket?.close();
  mockSocket = null;
  const ws = socket;
  socket = null; // clear first so onclose sees the teardown
  ws?.close();
  attempt = 0;
  everConnected = false;
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
    const sub: Subscriber = { message: messageRef, reconnect: reconnectRef };
    subscribers.add(sub);
    ensureLive();
    return () => {
      subscribers.delete(sub);
      teardownIfIdle();
    };
  }, []);
}

/** Upsert a WS session payload into a list (newest sessions first). */
export function applySessionMessage(sessions: Session[], msg: WSMessage): Session[] {
  // Only the session messages carry a Session payload (phase-2 permission_*
  // messages carry a PermissionRequest — see applyPermissionMessage).
  if (msg.type !== 'session_started' && msg.type !== 'session_updated') return sessions;
  const incoming = msg.payload;
  // Defensive: a malformed frame (no id / no startedAt) must never become a
  // ghost "(unknown)" row — the DB guarantees both fields on real sessions.
  if (!incoming.id || !incoming.startedAt) return sessions;
  const idx = sessions.findIndex((s) => s.id === incoming.id);
  if (idx === -1) return [incoming, ...sessions];
  return sessions.map((s) => (s.id === incoming.id ? incoming : s));
}

/**
 * Upsert a WS permission payload into a request list (phase 2 — approvals).
 * Idempotent by `id`: the same permission_resolved arrives for the client's
 * own optimistic approve/deny, so an update must overwrite, never duplicate.
 */
export function applyPermissionMessage(
  requests: PermissionRequest[],
  msg: WSMessage,
): PermissionRequest[] {
  if (msg.type !== 'permission_requested' && msg.type !== 'permission_resolved') return requests;
  const incoming = msg.payload;
  if (!incoming.id) return requests; // defensive: malformed frame
  const idx = requests.findIndex((r) => r.id === incoming.id);
  if (idx === -1) return [incoming, ...requests];
  return requests.map((r) => (r.id === incoming.id ? incoming : r));
}
