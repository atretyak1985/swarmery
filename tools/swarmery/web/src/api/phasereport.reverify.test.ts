// reverifyPhase (phase-reverify plan, phase 2): POST …/phases/{id}/verify. The
// endpoint answers 202 {"status":"verifying"} or a coded refusal
// {"error": msg, "code": code}; the client resolves on the first and rejects
// with the server's own message on the second, so the page can show it verbatim.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { reverifyPhase } from './phasereport';

const fetchMock = vi.fn<(input: string, init?: RequestInit) => Promise<Response>>();

function reply(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('reverifyPhase', () => {
  it('POSTs …/verify with no body and resolves on 202', async () => {
    fetchMock.mockResolvedValueOnce(reply(202, { status: 'verifying', phaseId: 11 }));
    await expect(reverifyPhase(77, 11)).resolves.toMatchObject({ status: 'verifying' });
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe('/api/epics/77/phases/11/verify');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBeUndefined();
  });

  it('rejects a coded 409 refusal with the server message', async () => {
    fetchMock.mockResolvedValueOnce(
      reply(409, { error: 'a run or a verification already holds this phase', code: 'already-running' }),
    );
    await expect(reverifyPhase(77, 11)).rejects.toThrow('a run or a verification already holds this phase');
  });

  it('rejects a 422 refusal with the server message', async () => {
    fetchMock.mockResolvedValueOnce(reply(422, { error: 'this phase has open criteria', code: 'criteria-open' }));
    await expect(reverifyPhase(77, 11)).rejects.toThrow('this phase has open criteria');
  });

  it('falls back to "verify again failed: <status>" when the body has no error', async () => {
    fetchMock.mockResolvedValueOnce(new Response('upstream down', { status: 503 }));
    await expect(reverifyPhase(77, 11)).rejects.toThrow('verify again failed: 503');
  });
});
