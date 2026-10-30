// @vitest-environment jsdom
//
// The Sessions place: three groups of ListRows beside the open session, one
// component across /sessions and /sessions/:id, a uuid in the URL working like a
// row id (the notch opens those), the state said once, and the actions by state.

import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { act } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { NeedsYouItem, Session, SessionDetail, SessionStatus } from '../api/types';
import * as api from '../api';
import { useLocation } from 'react-router-dom';
import { cleanup, fireEvent, render, screen, waitFor, within } from '../test/render';
import { parseTab } from './detail/SessionPane';
import { Sessions } from './Sessions';
import { SessionDetailPage } from './SessionDetail';

vi.mock('../api', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  fetchSessions: vi.fn(),
  fetchSession: vi.fn(),
  fetchApprovals: vi.fn(async () => []),
  fetchSessionLabels: vi.fn(async () => null),
  fetchSwarmClaims: vi.fn(async () => ({ claims: [], conflicts: [] })),
  getNeedsYou: vi.fn(),
  resolveApproval: vi.fn(async () => undefined),
  stopSession: vi.fn(async () => undefined),
}));
vi.mock('../lib/ws', () => ({
  useLiveUpdates: () => undefined,
  applySessionMessage: (s: unknown) => s,
}));
vi.mock('../lib/scope', () => ({
  useScope: () => ({ scope: null, scopeName: null, scopeProject: null, projects: [], setScope: () => undefined }),
}));
vi.mock('../components/FileContentionBanner', () => ({ FileContentionBanner: () => null }));

const NOW = Date.now();
const ago = (minutes: number): string => new Date(NOW - minutes * 60_000).toISOString();

function makeSession(id: number, status: SessionStatus, over: Partial<Session> = {}): Session {
  return {
    id,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'orders-api',
    sessionUuid: `uuid-${String(id)}`,
    model: 'claude-opus-5-5',
    modelLast: 'claude-opus-5-5',
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'feat/x',
    cwd: '/w',
    status,
    startedAt: ago(30),
    endedAt: ago(1),
    title: `session ${String(id)}`,
    source: 'jsonl',
    ...over,
  };
}

function detailOf(s: Session, over: Partial<SessionDetail> = {}): SessionDetail {
  return { ...s, turns: [], events: [], fileChanges: [], recovered: 0, ...over };
}

const NEEDS = makeSession(1, 'active');
const WORKING = makeSession(2, 'active');
const DONE = makeSession(3, 'completed', { endedAt: ago(5) });
const FAILED = makeSession(4, 'killed', { endedAt: ago(9) });
const OLD = makeSession(5, 'completed', { startedAt: ago(4000), endedAt: ago(3990) });
const ALL = [NEEDS, WORKING, DONE, FAILED, OLD];

const GATE: NeedsYouItem = {
  kind: 'approval',
  sessionId: 1,
  sessionUuid: 'uuid-1',
  sessionName: 'session 1',
  projectSlug: 'orders-api',
  requestId: 11,
  toolName: 'Bash',
  preview: 'rm -rf build',
  question: '',
  asksQuestion: false,
  blockingSince: ago(12),
  blockingSeconds: 720,
  termFocusUrl: null,
  suggestion: null,
};

function Where(): JSX.Element {
  const { pathname, search } = useLocation();
  return <output aria-label="where">{`${pathname}${search}`}</output>;
}

function open(path: string): ReturnType<typeof createMemoryRouter> {
  const router = createMemoryRouter(
    [
      {
        path: '/sessions',
        element: (
          <>
            <Sessions />
            <Where />
          </>
        ),
      },
      {
        path: '/sessions/:id',
        element: (
          <>
            <SessionDetailPage />
            <Where />
          </>
        ),
      },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

const paneTitle = (name: string): Promise<HTMLElement> => screen.findByRole('button', { name });

beforeEach(() => {
  vi.clearAllMocks();
  Element.prototype.scrollIntoView = () => undefined; // jsdom has no layout
  vi.mocked(api.fetchSessions).mockResolvedValue({ sessions: ALL, nextCursor: null });
  vi.mocked(api.getNeedsYou).mockResolvedValue({ items: [GATE], generatedAt: ago(0) });
  vi.mocked(api.fetchSession).mockImplementation(async (id) => {
    const found = ALL.find((s) => String(s.id) === String(id) || s.sessionUuid === String(id));
    if (found === undefined) throw new Error('not found');
    return detailOf(found);
  });
});

afterEach(cleanup);

describe('the list', () => {
  it('groups into Needs you, Working and Done today, with history under Earlier', async () => {
    open('/sessions/2');
    const list = await screen.findByRole('region', { name: 'Sessions' });
    await within(list).findByText('Needs you');
    expect(within(list).getByText('Working')).toBeTruthy();
    expect(within(list).getByText('Done today')).toBeTruthy();
    expect(within(list).getByText('Earlier')).toBeTruthy();
  });

  it('treats a queued gate as needs-you even while the status says active', async () => {
    open('/sessions/2');
    await screen.findByText('Allow Bash: rm -rf build?');
    const link = screen.getByText('Allow Bash: rm -rf build?').closest('a');
    expect(link?.getAttribute('href')).toBe('/sessions/1');
  });

  it('filters by s: and n: tokens in the field', async () => {
    open('/sessions/2');
    await screen.findByText('session 3');
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 's:failed' } });
    await waitFor(() => expect(screen.queryByRole('link', { name: /session 3/ })).toBeNull());
    expect(screen.getByRole('link', { name: /session 4/ })).toBeTruthy();
  });

  it('keeps the list mounted across routes: selecting does not refetch it', async () => {
    const router = open('/sessions');
    await screen.findByRole('link', { name: /session 3/ });
    expect(api.fetchSessions).toHaveBeenCalledTimes(1);
    await act(async () => {
      await router.navigate('/sessions/3');
    });
    await paneTitle('session 3');
    expect(api.fetchSessions).toHaveBeenCalledTimes(1);
  });

  it('opens the first row with no id in the URL, and leaves the URL alone', async () => {
    open('/sessions');
    await paneTitle('session 1');
    expect(screen.getByRole('status', { name: 'where' }).textContent).toBe('/sessions');
  });

  it('moves with j and k, and stops at the ends', async () => {
    open('/sessions/1');
    await paneTitle('session 1');
    await screen.findByText('Allow Bash: rm -rf build?');
    fireEvent.keyDown(window, { key: 'j' });
    await waitFor(() => expect(screen.getByRole('status', { name: 'where' }).textContent).toBe('/sessions/2'));
    fireEvent.keyDown(window, { key: 'k' });
    await waitFor(() => expect(screen.getByRole('status', { name: 'where' }).textContent).toBe('/sessions/1'));
    fireEvent.keyDown(window, { key: 'k' });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.getByRole('status', { name: 'where' }).textContent).toBe('/sessions/1');
  });
});

