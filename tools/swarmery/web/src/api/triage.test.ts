import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  TriageBusyError,
  TriageConflictError,
  acceptAllTriageVerdicts,
  acceptTriageVerdict,
  fetchActiveTriageRun,
  fetchTriageAudit,
  fetchTriageRun,
  fetchTriageVerdicts,
  startTriageRun,
  undoTriageVerdict,
} from './triage';

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

describe('triage api', () => {
  it('fetchActiveTriageRun: GET, passes null through', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, null));
    await expect(fetchActiveTriageRun()).resolves.toBeNull();
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/triage/runs/active');
  });

  it('fetchTriageRun: GET by id', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { id: 4, status: 'ok' }));
    await expect(fetchTriageRun(4)).resolves.toMatchObject({ id: 4 });
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/triage/runs/4');
  });

  it('startTriageRun: POSTs the project slug, or an empty body for the fleet', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(reply(202, { id: 9 })));
    await expect(startTriageRun('web')).resolves.toEqual({ id: 9 });
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe('/api/triage/runs');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe(JSON.stringify({ project: 'web' }));
    await startTriageRun(null);
    expect(fetchMock.mock.calls[1]?.[1]?.body).toBe('{}');
  });

  it('startTriageRun: a 409 throws busy with the active run id', async () => {
    fetchMock.mockResolvedValueOnce(reply(409, { error: 'triage: a run is already active', activeRunId: 12 }));
    const err = await startTriageRun(null).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TriageBusyError);
    expect((err as TriageBusyError).message.startsWith('busy')).toBe(true);
    expect((err as TriageBusyError).activeRunId).toBe(12);
  });

  it('fetchTriageVerdicts: builds the query and unwraps items', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { items: [{ id: 1 }] }));
    await expect(fetchTriageVerdicts(['suggested', 'sample'], 'web', 50)).resolves.toEqual([{ id: 1 }]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/triage/verdicts?state=suggested%2Csample&project=web&limit=50');
  });

  it('fetchTriageVerdicts: no project, no limit; null items become []', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { items: null }));
    await expect(fetchTriageVerdicts(['applied'], null)).resolves.toEqual([]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/triage/verdicts?state=applied');
  });

  it('acceptTriageVerdict: POST; a 409 carries the server text and state', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { id: 3, state: 'accepted' }));
    await expect(acceptTriageVerdict(3)).resolves.toMatchObject({ state: 'accepted' });
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe('/api/triage/verdicts/3/accept');
    expect(init?.method).toBe('POST');

    fetchMock.mockResolvedValueOnce(reply(409, { error: 'item changed', state: 'stale' }));
    const err = await acceptTriageVerdict(3).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TriageConflictError);
    expect((err as TriageConflictError).message).toBe('item changed');
    expect((err as TriageConflictError).state).toBe('stale');
  });

  it('acceptTriageVerdict: a 422 throws with the status', async () => {
    fetchMock.mockResolvedValueOnce(reply(422, { error: 'bad payload' }));
    await expect(acceptTriageVerdict(3)).rejects.toThrow(/422 bad payload/);
  });

  it('acceptAllTriageVerdicts: POSTs the scope and returns the result', async () => {
    const result = { accepted: [1], stale: [2], failed: [{ id: 3, error: 'x' }], remaining: 1 };
    fetchMock.mockResolvedValueOnce(reply(200, result));
    await expect(acceptAllTriageVerdicts('web')).resolves.toEqual(result);
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe('/api/triage/verdicts/accept-all');
    expect(init?.body).toBe(JSON.stringify({ project: 'web' }));
  });

  it('undoTriageVerdict: POST; a 409 throws', async () => {
    fetchMock.mockResolvedValueOnce(reply(200, { id: 5, state: 'undone' }));
    await expect(undoTriageVerdict(5)).resolves.toMatchObject({ state: 'undone' });
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/triage/verdicts/5/undo');
    fetchMock.mockResolvedValueOnce(reply(409, { error: 'verdict is not undoable (state stale)' }));
    await expect(undoTriageVerdict(5)).rejects.toThrow(/409/);
  });

  it('fetchTriageAudit: GET', async () => {
    const audit = { answered: 4, agree: 3, byQuestion: [{ questionId: 'q', answered: 4, agree: 3 }] };
    fetchMock.mockResolvedValueOnce(reply(200, audit));
    await expect(fetchTriageAudit()).resolves.toEqual(audit);
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/triage/audit');
  });

  describe('503: the daemon has no triage service', () => {
    beforeEach(() => {
      fetchMock.mockImplementation(() => Promise.resolve(reply(503, { error: 'triage service not attached' })));
    });

    it('reads degrade to nothing', async () => {
      await expect(fetchActiveTriageRun()).resolves.toBeNull();
      await expect(fetchTriageVerdicts(['suggested'], null)).resolves.toEqual([]);
      await expect(fetchTriageAudit()).resolves.toEqual({ answered: 0, agree: 0, byQuestion: [] });
    });

    it('mutations throw', async () => {
      await expect(startTriageRun(null)).rejects.toThrow(/503/);
      await expect(acceptTriageVerdict(1)).rejects.toThrow(/503/);
      await expect(acceptAllTriageVerdicts(null)).rejects.toThrow(/503/);
      await expect(undoTriageVerdict(1)).rejects.toThrow(/503/);
    });
  });
});
