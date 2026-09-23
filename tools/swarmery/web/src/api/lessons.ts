// Typed client for the lesson review queue (learning-loop phase 14, Go DTOs in
// internal/api/lessons.go + internal/lessons/review.go). A candidate becomes
// active ONLY through acceptLesson — the operator's action. MOCK mode serves a
// fixture queue so the page renders offline.

import { MOCK } from '../api';

export type LessonStatus = 'candidate' | 'active' | 'retired' | 'dismissed' | 'merged';

export interface LessonMatch {
  kind: 'retro' | 'surprise';
  /** Set for kind "surprise" (another surprise lesson). */
  lessonId?: number;
  normTitle: string;
  title: string;
  count: number;
  exact: boolean;
}

export interface Lesson {
  id: number;
  phaseId: number;
  phaseName: string;
  planId: string;
  sourcePhaseRun: string;
  title: string;
  normTitle: string;
  guidance: string;
  areaGlobs: string[];
  evidence: string[];
  cause: string;
  sourceParagraph: string;
  surpriseIndex: number | null;
  status: LessonStatus;
  linkedNormTitle: string;
  mergedIntoId: number | null;
  recurrences: number;
  recurrenceRuns: string[];
  model: string;
  createdAt: string;
  updatedAt: string;
  activatedAt: string | null;
  retiredAt: string | null;
  retireReason: string | null;
  matches: LessonMatch[];
}

export interface LessonEdit {
  title?: string;
  guidance?: string;
  areaGlobs?: string[];
}

export interface MergeTarget {
  lessonId?: number;
  normTitle?: string;
}

const MOCK_LESSONS: Lesson[] = [
  {
    id: 3,
    phaseId: 41,
    phaseName: 'Phase 2 — cache-aware costing',
    planId: '2026-09-20-cost-accuracy',
    sourcePhaseRun: '6f1c9a2e-0000-4000-8000-000000000003',
    title: 'Cache usage lives under usage.cache_creation',
    normTitle: 'cache usage lives under usage cache creation',
    guidance:
      'In internal/ingest, read cache usage from usage.cache_creation; the flat total is legacy.',
    areaGlobs: ['tools/swarmery/internal/ingest/**'],
    evidence: ['divergence:41', 'file:tools/swarmery/internal/ingest/record.go'],
    cause: 'The forecast assumed the flat cache total was current; the parser needed a second path.',
    sourceParagraph:
      'The cache fields moved under usage.cache_creation, so record.go needed a second parser and the cost package was never touched.',
    surpriseIndex: 0.74,
    status: 'candidate',
    linkedNormTitle: 'cache usage lives under usage cache creation',
    mergedIntoId: null,
    recurrences: 1,
    recurrenceRuns: ['6f1c9a2e-0000-4000-8000-000000000003'],
    model: 'claude-sonnet-5',
    createdAt: '2026-09-23T10:00:00Z',
    updatedAt: '2026-09-23T10:00:00Z',
    activatedAt: null,
    retiredAt: null,
    retireReason: null,
    matches: [
      {
        kind: 'retro',
        normTitle: 'cache usage lives under usage cache creation',
        title: 'Cache usage lives under usage.cache_creation',
        count: 2,
        exact: true,
      },
    ],
  },
  {
    id: 2,
    phaseId: 38,
    phaseName: 'Phase 1 — migration',
    planId: '2026-09-18-retention',
    sourcePhaseRun: '6f1c9a2e-0000-4000-8000-000000000002',
    title: 'Index every FK child column before a prune',
    normTitle: 'index every fk child column before prune',
    guidance:
      'In internal/store/migrations, index every foreign-key child column before a retention prune deletes parents.',
    areaGlobs: ['tools/swarmery/internal/store/**'],
    evidence: ['divergence:38', 'commit:abc1234'],
    cause: 'The prune ran with foreign_keys=ON and scanned an unindexed child table per row.',
    sourceParagraph: 'The prune hung because the FK child column had no index.',
    surpriseIndex: 0.81,
    status: 'active',
    linkedNormTitle: '',
    mergedIntoId: null,
    recurrences: 2,
    recurrenceRuns: [
      '6f1c9a2e-0000-4000-8000-000000000002',
      '6f1c9a2e-0000-4000-8000-000000000004',
    ],
    model: 'claude-sonnet-5',
    createdAt: '2026-09-20T09:00:00Z',
    updatedAt: '2026-09-21T09:00:00Z',
    activatedAt: '2026-09-21T09:00:00Z',
    retiredAt: null,
    retireReason: null,
    matches: [],
  },
];