describe('the detail', () => {
  it('opens a session by uuid, which is what the notch links to', async () => {
    open('/sessions/uuid-3');
    await paneTitle('session 3');
    expect(api.fetchSession).toHaveBeenCalledWith('uuid-3');
    expect(screen.getByRole('link', { name: /session 3/ }).getAttribute('aria-current')).toBe('true');
  });

  it('says the state once, above the numbers', async () => {
    open('/sessions/3');
    await paneTitle('session 3');
    expect(screen.getAllByText('Finished')).toHaveLength(1);
    const body = screen.getByTestId('session-detail').textContent ?? '';
    expect(body).not.toContain('status');
    expect(body).not.toContain('completed');
  });

  it('shows the gate card only while the session needs you, and allows through the API', async () => {
    open('/sessions/1');
    const allow = await screen.findByRole('button', { name: /allow once/ });
    fireEvent.click(allow);
    await waitFor(() => expect(api.resolveApproval).toHaveBeenCalledWith(11, 'approve', undefined, undefined));
    cleanup();
    open('/sessions/3');
    await paneTitle('session 3');
    expect(screen.queryByRole('button', { name: /allow once/ })).toBeNull();
  });

  it('asks for the reason before a deny goes out', async () => {
    open('/sessions/1');
    const deny = await screen.findByRole('button', { name: /^deny/ });
    fireEvent.click(deny);
    expect(api.resolveApproval).not.toHaveBeenCalled();
    fireEvent.change(screen.getByRole('textbox', { name: /why you deny/ }), { target: { value: 'out of scope' } });
    fireEvent.click(screen.getByRole('button', { name: /^deny/ }));
    await waitFor(() => expect(api.resolveApproval).toHaveBeenCalledWith(11, 'deny', 'out of scope', undefined));
  });

  it.each([
    ['working', '/sessions/2', ['redirect', 'stop']],
    ['done', '/sessions/3', ['session outcome', 'extract tasks']],
    ['failed', '/sessions/4', ['resume with a reply']],
  ])('offers the %s actions beside the tabs', async (_state, path, names) => {
    open(path);
    await screen.findByRole('tab', { name: /Steps/ });
    for (const name of names) expect(screen.getByRole('button', { name })).toBeTruthy();
  });

  // Plan D-13: stopSession ends the process, so nothing calls it a pause.
  it('offers no pause on a working session', async () => {
    open('/sessions/2');
    await screen.findByRole('button', { name: 'redirect' });
    expect(screen.queryByRole('button', { name: /pause/i })).toBeNull();
  });

  it('keeps Kill, the outcome, extract tasks and the remote controls in ⋯', async () => {
    vi.mocked(api.fetchSession).mockResolvedValue(detailOf({ ...FAILED, procPid: 42, procState: 'dead' }));
    open('/sessions/4');
    fireEvent.click(await screen.findByRole('button', { name: 'more for this session' }));
    const menu = screen.getByRole('group', { name: 'more for this session' });
    expect(within(menu).getByRole('group', { name: 'session outcome' })).toBeTruthy();
    expect(within(menu).getByRole('button', { name: 'extract tasks' })).toBeTruthy();
    expect(within(menu).getByText('exited')).toBeTruthy();
    expect(within(menu).getByRole('button', { name: /Stop turn/ })).toBeTruthy();
  });

  it('moves the old tab names onto the new tabs', () => {
    expect(parseTab(null)).toBe('steps');
    expect(parseTab('timeline')).toBe('steps');
    expect(parseTab('swarm')).toBe('steps');
    expect(parseTab('diffs')).toBe('diff');
    expect(parseTab('diff')).toBe('diff');
    expect(parseTab('chat')).toBe('chat');
  });

  it('opens the swarm fold on the old ?tab=swarm link', async () => {
    open('/sessions/3?tab=swarm');
    await paneTitle('session 3');
    expect(screen.getByRole('button', { name: 'swarm' }).getAttribute('aria-expanded')).toBe('true');
  });
});
