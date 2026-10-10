// Typed client for the triage runs (Go DTOs in internal/triage/types.go, handlers
// in internal/api/triage*.go). An agent walks the Inbox backlog: it applies a
// decision itself (`applied`), leaves a suggestion for the operator
// (`suggested`), or keeps an audit sample (`sample`). MOCK mode serves fixtures.
//
// A daemon without the triage service answers 503: the read calls degrade to
// "nothing" so the Inbox still renders; the mutating calls throw.

import { MOCK } from '../api';

export type TriageVerdictState =
  | 'applied'
  | 'suggested'
  | 'sample'
  | 'accepted'
  | 'audited'
  | 'stale'
  | 'undone'
  | 'undoing'
  | 'rejected'
  | 'failed'
  | 'skipped';

export type TriageVerdictKind = 'classifier' | 'advisor' | 'lesson' | 'retire' | 'friction' | 'agent';

export interface TriageRun {
  id: number;
  trigger: string;
  scopeProjectId: number | null;
  kinds: string[];
  /** `running` | `ok` | `failed`. */
  status: string;
  total: number;
  done: number;
  applied: number;
  suggested: number;
  skipped: number;
  failed: number;
  rejected: number;
  costUsd: number;
  sessionUuids: string[];
  error: string;
  startedAt: string;
  finishedAt: string | null;
}

export interface TriageVerdict {
  id: number;
  runId: number;
  /** Kept a string: the server may add kinds (`friction`, `agent`) before the UI knows them. */
  kind: TriageVerdictKind | (string & {});
  class: string;
  /** The source row id as text (classifier: a decision id). */
  ref: string;
  /** Groups parts of one item (classifier: the session uuid). */
  itemKey: string;
  title: string;
  value: string;
  reason: string;
  /** For an advisor `fix-card`: `{ title, prompt, cardId? }`. */
  payload: Record<string, unknown> | null;
  prior: unknown;
  state: TriageVerdictState;
  createdAt: string;
  decidedAt: string | null;
  projectId: number | null;
}

export interface AcceptAllResult {
  accepted: number[];
  stale: number[];
  failed: { id: number; error: string }[];
  remaining: number;
}

export interface TriageAudit {
  answered: number;
  agree: number;
  byQuestion: { questionId: string; answered: number; agree: number }[];
}

/** A run is already active (POST /api/triage/runs → 409). The active run's id is `activeRunId`. */
export class TriageBusyError extends Error {
  readonly activeRunId: number | null;
  constructor(activeRunId: number | null) {
    // i18n-ignore: never shown — callers match the `busy` prefix and hide this error
    super('busy: a triage run is already active');
    this.name = 'TriageBusyError';
    this.activeRunId = activeRunId;
  }
}

/** A verdict could not be accepted (409): `state` is where it stands now (`stale`, `accepted`, …). */
export class TriageConflictError extends Error {
  readonly state: string;
  constructor(message: string, state: string) {
    super(message);
    this.name = 'TriageConflictError';
    this.state = state;
  }
}

const NO_AUDIT: TriageAudit = { answered: 0, agree: 0, byQuestion: [] };

const MOCK_RUN: TriageRun = {
  id: 7,
  trigger: 'operator',
  scopeProjectId: null,
  kinds: ['classifier', 'advisor', 'lesson', 'retire'],
  status: 'running',
  total: 40,
  done: 12,
  applied: 7,
  suggested: 3,
  skipped: 1,
  failed: 0,
  rejected: 1,
  costUsd: 0.42,
  sessionUuids: [],
  error: '',
  startedAt: '2026-10-05T09:00:00Z',
  finishedAt: null,
};

function mockVerdict(v: Partial<TriageVerdict> & Pick<TriageVerdict, 'id' | 'kind' | 'ref' | 'state'>): TriageVerdict {
  return {
    runId: 7,
    class: '',
    itemKey: '',
    title: '',
    value: '',
    reason: '',
    payload: null,
    prior: null,
    createdAt: '2026-10-05T09:01:00Z',
    decidedAt: null,
    projectId: null,
    ...v,
  };
}

const MOCK_VERDICTS: TriageVerdict[] = [
  mockVerdict({
    id: 101,
    kind: 'lesson',
    ref: '5',
    state: 'suggested',
    title: 'New lesson',
    value: 'accept',
    reason: 'Seen in three phase runs and phrased as a rule.',
  }),
  mockVerdict({
    id: 102,
    kind: 'advisor',
    ref: '11',
    state: 'suggested',
    title: 'Advisor recommendation',
    value: 'fix-card',
    reason: 'The same fix would help every run.',
    payload: { title: 'Pin the effort level', prompt: 'Pin --effort in the runner and add a test.' },
  }),
  mockVerdict({
    id: 103,
    kind: 'classifier',
    ref: '3',
    state: 'sample',
    itemKey: 'mock-session-1',
    value: 'refactor',
    reason: 'The session only moved code between files.',
  }),
  mockVerdict({
    id: 104,
    kind: 'lesson',
    ref: '6',
    state: 'applied',
    value: 'dismiss',
    reason: 'Duplicate of an existing lesson.',
    decidedAt: '2026-10-05T09:02:00Z',
  }),
  mockVerdict({
    id: 105,
    kind: 'advisor',
    ref: '12',
    state: 'applied',
    value: 'not-useful',
    reason: 'The rule fires on a pattern that is fine here.',
    decidedAt: '2026-10-05T09:03:00Z',
  }),
];

