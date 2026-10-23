// @vitest-environment jsdom
//
// The dashboard session (lib/dashboardSession.ts): the unlock fragment is
// traded for a session and stripped from the address bar; every write carries
// the session header and no read does; a page whose storage throws still
// works; a 401 dashboard_locked raises the locked notice; sockets offer the
// session as a subprotocol.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  SESSION_HEADER,
  SESSION_STORAGE_KEY,
  apiFetch,
  consumeUnlockFragment,
  getSession,
  isLockedNoticeShown,
  readUnlockTicket,
  reportSocketRefused,
  resetDashboardSessionForTests,
  wsProtocols,
} from './dashboardSession';

const TICKET = '1761200000.n0nce-test-1.6d3d147593d214d11e3b5c8b501651cf6cf188c36cbf8c11523d3c8e42c53f89';

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

/** The headers of the n-th fetch call, as a Headers object. */
function sentHeaders(fetchMock: ReturnType<typeof vi.fn>, n = 0): Headers {
  const init = fetchMock.mock.calls[n]?.[1] as RequestInit | undefined;
  return new Headers(init?.headers);
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  window.localStorage.clear();
  resetDashboardSessionForTests();
  window.history.replaceState(null, '', '/');
  fetchMock = vi.fn(async () => json(200, {}));
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('unlock fragment', () => {
  it('reads the ticket only from a swarmery-unlock fragment', () => {
    expect(readUnlockTicket(`#swarmery-unlock=${TICKET}`)).toBe(TICKET);
    expect(readUnlockTicket('#heading-id')).toBeNull();
    expect(readUnlockTicket('')).toBeNull();
    expect(readUnlockTicket('#swarmery-unlock=')).toBeNull();
  });

  it('is exchanged for a session, stored, and stripped keeping path and query', async () => {
    window.history.replaceState(null, '', `/sessions/42?tab=chat#swarmery-unlock=${TICKET}`);
    fetchMock.mockResolvedValueOnce(json(200, { session: 'sess-1' }));

    const done = consumeUnlockFragment();
    // Stripped synchronously, before the exchange resolves.
    expect(window.location.hash).toBe('');
    expect(window.location.pathname).toBe('/sessions/42');
    expect(window.location.search).toBe('?tab=chat');
    await done;

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/session/unlock');
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toEqual({ ticket: TICKET });
    expect(getSession()).toBe('sess-1');
    expect(window.localStorage.getItem(SESSION_STORAGE_KEY)).toBe('sess-1');
  });

  it('a refused ticket leaves the page locked and never rejects', async () => {
    window.history.replaceState(null, '', `/#swarmery-unlock=${TICKET}`);
    fetchMock.mockResolvedValueOnce(json(401, { error: 'bad ticket', code: 'unlock_invalid' }));
    await expect(consumeUnlockFragment()).resolves.toBeUndefined();
    expect(getSession()).toBeNull();
    expect(window.location.hash).toBe('');
  });

  it('does nothing without a fragment', async () => {
    window.history.replaceState(null, '', '/docs#anchor');
    await consumeUnlockFragment();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(window.location.hash).toBe('#anchor');
  });
});

describe('apiFetch', () => {
  it('adds the session header to every write and to no read', async () => {
    window.localStorage.setItem(SESSION_STORAGE_KEY, 'sess-2');
    const writes = ['POST', 'PUT', 'PATCH', 'DELETE', 'post'];
    for (const method of writes) await apiFetch('/api/x', { method });
    for (let i = 0; i < writes.length; i++) expect(sentHeaders(fetchMock, i).get(SESSION_HEADER)).toBe('sess-2');

    fetchMock.mockClear();
    await apiFetch('/api/x');
    await apiFetch('/api/x', { method: 'GET' });
    await apiFetch('/api/x', { method: 'HEAD' });
    for (let i = 0; i < 3; i++) expect(sentHeaders(fetchMock, i).get(SESSION_HEADER)).toBeNull();
    // A plain read passes through with exactly the caller's arguments.
    expect(fetchMock.mock.calls[0]).toEqual(['/api/x']);
  });

  it('keeps the caller headers next to the session header', async () => {
    window.localStorage.setItem(SESSION_STORAGE_KEY, 'sess-3');
    await apiFetch('/api/x', { method: 'POST', headers: { 'Content-Type': 'application/json' } });
    await apiFetch('/api/x', { method: 'PUT', headers: new Headers({ 'X-Other': '1' }) });
    await apiFetch('/api/x', { method: 'PATCH', headers: [['X-Other', '2']] });
    expect(sentHeaders(fetchMock, 0).get('Content-Type')).toBe('application/json');
    expect(sentHeaders(fetchMock, 1).get('X-Other')).toBe('1');
    expect(sentHeaders(fetchMock, 2).get('X-Other')).toBe('2');
    for (let i = 0; i < 3; i++) expect(sentHeaders(fetchMock, i).get(SESSION_HEADER)).toBe('sess-3');
  });

  it('without a session, a write goes out with the caller init untouched', async () => {
    const init = { method: 'POST', body: '{}' };
    await apiFetch('/api/x', init);
    expect(fetchMock.mock.calls[0]).toEqual(['/api/x', init]);
  });

  it('a 401 dashboard_locked raises the notice and drops the dead session', async () => {
    window.localStorage.setItem(SESSION_STORAGE_KEY, 'stale');
    fetchMock.mockResolvedValueOnce(json(401, { error: 'locked', code: 'dashboard_locked' }));
    const res = await apiFetch('/api/x', { method: 'POST' });
    // The caller still owns the body.
    expect(((await res.json()) as { code: string }).code).toBe('dashboard_locked');
    await vi.waitFor(() => expect(isLockedNoticeShown()).toBe(true));
    expect(getSession()).toBeNull();
  });

  it('another 401 is not a lock', async () => {
    window.localStorage.setItem(SESSION_STORAGE_KEY, 'sess-4');
    fetchMock.mockResolvedValueOnce(json(401, { error: 'link auth required', code: 'link_auth' }));
    await apiFetch('/api/x', { method: 'POST' });
    await new Promise((r) => setTimeout(r, 0));
    expect(isLockedNoticeShown()).toBe(false);
    expect(getSession()).toBe('sess-4');
  });
});

describe('storage that throws', () => {
  it('still unlocks, stores in memory and signs writes', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('SecurityError');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('SecurityError');
    });
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new Error('SecurityError');
    });
    expect(getSession()).toBeNull();

    window.history.replaceState(null, '', `/#swarmery-unlock=${TICKET}`);
    fetchMock.mockResolvedValueOnce(json(200, { session: 'mem-1' }));
    await consumeUnlockFragment();
    expect(getSession()).toBe('mem-1');

    await apiFetch('/api/x', { method: 'DELETE' });
    expect(sentHeaders(fetchMock, 1).get(SESSION_HEADER)).toBe('mem-1');

    fetchMock.mockResolvedValueOnce(json(401, { code: 'dashboard_locked' }));
    await apiFetch('/api/x', { method: 'POST' });
    await vi.waitFor(() => expect(isLockedNoticeShown()).toBe(true));
    expect(getSession()).toBeNull();
  });
});

describe('socket subprotocols', () => {
  it('offers the session as a second subprotocol when there is one', () => {
    expect(wsProtocols()).toEqual(['swarmery.v1']);
    window.localStorage.setItem(SESSION_STORAGE_KEY, 'sess-5');
    expect(wsProtocols()).toEqual(['swarmery.v1', 'swarmery.session.sess-5']);
  });

  it('a refused socket raises the notice only when there was no session to offer', () => {
    window.localStorage.setItem(SESSION_STORAGE_KEY, 'sess-6');
    reportSocketRefused();
    expect(isLockedNoticeShown()).toBe(false);
    window.localStorage.clear();
    reportSocketRefused();
    expect(isLockedNoticeShown()).toBe(true);
  });
});
