// The dashboard session: the token the daemon asks for on every browser write
// and on every WebSocket upgrade.
//
// How the page gets one: `swarmery open` (or "Open dashboard" in the notch
// menu) opens the dashboard as `<base>/<path>#swarmery-unlock=<ticket>`, a
// short-lived ticket signed with the link token. On boot the page reads that
// fragment, strips it from the address bar at once (a reload or a copied URL
// never carries it), and trades it for a session token at
// POST /api/session/unlock. The token lives in localStorage, with an
// in-memory copy for a browser whose storage throws.
//
// This module is also the ONE place the app calls `fetch`: `apiFetch` adds the
// session header to every write and reports a `dashboard_locked` answer to
// whoever subscribed with `onDashboardLocked` (the global banner). A source
// scan test (dashboardSession.test.ts) fails on a `fetch` anywhere else.

export const SESSION_HEADER = 'X-Swarmery-Session';
export const SESSION_STORAGE_KEY = 'swarmery.session';
export const UNLOCK_FRAGMENT_KEY = 'swarmery-unlock';
export const UNLOCK_PATH = '/api/session/unlock';
/** The base WebSocket subprotocol every dashboard socket offers. */
export const WS_PROTOCOL = 'swarmery.v1';
const WS_SESSION_PREFIX = 'swarmery.session.';
const LOCKED_CODE = 'dashboard_locked';

/** The token when storage is unavailable, and a mirror of it otherwise. */
let memoryToken: string | null = null;

/** The session token, or null when this browser has not been unlocked. */
export function getSession(): string | null {
  try {
    const stored = window.localStorage.getItem(SESSION_STORAGE_KEY);
    if (stored !== null && stored !== '') return stored;
  } catch {
    // Storage blocked (private window, site data off): fall back to memory.
  }
  return memoryToken;
}

function setSession(token: string | null): void {
  memoryToken = token;
  try {
    if (token === null) window.localStorage.removeItem(SESSION_STORAGE_KEY);
    else window.localStorage.setItem(SESSION_STORAGE_KEY, token);
  } catch {
    // Storage blocked: the in-memory copy serves this tab.
  }
}

type LockedListener = () => void;
const lockedListeners = new Set<LockedListener>();
/** Whether the locked notice is up: raised by a refusal, lowered by a dismiss.
 * Module state, so the notice survives a switch between the two shells. */
let lockedNotice = false;

/** Subscribe to changes of the locked notice (useSyncExternalStore shape).
 * Returns the unsubscribe function. */
export function onDashboardLocked(listener: LockedListener): () => void {
  lockedListeners.add(listener);
  return () => {
    lockedListeners.delete(listener);
  };
}

/** True while the daemon's last refusal has not been dismissed. */
export function isLockedNoticeShown(): boolean {
  return lockedNotice;
}

/** Hide the locked notice until the next refusal. */
export function dismissLockedNotice(): void {
  lockedNotice = false;
  notifyLocked();
}

function notifyLocked(): void {
  for (const listener of lockedListeners) listener();
}

function emitLocked(): void {
  lockedNotice = true;
  notifyLocked();
}

/** The ticket in a location hash (`#swarmery-unlock=<ticket>`), or null. */
export function readUnlockTicket(hash: string): string | null {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash;
  if (!raw.includes(`${UNLOCK_FRAGMENT_KEY}=`)) return null;
  const ticket = new URLSearchParams(raw).get(UNLOCK_FRAGMENT_KEY);
  return ticket !== null && ticket !== '' ? ticket : null;
}

/**
 * Consume an unlock fragment, if the page was opened with one. The fragment is
 * stripped synchronously, before the first await — call this before the router
 * reads the location. Resolves once the exchange is over; never rejects (a
 * failed unlock leaves the page locked and the banner explains on the next
 * refused write).
 */
export function consumeUnlockFragment(): Promise<void> {
  const ticket = readUnlockTicket(window.location.hash);
  if (ticket === null) return Promise.resolve();
  const { pathname, search } = window.location;
  window.history.replaceState(window.history.state, '', `${pathname}${search}`);
  return exchangeTicket(ticket);
}

/** The first render waits on the exchange; a daemon that never answers must
 * not hold the page blank. */
const UNLOCK_TIMEOUT_MS = 5_000;

async function exchangeTicket(ticket: string): Promise<void> {
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), UNLOCK_TIMEOUT_MS);
  try {
    const res = await fetch(UNLOCK_PATH, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ticket }),
      signal: abort.signal,
    });
    if (!res.ok) return;
    const body = (await res.json()) as { session?: unknown };
    if (typeof body.session === 'string' && body.session !== '') setSession(body.session);
  } catch {
    // Daemon unreachable, timed out, or a malformed answer: stay locked.
  } finally {
    clearTimeout(timer);
  }
}

/** The subprotocols a dashboard WebSocket offers: the session rides as the
 * second one (a browser cannot set headers on an upgrade). */
export function wsProtocols(): string[] {
  const token = getSession();
  return token === null ? [WS_PROTOCOL] : [WS_PROTOCOL, `${WS_SESSION_PREFIX}${token}`];
}

/**
 * A socket that closed before it ever opened, with no session to offer, was
 * refused for want of one: say so through the locked banner. A socket that had
 * a session and still failed is more likely a daemon restart; the next write
 * tells the two apart.
 */
export function reportSocketRefused(): void {
  if (getSession() === null) emitLocked();
}

function methodOf(input: RequestInfo | URL, init: RequestInit | undefined): string {
  if (init?.method !== undefined) return init.method.toUpperCase();
  if (typeof Request !== 'undefined' && input instanceof Request) return input.method.toUpperCase();
  return 'GET';
}

function withSessionHeader(headers: HeadersInit | undefined, token: string): HeadersInit {
  if (headers === undefined) return { [SESSION_HEADER]: token };
  if (headers instanceof Headers) {
    const copy = new Headers(headers);
    copy.set(SESSION_HEADER, token);
    return copy;
  }
  if (Array.isArray(headers)) return [...headers, [SESSION_HEADER, token]];
  return { ...headers, [SESSION_HEADER]: token };
}

async function noteIfLocked(res: Response): Promise<void> {
  try {
    const body = (await res.json()) as { code?: unknown };
    if (body.code !== LOCKED_CODE) return;
  } catch {
    return;
  }
  // The daemon refused the token we sent (or there was none): drop it, so the
  // sockets stop offering a dead session.
  setSession(null);
  emitLocked();
}

/**
 * `fetch` for the dashboard API. Reads pass through untouched; a write
 * (anything but GET/HEAD) carries the session header when the page has one,
 * and a 401 `dashboard_locked` answer raises the locked banner. The caller
 * still receives the response and handles its error as before.
 */
export function apiFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const method = methodOf(input, init);
  if (method === 'GET' || method === 'HEAD') {
    return init === undefined ? fetch(input) : fetch(input, init);
  }
  const token = getSession();
  const base = init?.headers ?? (input instanceof Request ? input.headers : undefined);
  const sent = token === null ? init : { ...init, headers: withSessionHeader(base, token) };
  return fetch(input, sent).then((res) => {
    if (res.status === 401 && typeof res.clone === 'function') void noteIfLocked(res.clone());
    return res;
  });
}

/** Test-only: forget the in-memory token and lower the locked notice. */
export function resetDashboardSessionForTests(): void {
  memoryToken = null;
  dismissLockedNotice();
}