async function errorBody(res: Response): Promise<Record<string, unknown>> {
  try {
    const body: unknown = await res.json();
    return typeof body === 'object' && body !== null ? (body as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function failure(what: string, res: Response, body: Record<string, unknown>): Error {
  const detail = typeof body.error === 'string' ? ` ${body.error}` : '';
  return new Error(`${what}: ${String(res.status)}${detail}`);
}

async function readJson<T>(res: Response, what: string): Promise<T> {
  if (!res.ok) throw failure(what, res, await errorBody(res));
  return (await res.json()) as T;
}

function post(path: string, body?: unknown): Promise<Response> {
  return fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
}

/** GET /api/triage/runs/active — the running run, or null (also null on an older daemon: 503). */
export async function fetchActiveTriageRun(): Promise<TriageRun | null> {
  if (MOCK) return MOCK_RUN;
  const path = '/api/triage/runs/active';
  const res = await fetch(path);
  if (res.status === 503) return null;
  return readJson<TriageRun | null>(res, `GET ${path}`);
}

/** GET /api/triage/runs/{id}. */
export async function fetchTriageRun(id: number): Promise<TriageRun> {
  if (MOCK) return { ...MOCK_RUN, id, status: 'ok', done: MOCK_RUN.total, finishedAt: '2026-10-05T09:10:00Z' };
  const path = `/api/triage/runs/${String(id)}`;
  return readJson<TriageRun>(await fetch(path), `GET ${path}`);
}

export interface StartTriageOptions {
  /** Kinds the run visits; the server's registry order applies. Omitted = every registered kind. */
  kinds?: readonly string[];
  /** Most items the run takes, across its kinds; omitted = the server's DefaultCap (50); the server clamps to MaxCap (1000). */
  cap?: number;
}

/**
 * POST /api/triage/runs — start a run over the Inbox backlog (null = every project).
 * A field is sent only when given; `cap` only when it is a positive integer.
 * A 409 throws a TriageBusyError (message starts with `busy`); its `activeRunId` is the run in flight.
 */
export async function startTriageRun(project: string | null, opts: StartTriageOptions = {}): Promise<{ id: number }> {
  if (MOCK) return { id: MOCK_RUN.id };
  const path = '/api/triage/runs';
  const { kinds, cap } = opts;
  const res = await post(path, {
    ...(project === null ? {} : { project }),
    ...(kinds === undefined ? {} : { kinds }),
    ...(cap !== undefined && Number.isInteger(cap) && cap > 0 ? { cap } : {}),
  });
  if (res.status === 409) {
    const body = await errorBody(res);
    throw new TriageBusyError(typeof body.activeRunId === 'number' ? body.activeRunId : null);
  }
  return readJson<{ id: number }>(res, `POST ${path}`);
}

/** GET /api/triage/verdicts — newest first; `[]` on an older daemon (503). */
export async function fetchTriageVerdicts(
  states: TriageVerdictState[],
  project: string | null,
  limit?: number,
): Promise<TriageVerdict[]> {
  if (MOCK) return MOCK_VERDICTS.filter((v) => states.includes(v.state));
  const qs = new URLSearchParams({ state: states.join(',') });
  if (project !== null) qs.set('project', project);
  if (limit !== undefined) qs.set('limit', String(limit));
  const path = `/api/triage/verdicts?${qs.toString()}`;
  const res = await fetch(path);
  if (res.status === 503) return [];
  return (await readJson<{ items: TriageVerdict[] | null }>(res, `GET ${path}`)).items ?? [];
}

/** POST /api/triage/verdicts/{id}/accept — a 409 throws a TriageConflictError (server text + `state`). */
export async function acceptTriageVerdict(id: number): Promise<TriageVerdict> {
  if (MOCK) return { ...(MOCK_VERDICTS.find((v) => v.id === id) ?? MOCK_VERDICTS[0]), state: 'accepted' } as TriageVerdict;
  const path = `/api/triage/verdicts/${String(id)}/accept`;
  const res = await post(path);
  if (res.status === 409) {
    const body = await errorBody(res);
    throw new TriageConflictError(
      typeof body.error === 'string' ? body.error : 'conflict',
      typeof body.state === 'string' ? body.state : '',
    );
  }
  return readJson<TriageVerdict>(res, `POST ${path}`);
}

/** POST /api/triage/verdicts/accept-all — every open suggestion in scope. */
export async function acceptAllTriageVerdicts(project: string | null): Promise<AcceptAllResult> {
  if (MOCK) return { accepted: [101, 102], stale: [], failed: [], remaining: 0 };
  const path = '/api/triage/verdicts/accept-all';
  return readJson<AcceptAllResult>(await post(path, project === null ? {} : { project }), `POST ${path}`);
}

/** POST /api/triage/verdicts/{id}/undo — 409 when the verdict is not undoable. */
export async function undoTriageVerdict(id: number): Promise<TriageVerdict> {
  if (MOCK) return { ...(MOCK_VERDICTS.find((v) => v.id === id) ?? MOCK_VERDICTS[0]), state: 'undone' } as TriageVerdict;
  const path = `/api/triage/verdicts/${String(id)}/undo`;
  return readJson<TriageVerdict>(await post(path), `POST ${path}`);
}

/** GET /api/triage/audit — how often the agent's audit-sample labels matched yours; zeros on 503. */
export async function fetchTriageAudit(): Promise<TriageAudit> {
  if (MOCK) return { answered: 10, agree: 9, byQuestion: [{ questionId: 'd2.task_type', answered: 10, agree: 9 }] };
  const path = '/api/triage/audit';
  const res = await fetch(path);
  if (res.status === 503) return NO_AUDIT;
  return readJson<TriageAudit>(res, `GET ${path}`);
}
