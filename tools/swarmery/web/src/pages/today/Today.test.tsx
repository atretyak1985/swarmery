// @vitest-environment jsdom
//
// Today (Canvas v3 phase 4). The claims:
//
//   1. With no data the loop still renders five stages, each teaching what
//      will appear (no "0 · n/a"), and the old deck renders under
//      "Today in detail".
//   2. A pending approval turns Run amber and a lesson candidate turns Learn
//      amber; Waiting on you shows at most three rows and "Inbox · N →";
//      Live now lists the running session.
//   3. /p/:slug renders the same page with the project deck.
//
// Dev-only suite. Run with
//   npx vitest run src/pages/today
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import * as lessons from '../../api/lessons';
import { Today } from './Today';

const iso = (offsetSec: number): string => new Date(Date.now() + offsetSec * 1000).toISOString();

vi.mock('../../api', () => ({
  fetchApprovals: vi.fn(),
  fetchEpics: vi.fn(),
  fetchSessions: vi.fn(),
  fetchRecommendations: vi.fn(),
  fetchProjectRecommendations: vi.fn(),
  fetchProposals: vi.fn(),
  resolveApproval: vi.fn(async () => ({})),
}));

vi.mock('../../api/decisions', () => ({
  fetchDecisions: vi.fn(async () => ({ configured: true, local: true, claude: false, questions: [] })),
  fetchLabelQueue: vi.fn(async () => []),
}));

vi.mock('../../api/lessons', () => ({
  fetchLessons: vi.fn(),
  fetchRetirements: vi.fn(async () => []),
}));

// The Inbox's seventh source (useInboxItems); no alert is open in these tests.
vi.mock('../../api/alerts', () => ({
  ACCOUNT_BREAKER_RULE: 'account_breaker_open',
  fetchAlerts: vi.fn(async () => []),
  resumeAccount: vi.fn(async () => undefined),
}));

vi.mock('../../lib/ws', () => ({
  useLiveUpdates: vi.fn(),
  applySessionMessage: (s: unknown) => s,
}));

function approval(id: number): Record<string, unknown> {
  return {
    id,
    sessionId: 40 + id,
    toolName: 'Bash',
    requestJson: JSON.stringify({ tool_name: 'Bash', tool_input: { command: `npm ci #${String(id)}` } }),
    status: 'pending',
    requestedAt: iso(-42),
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt: iso(78),
  };
}

function lesson(id: number): Record<string, unknown> {
  return {
    id,
    phaseName: 'Phase 3',
    title: `Lesson ${String(id)}`,
    guidance: '',
    areaGlobs: [],
    cause: '',
    sourceParagraph: '',
    surpriseIndex: 0.62,
    recurrences: 1,
    createdAt: iso(-7200),
  };
}

const RUNNING_SESSION = {
  id: 7,
  projectId: 1,
  projectSlug: 'orders-api',
  projectName: 'orders-api',
  sessionUuid: 'a1f9c22b-0000',
  model: 'claude-opus-5-5',
  modelLast: null,
  modelChanged: false,
  modelFellBack: false,
  gitBranch: null,
  cwd: null,
  status: 'active',
  startedAt: iso(-18 * 60),
  endedAt: iso(-5),
  title: 'Migrate email templates to provider v2',
  source: 'local',
  costUsd: 0.84,
};

function mockData(opts: { approvals: number; lessons: number; sessions: unknown[] }): void {
  vi.mocked(api.fetchApprovals).mockResolvedValue(
    Array.from({ length: opts.approvals }, (_, i) => approval(i + 1)) as never,
  );
  vi.mocked(lessons.fetchLessons).mockResolvedValue(
    Array.from({ length: opts.lessons }, (_, i) => lesson(i + 1)) as never,
  );
  vi.mocked(api.fetchEpics).mockResolvedValue([]);
  vi.mocked(api.fetchSessions).mockResolvedValue({ sessions: opts.sessions, nextCursor: null } as never);
  vi.mocked(api.fetchRecommendations).mockResolvedValue({ recommendations: [] });
  vi.mocked(api.fetchProjectRecommendations).mockResolvedValue({ recommendations: [] });
  vi.mocked(api.fetchProposals).mockResolvedValue({ proposals: [] } as never);
}