async function jsonOrThrow<T>(res: Response, what: string): Promise<T> {
  if (!res.ok) {
    let msg = String(res.status);
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) msg = `${msg} ${body.error}`;
    } catch {
      // non-JSON error body: keep the status alone
    }
    throw new Error(`${what}: ${msg}`);
  }
  return (await res.json()) as T;
}

/** GET /api/lessons — the queue, optionally filtered by status. */
export async function fetchLessons(status?: LessonStatus): Promise<Lesson[]> {
  if (MOCK) return MOCK_LESSONS.filter((l) => status === undefined || l.status === status);
  const q = status === undefined ? '' : `?status=${encodeURIComponent(status)}`;
  const body = await jsonOrThrow<{ lessons: Lesson[] }>(
    await fetch(`/api/lessons${q}`),
    'GET /api/lessons',
  );
  return body.lessons;
}

function mockUpdate(id: number, patch: Partial<Lesson>): Lesson {
  const cur = MOCK_LESSONS.find((l) => l.id === id);
  if (cur === undefined) throw new Error(`no such lesson ${String(id)}`);
  const next = { ...cur, ...patch };
  MOCK_LESSONS.splice(MOCK_LESSONS.indexOf(cur), 1, next);
  return next;
}

async function send(method: string, path: string, body?: unknown): Promise<Lesson> {
  return jsonOrThrow<Lesson>(
    await fetch(path, {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: body === undefined ? null : JSON.stringify(body),
    }),
    `${method} ${path}`,
  );
}

/** POST /api/lessons/{id}/accept — candidate → active (the only path to active). */
export async function acceptLesson(id: number): Promise<Lesson> {
  if (MOCK) return mockUpdate(id, { status: 'active', activatedAt: new Date().toISOString() });
  return send('POST', `/api/lessons/${String(id)}/accept`);
}

/** PATCH /api/lessons/{id} — edit the words or areas; never the status. */
export async function editLesson(id: number, edit: LessonEdit): Promise<Lesson> {
  if (MOCK) return mockUpdate(id, edit);
  return send('PATCH', `/api/lessons/${String(id)}`, edit);
}

/** POST /api/lessons/{id}/merge — fold a candidate into an existing lesson. */
export async function mergeLesson(id: number, target: MergeTarget): Promise<Lesson> {
  if (MOCK) {
    return mockUpdate(id, {
      status: 'merged',
      mergedIntoId: target.lessonId ?? null,
      linkedNormTitle: target.normTitle ?? '',
    });
  }
  return send('POST', `/api/lessons/${String(id)}/merge`, target);
}

/** POST /api/lessons/{id}/dismiss — drop a candidate (kept for calibration). */
export async function dismissLesson(id: number, reason: string): Promise<Lesson> {
  if (MOCK) return mockUpdate(id, { status: 'dismissed', retireReason: reason });
  return send('POST', `/api/lessons/${String(id)}/dismiss`, { reason });
}

/** POST /api/lessons/{id}/retire — take an active lesson out of circulation. */
export async function retireLesson(id: number, reason: string): Promise<Lesson> {
  if (MOCK) return mockUpdate(id, { status: 'retired', retireReason: reason });
  return send('POST', `/api/lessons/${String(id)}/retire`, { reason });
}
