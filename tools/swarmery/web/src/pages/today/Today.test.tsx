// @vitest-environment jsdom
//
// Today (handoff §3). The claims:
//
//   1. With no data the loop still renders five stages, each teaching what
//      will appear (no "0 · n/a"), and the old deck stays one click down under
//      "Today in detail".
//   2. A blocked session turns Run the accent and a lesson candidate turns Learn;
//      Waiting on you shows at most four rows and "Needs you · N →"; Live now
//      lists the running session; the feed links a row to its entity.
//   3. /p/:slug renders the same page in that scope, and the week's spend reads
//      from the overview series.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/today`.

import { cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
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
  fetchStatsOverview: vi.fn(),
  getNeedsYou: vi.fn(),
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
  AUTO_MODE_NO_VERDICT_RULE: 'auto_mode_no_verdict',
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

function blocker(id: number): Record<string, unknown> {
  return {
    kind: 'approval',
    sessionId: 40 + id,
    sessionUuid: `uuid-${String(id)}`,
    sessionName: `Blocked session ${String(id)}`,
    projectSlug: 'orders-api',
    requestId: id,
    toolName: 'Bash',
    preview: `npm ci #${String(id)}`,
    question: '',
    asksQuestion: false,
    blockingSince: iso(-42),
    blockingSeconds: 42,
    termFocusUrl: null,
    suggestion: null,
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
  vi.mocked(api.getNeedsYou).mockResolvedValue({
    items: Array.from({ length: opts.approvals }, (_, i) => blocker(i + 1)),
    generatedAt: iso(0),
  } as never);
  vi.mocked(api.fetchStatsOverview).mockResolvedValue({
    series: [1, 2, 3, 4, 5, 6, 7, 8].map((n) => ({
      day: `2026-10-${String(20 + n)}`,
      sessions: 1,
      tokens: 1,
      cost_usd: n,
      errors: 0,
    })),
  } as never);
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
  it('renders five teaching stages and keeps the old deck one click down when there is no data', async () => {
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
    await waitFor(() => {
      expect(screen.getByText(/Nothing waits on you · 0 running/)).toBeTruthy();
    });
    expect(screen.queryByTestId('today-detail')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /Today in detail/ }));
    expect(within(screen.getByTestId('today-detail')).getByText('fleet command deck')).toBeTruthy();
  });

  it('turns Run and Learn to the accent, caps waiting rows at four and lists live sessions', async () => {
    mockData({ approvals: 5, lessons: 2, sessions: [RUNNING_SESSION] });
    renderAt('/');
    await waitFor(() => {
      expect(screen.getByTestId('stage-run').getAttribute('data-waiting')).toBe('true');
    });
    await waitFor(() => {
      expect(screen.getByTestId('stage-learn').getAttribute('data-waiting')).toBe('true');
    });
    expect(screen.getByTestId('stage-plan').getAttribute('data-waiting')).toBe('false');
    expect(screen.getByText('5 sessions ask for you.').className).toContain('text-brand');
    expect(screen.getByText(/5 things wait on you · 1 running/)).toBeTruthy();

    const waiting = screen.getByTestId('waiting-on-you');
    expect(within(waiting).getAllByTestId('waiting-row')).toHaveLength(4);
    expect(within(waiting).getByText('Needs you · 5 →').getAttribute('href')).toBe('/needs-you');
    expect(within(waiting).getAllByTestId('waiting-row')[0]?.getAttribute('href')).toBe('/sessions/41');

    const live = screen.getByTestId('live-now');
    await waitFor(() => {
      expect(within(live).getAllByTestId('live-row')).toHaveLength(1);
    });
    expect(within(live).getByText('Migrate email templates to provider v2')).toBeTruthy();
    expect(within(live).getByText('orders-api · working · $0.84')).toBeTruthy();
    expect(within(screen.getByTestId('stage-run')).getByText('1')).toBeTruthy();

    const row = screen.getByRole('link', { name: /Migrate email templates to provider v2 session started/ });
    expect(row.getAttribute('href')).toBe('/sessions/7');
  });

  it('approves a plain permission in place', async () => {
    mockData({ approvals: 1, lessons: 0, sessions: [] });
    renderAt('/');
    const button = await screen.findByRole('button', { name: 'approve' });
    fireEvent.click(button);
    await waitFor(() => {
      expect(api.resolveApproval).toHaveBeenCalledWith(1, 'approve');
    });
  });

  it('renders on the project index in that scope, with the week of spend', async () => {
    mockData({ approvals: 1, lessons: 0, sessions: [] });
    renderAt('/p/orders-api');
    expect(screen.getByTestId('loop-map')).toBeTruthy();
    expect(screen.getByTestId('waiting-on-you')).toBeTruthy();
    expect(screen.getByTestId('live-now')).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByText('Needs you · 1 →')).toBeTruthy();
    });
    expect(screen.getByText('Needs you · 1 →').getAttribute('href')).toBe('/p/orders-api/needs-you');
    expect(api.getNeedsYou).toHaveBeenCalledWith('orders-api');
    expect(api.fetchProjectRecommendations).toHaveBeenCalledWith('orders-api', 'verified,accepted,adopted');
    await waitFor(() => {
      expect(within(screen.getByTestId('spend-week')).getByText('$35')).toBeTruthy();
    });
    fireEvent.click(screen.getByRole('button', { name: /Today in detail/ }));
    expect(within(screen.getByTestId('today-detail')).getByText('project overview deck')).toBeTruthy();
  });
});
