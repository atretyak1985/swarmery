// Typed client for the decision classifier (learning-loop phase 9, Go DTOs in
// internal/api/decisions.go + internal/decide). Advisory data: per-question
// stats, the mode switch, and a session's D2 labels. MOCK mode serves fixture
// data so the view renders offline.

import { MOCK } from '../api';

export type DecideMode = 'off' | 'shadow' | 'active';

export interface QuestionStats {
  questionId: string;
  mode: DecideMode;
  threshold: number;
  calls: number;
  errors: number;
  acted: number;
  withTruth: number;
  agreed: number;
  agreement: number | null;
  /** 10 confidence buckets: [0,0.1) … [0.9,1]. */
  histogram: number[];
}

export interface DecisionsResponse {
  configured: boolean;
  local: boolean;
  claude: boolean;
  questions: QuestionStats[];
}

export interface SessionLabel {
  sessionUuid: string;
  taskType: string;
  outcome: string;
  failureCause: string;
  backend: string;
  labeledAt: string;
}

const MOCK_DECISIONS: DecisionsResponse = {
  configured: true,
  local: true,
  claude: false,
  questions: [
    {
      questionId: 'd1.run_end',
      mode: 'shadow',
      threshold: 0.85,
      calls: 37,
      errors: 1,
      acted: 0,
      withTruth: 22,
      agreed: 19,
      agreement: 19 / 22,
      histogram: [0, 0, 0, 1, 2, 3, 4, 6, 8, 12],
    },
    {
      questionId: 'd2.task_type',
      mode: 'shadow',
      threshold: 0.6,
      calls: 120,
      errors: 0,
      acted: 0,
      withTruth: 0,
      agreed: 0,
      agreement: null,
      histogram: [0, 1, 2, 6, 9, 14, 18, 25, 24, 21],
    },
    {
      questionId: 'd2.outcome',
      mode: 'shadow',
      threshold: 0.6,
      calls: 120,
      errors: 0,
      acted: 0,
      withTruth: 14,
      agreed: 12,
      agreement: 12 / 14,
      histogram: [0, 0, 1, 3, 5, 10, 14, 22, 30, 35],
    },
    {
      questionId: 'd2.failure_cause',
      mode: 'off',
      threshold: 0.6,
      calls: 0,
      errors: 0,
      acted: 0,
      withTruth: 0,
      agreed: 0,
      agreement: null,
      histogram: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0],
    },
  ],
};

async function jsonOrThrow<T>(res: Response, what: string): Promise<T> {
  if (!res.ok) {
    throw new Error(`${what}: ${String(res.status)}`);
  }
  return (await res.json()) as T;
}

/** GET /api/decisions — per-question stats. */
export async function fetchDecisions(): Promise<DecisionsResponse> {
  if (MOCK) return MOCK_DECISIONS;
  return jsonOrThrow<DecisionsResponse>(await fetch('/api/decisions'), 'GET /api/decisions');
}

/** PUT /api/decisions/{question}/mode — the dashboard's mode switch. */
export async function putDecisionMode(
  questionId: string,
  mode: DecideMode,
): Promise<DecisionsResponse> {
  if (MOCK) {
    return {
      ...MOCK_DECISIONS,
      questions: MOCK_DECISIONS.questions.map((q) =>
        q.questionId === questionId ? { ...q, mode } : q,
      ),
    };
  }
  const path = `/api/decisions/${encodeURIComponent(questionId)}/mode`;
  return jsonOrThrow<DecisionsResponse>(
    await fetch(path, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode }),
    }),
    `PUT ${path}`,
  );
}

/** GET /api/decisions/labels/{uuid} — a session's D2 labels, or null. */
export async function fetchSessionLabels(uuid: string): Promise<SessionLabel | null> {
  if (MOCK) {
    return {
      sessionUuid: uuid,
      taskType: 'bugfix',
      outcome: 'shipped',
      failureCause: 'none',
      backend: 'local',
      labeledAt: '2026-09-23T12:00:00Z',
    };
  }
  const path = `/api/decisions/labels/${encodeURIComponent(uuid)}`;
  const body = await jsonOrThrow<{ labels: SessionLabel | null }>(await fetch(path), `GET ${path}`);
  return body.labels;
}