function renderAt(path: string): void {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/" element={<Today detail={<div>fleet command deck</div>} />} />
        <Route path="/p/:slug" element={<Today detail={<div>project overview deck</div>} />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  cleanup();
});

describe('Today', () => {
  it('renders five teaching stages and the old deck when there is no data', async () => {
    mockData({ approvals: 0, lessons: 0, sessions: [] });
    renderAt('/');
    const map = screen.getByTestId('loop-map');
    for (const id of ['plan', 'run', 'measure', 'learn', 'change']) {
      expect(within(map).getByTestId(`stage-${id}`)).toBeTruthy();
    }
    await waitFor(() => {
      expect(screen.getByText('Appears after the first finished phase run.')).toBeTruthy();
    });
    expect(screen.getByText(/Appears after the first plan is written/)).toBeTruthy();
    expect(screen.queryByText(/n\/a/)).toBeNull();
    for (const id of ['plan', 'run', 'measure', 'learn', 'change']) {
      expect(screen.getByTestId(`stage-${id}`).getAttribute('data-waiting')).toBe('false');
    }
    expect(screen.getByText('Today in detail')).toBeTruthy();
    expect(within(screen.getByTestId('today-detail')).getByText('fleet command deck')).toBeTruthy();
  });

  it('turns Run and Learn amber, caps waiting rows at three and lists live sessions', async () => {
    mockData({ approvals: 2, lessons: 2, sessions: [RUNNING_SESSION] });
    renderAt('/');
    await waitFor(() => {
      expect(screen.getByTestId('stage-run').getAttribute('data-waiting')).toBe('true');
    });
    expect(screen.getByTestId('stage-learn').getAttribute('data-waiting')).toBe('true');
    expect(screen.getByTestId('stage-plan').getAttribute('data-waiting')).toBe('false');
    expect(screen.getByText('2 approvals waiting.').className).toContain('text-amber');

    const waiting = screen.getByTestId('waiting-on-you');
    expect(within(waiting).getAllByTestId('waiting-row')).toHaveLength(3);
    expect(within(waiting).getByText('Inbox · 4 →')).toBeTruthy();
    expect(within(waiting).getAllByText('approve')).toHaveLength(2);

    const live = screen.getByTestId('live-now');
    await waitFor(() => {
      expect(within(live).getAllByTestId('live-row')).toHaveLength(1);
    });
    expect(within(live).getByText('Migrate email templates to provider v2')).toBeTruthy();
    expect(within(live).getByText('orders-api · working · $0.84')).toBeTruthy();
    expect(within(screen.getByTestId('stage-run')).getByText('1')).toBeTruthy();
  });

  it('renders on the project index with the project deck', async () => {
    mockData({ approvals: 1, lessons: 0, sessions: [] });
    renderAt('/p/orders-api');
    expect(screen.getByTestId('loop-map')).toBeTruthy();
    expect(screen.getByTestId('waiting-on-you')).toBeTruthy();
    expect(screen.getByTestId('live-now')).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByText('Inbox · 1 →')).toBeTruthy();
    });
    expect(screen.getByText('Inbox · 1 →').getAttribute('href')).toBe('/p/orders-api/inbox');
    expect(api.fetchApprovals).toHaveBeenCalledWith('pending', 'orders-api');
    expect(api.fetchProjectRecommendations).toHaveBeenCalledWith('orders-api', 'verified,accepted,adopted');
    expect(within(screen.getByTestId('today-detail')).getByText('project overview deck')).toBeTruthy();
  });
});
