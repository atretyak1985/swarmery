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
import { parkedAtFoot, parseTab } from './detail/SessionPane';
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
const live = vi.hoisted(() => ({ handlers: new Set<(msg: unknown) => void>() }));
vi.mock('../lib/ws', () => ({
  useLiveUpdates: (onMessage: (msg: unknown) => void) => {
    live.handlers.add(onMessage);
  },
  applySessionMessage: (s: unknown) => s,
}));
vi.mock('../lib/scope', () => ({
  useScope: () => ({ scope: null, scopeName: null, scopeProject: null, projects: [], setScope: () => undefined }),
}));
vi.mock('../components/FileContentionBanner', () => ({ FileContentionBanner: () => null }));
vi.mock('../api/lessons', () => ({ fetchLessons: vi.fn(async () => []) }));

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
  live.handlers.clear();
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

  it('moves the old tab names onto the new tabs, with Chat the default', () => {
    expect(parseTab(null)).toBe('chat');
    expect(parseTab('chat')).toBe('chat');
    expect(parseTab('nonsense')).toBe('chat');
    expect(parseTab('steps')).toBe('steps');
    expect(parseTab('timeline')).toBe('steps');
    expect(parseTab('swarm')).toBe('steps');
    expect(parseTab('diffs')).toBe('diff');
    expect(parseTab('diff')).toBe('diff');
  });

  it('opens on Chat, first in the tab row, and keeps Chat out of the URL', async () => {
    open('/sessions/3?tab=steps');
    const chat = await screen.findByRole('tab', { name: /Chat/ });
    const tabs = within(screen.getByTestId('session-detail')).getAllByRole('tab');
    expect(tabs.map((tab) => tab.textContent?.replace(/\d+$/, ''))).toEqual(['Chat', 'Steps', 'Diff']);
    expect(screen.getByRole('tab', { name: /Steps/ }).getAttribute('aria-selected')).toBe('true');
    fireEvent.click(chat);
    await waitFor(() => expect(screen.getByRole('status', { name: 'where' }).textContent).toBe('/sessions/3'));
    expect(chat.getAttribute('aria-selected')).toBe('true');
    cleanup();
    open('/sessions/3');
    expect((await screen.findByRole('tab', { name: /Chat/ })).getAttribute('aria-selected')).toBe('true');
  });

  it('puts Steps in the URL, which Chat never needs', async () => {
    open('/sessions/3');
    fireEvent.click(await screen.findByRole('tab', { name: /Steps/ }));
    await waitFor(() => expect(screen.getByRole('status', { name: 'where' }).textContent).toBe('/sessions/3?tab=steps'));
  });

  it('draws one band: the tabs live inside it, and the rail sits under it', async () => {
    open('/sessions/3');
    const tabs = within(await screen.findByTestId('session-detail')).getByRole('tablist');
    const band = tabs.parentElement?.parentElement as HTMLElement;
    expect(band.className).toContain('border-b');
    expect(band.className).toContain('bg-bg');
    expect(tabs.className).not.toContain('border-b');
    expect(band.querySelector('h2')).toBeTruthy();
    const rail = screen.getByRole('complementary', { name: 'session context' });
    expect(band.contains(rail)).toBe(false);
    // The band sits outside the scroller, so it never moves with the conversation.
    const scroller = band.nextElementSibling as HTMLElement;
    expect(scroller.className).toContain('overflow-y-auto');
    expect(scroller.contains(band)).toBe(false);
    expect(scroller.contains(rail)).toBe(true);
  });

  it('parks on a band around the foot, not on everything past it', () => {
    expect(parkedAtFoot(0, 900)).toBe(true);
    expect(parkedAtFoot(47, 900)).toBe(true);
    expect(parkedAtFoot(-47, 900)).toBe(true);
    expect(parkedAtFoot(48, 900)).toBe(false);
    expect(parkedAtFoot(-48, 900)).toBe(false);
    expect(parkedAtFoot(-400, 0)).toBe(true);
  });

  describe('where the conversation opens', () => {
    // jsdom has no layout: the panel's foot sits at 900 and the conversation's at
    // 2000, moving up as the panel scrolls.
    beforeEach(() => {
      vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function rect(this: Element) {
        const scrolled = document.querySelector('[data-testid="session-detail"]')?.lastElementChild?.scrollTop ?? 0;
        return { bottom: this.className.includes('flex-[1_1_520px]') ? 2000 - scrolled : 900 } as DOMRect;
      });
    });
    afterEach(() => {
      vi.restoreAllMocks();
    });

    const panel = (): HTMLElement => screen.getByTestId('session-detail').lastElementChild as HTMLElement;

    it('opens a finished session at the top, with the way down on offer', async () => {
      open('/sessions/3');
      await paneTitle('session 3');
      await waitFor(() => expect(screen.getByRole('button', { name: 'Jump to latest' })).toBeTruthy());
      expect(panel().scrollTop).toBe(0);
    });

    it('opens a working session at the foot of the conversation', async () => {
      open('/sessions/2');
      await paneTitle('session 2');
      await waitFor(() => expect(panel().scrollTop).toBe(1100));
      expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull();
    });

    it('opens a session that needs you at the top, where its gate is', async () => {
      open('/sessions/1');
      await screen.findByRole('button', { name: /allow once/ });
      await paneTitle('session 1');
      expect(panel().scrollTop).toBe(0);
    });

    it('opens at the top on the session status alone, with nothing in the queue', async () => {
      vi.mocked(api.getNeedsYou).mockResolvedValue({ items: [], generatedAt: ago(0) });
      vi.mocked(api.fetchSession).mockResolvedValue(detailOf(makeSession(2, 'waiting_approval')));
      open('/sessions/2');
      await paneTitle('session 2');
      await screen.findByRole('button', { name: 'Jump to latest' });
      expect(panel().scrollTop).toBe(0);
    });

    it('goes up to the gate when the queue answers after the session', async () => {
      let answer: (value: { items: NeedsYouItem[]; generatedAt: string }) => void = () => undefined;
      vi.mocked(api.getNeedsYou).mockReturnValue(new Promise((resolve) => (answer = resolve)));
      open('/sessions/1');
      await paneTitle('session 1');
      await waitFor(() => expect(panel().scrollTop).toBe(1100));
      await act(async () => {
        answer({ items: [GATE], generatedAt: ago(0) });
      });
      await screen.findByRole('button', { name: /allow once/ });
      expect(panel().scrollTop).toBe(0);
    });

    it('leaves a landing on the brief alone', async () => {
      vi.mocked(api.fetchSession).mockResolvedValue(
        detailOf(WORKING, { handoff: { path: '/h.md', createdAt: ago(4), contextTokens: 1000 } }),
      );
      open('/sessions/2#handoff');
      await paneTitle('session 2');
      await new Promise((resolve) => setTimeout(resolve, 20));
      expect(panel().scrollTop).toBe(0);
    });

    it('does not count a reader past the conversation, down in the rail, as parked', async () => {
      open('/sessions/2');
      await paneTitle('session 2');
      await waitFor(() => expect(panel().scrollTop).toBe(1100));
      expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull();
      panel().scrollTop = 1500;
      fireEvent.scroll(panel());
      await waitFor(() => expect(screen.getByRole('button', { name: 'Jump to latest' })).toBeTruthy());
    });

    /** A new event on a session, as the WS brings it. */
    const newEvent = async (id: number): Promise<void> => {
      await act(async () => {
        for (const handler of live.handlers) {
          handler({ type: 'event_appended', payload: { sessionId: 2, event: { id, type: 'tool_call', ts: ago(0) } } });
        }
        await Promise.resolve();
      });
    };

    it('leaves a reader in the rail alone when a working session gets an event', async () => {
      open('/sessions/2');
      await paneTitle('session 2');
      await waitFor(() => expect(panel().scrollTop).toBe(1100));
      panel().scrollTop = 1500;
      fireEvent.scroll(panel());
      await screen.findByRole('button', { name: 'Jump to latest' });
      await newEvent(901);
      expect(panel().scrollTop).toBe(1500);
    });

    it('still follows a reader parked at the foot', async () => {
      open('/sessions/2');
      await paneTitle('session 2');
      await waitFor(() => expect(panel().scrollTop).toBe(1100));
      panel().scrollTop = 1090;
      fireEvent.scroll(panel());
      await newEvent(902);
      expect(panel().scrollTop).toBe(1100);
    });

    it('goes back to the top of a finished session when Chat is reopened', async () => {
      open('/sessions/3?tab=steps');
      fireEvent.click(await screen.findByRole('tab', { name: /Chat/ }));
      await waitFor(() => expect(screen.getByRole('button', { name: 'Jump to latest' })).toBeTruthy());
      expect(panel().scrollTop).toBe(0);
    });
  });

  describe('from wide up', () => {
    beforeEach(() => {
      window.matchMedia = ((): MediaQueryList =>
        ({ matches: true, addEventListener: () => undefined, removeEventListener: () => undefined }) as unknown as MediaQueryList) as typeof window.matchMedia;
    });
    afterEach(() => {
      Reflect.deleteProperty(window, 'matchMedia');
    });

    it('stands the rail beside the whole session, header included', async () => {
      open('/sessions/3');
      const detail = await screen.findByTestId('session-detail');
      const rail = screen.getByRole('complementary', { name: 'session context' });
      expect(detail.contains(rail)).toBe(false);
      expect(rail.previousElementSibling).toBe(detail);
      expect(rail.className).toContain('overflow-y-auto');
      expect(rail.className).toContain('w-[260px]');
    });
  });

  it('keeps the rail in the flow below wide, under the header', async () => {
    open('/sessions/3');
    const detail = await screen.findByTestId('session-detail');
    expect(detail.contains(screen.getByRole('complementary', { name: 'session context' }))).toBe(true);
  });

  it('keeps the composer in the conversation column, beside the rail', async () => {
    open('/sessions/3');
    const composer = await screen.findByRole('textbox', { name: /reply|message/i });
    const rail = screen.getByRole('complementary', { name: 'session context' });
    expect(rail.contains(composer)).toBe(false);
    expect(composer.closest('.sticky')).toBeTruthy();
    expect(composer.closest('[class*="flex-[1_1_520px]"]')?.parentElement).toBe(rail.parentElement);
  });

  it('opens the swarm fold on the old ?tab=swarm link', async () => {
    open('/sessions/3?tab=swarm');
    await paneTitle('session 3');
    expect(screen.getByRole('button', { name: 'swarm' }).getAttribute('aria-expanded')).toBe('true');
  });
});

/* ----- artboard 2a on v4 (plan D-7): details stay in the side pane --------- */

const where = (): string | null => screen.getByRole('status', { name: 'where' }).textContent;

describe('the tabs', () => {
  const tab = (name: RegExp): HTMLElement => screen.getByRole('tab', { name });

  it('splits the list into four groups by row state, and counts sessions', async () => {
    open('/sessions/2');
    await paneTitle('session 2');
    const strip = within(screen.getByRole('region', { name: 'Sessions' })).getByRole('tablist');
    expect(within(strip).getAllByRole('tab').map((el) => el.textContent)).toEqual([
      'all5',
      'needs you1',
      'working1',
      'done2',
      'stopped1',
    ]);
    fireEvent.click(tab(/^stopped/));
    const list = screen.getByRole('region', { name: 'Sessions' });
    await waitFor(() => expect(within(list).queryByRole('link', { name: /session 3/ })).toBeNull());
    expect(within(list).getByRole('link', { name: /session 4/ })).toBeTruthy();
    // Done holds every finished day, not just today's.
    fireEvent.click(tab(/^done/));
    expect(within(list).getByRole('link', { name: /session 3/ })).toBeTruthy();
    expect(within(list).getByRole('link', { name: /session 5/ })).toBeTruthy();
    expect(within(list).queryByRole('link', { name: /session 4/ })).toBeNull();
  });

  it('keeps a plan run whole: one matching row brings the entire card', async () => {
    const group = { taskId: 9, title: 'orders rollout', phaseId: null, phaseName: null };
    const ctl = makeSession(20, 'active', { planGroup: { ...group, role: 'controller', phaseSeq: null } });
    const phase = makeSession(21, 'completed', {
      endedAt: ago(3),
      planGroup: { ...group, role: 'phase', phaseId: 3, phaseSeq: 1, phaseName: 'schema' },
    });
    vi.mocked(api.fetchSessions).mockResolvedValue({ sessions: [...ALL, ctl, phase], nextCursor: null });
    open('/sessions/2');
    await paneTitle('session 2');
    fireEvent.click(tab(/^working/));
    const list = screen.getByRole('region', { name: 'Sessions' });
    fireEvent.click(within(list).getByRole('button', { name: /Plan runs/ }));
    fireEvent.click(within(list).getByRole('button', { name: /plan run orders rollout/ }));
    // The finished phase rides along with its working controller.
    expect(within(list).getByText('schema')).toBeTruthy();
    expect(within(list).getByText('plan controller')).toBeTruthy();
    // No run has a row in Stopped, so that tab shows no card.
    fireEvent.click(tab(/^stopped/));
    expect(within(list).queryByRole('button', { name: /Plan runs/ })).toBeNull();
  });
});

describe('the keys', () => {
  it('e only focuses the gate\'s main action and never resolves it', async () => {
    open('/sessions/1');
    const allow = await screen.findByRole('button', { name: /allow once/ });
    fireEvent.keyDown(window, { key: 'e' });
    expect(document.activeElement).toBe(allow);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(api.resolveApproval).not.toHaveBeenCalled();
  });

  it('e with no gate focuses the composer', async () => {
    open('/sessions/3');
    const composer = await screen.findByRole('textbox', { name: /reply|message/i });
    fireEvent.keyDown(window, { key: 'e' });
    await waitFor(() => expect(document.activeElement).toBe(composer));
  });

  it.each(['o', 'ArrowRight'])('%s opens the selected row and moves into its pane', async (key) => {
    open('/sessions');
    await paneTitle('session 1');
    fireEvent.keyDown(window, { key });
    await waitFor(() => expect(where()).toBe('/sessions/1'));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('open session')));
  });

  it('← goes back to the list and keeps the selection', async () => {
    open('/sessions/2');
    await paneTitle('session 2');
    fireEvent.keyDown(window, { key: 'ArrowLeft' });
    await waitFor(() => expect(where()).toBe('/sessions'));
    const row = screen.getByRole('link', { name: /session 2/ });
    await waitFor(() => expect(row.getAttribute('aria-current')).toBe('true'));
    expect(document.activeElement).toBe(row);
  });

  it('leaves every key to the search field while the operator types', async () => {
    open('/sessions/2');
    await paneTitle('session 2');
    const field = screen.getByRole('searchbox');
    field.focus();
    for (const key of ['j', 'k', 'o', 'e', 'ArrowLeft', 'ArrowRight']) fireEvent.keyDown(field, { key });
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(where()).toBe('/sessions/2');
    expect(document.activeElement).toBe(field);
  });

  it('lists the keys in the strip under the list', async () => {
    open('/sessions/2');
    await paneTitle('session 2');
    const list = screen.getByRole('region', { name: 'Sessions' });
    for (const hint of ['j/k select', 'e focus action', 'o → open', '← list']) {
      expect(within(list).getByText(hint)).toBeTruthy();
    }
  });
});

describe('the pane keeps every datum of the old chips', () => {
  it('shows context, cost, task, handoff, account, model change, cache cold, process, conflict and plan limit', async () => {
    const fat = makeSession(6, 'completed', {
      endedAt: ago(2),
      contextTokens: 420_000,
      taskExternalId: '2026-11-03-line-items',
      taskLinkSource: 'explicit',
      account: 'work',
      model: 'claude-opus-5-5',
      modelLast: 'claude-sonnet-5-5',
      modelChanged: true,
      modelFellBack: true,
      cacheCold: { since: ago(70), kind: 'subscription', footprint: 420_000 },
      handoff: { path: '/h.md', createdAt: ago(4), contextTokens: 420_000 },
      procPid: 42,
      procState: 'dead',
      link: {
        status: null,
        seenAt: null,
        live: false,
        contextPct: null,
        rateLimits: [{ kind: 'five_hour', percentUsed: 91, resetsAt: null }],
        costUsd: null,
      } as unknown as Session['link'],
    });
    vi.mocked(api.fetchSessions).mockResolvedValue({ sessions: [...ALL, fat], nextCursor: null });
    vi.mocked(api.fetchSwarmClaims).mockResolvedValue({
      claims: [],
      conflicts: [
        {
          path: 'src/orders.ts',
          sessions: [
            { sessionId: 6, sessionUuid: 'uuid-6' },
            { sessionId: 2, sessionUuid: 'uuid-2' },
          ],
        },
      ],
    } as unknown as Awaited<ReturnType<typeof api.fetchSwarmClaims>>);
    vi.mocked(api.fetchSession).mockResolvedValue(
      detailOf(fat, {
        turns: [
          {
            id: 1,
            seq: 1,
            role: 'assistant',
            messageId: null,
            model: 'claude-sonnet-5-5',
            startedAt: ago(3),
            endedAt: ago(2),
            tokensIn: 1000,
            tokensOut: 200,
            tokensCacheRead: null,
            tokensCacheWrite: null,
            costUsd: 1.25,
            text: null,
          } as unknown as SessionDetail['turns'][number],
        ],
      }),
    );
    open('/sessions/6');
    const detail = await screen.findByTestId('session-detail');
    await within(detail).findByText('session 6');
    const text = (): string => detail.textContent ?? '';
    // Numbers strip: context and cost.
    expect(text()).toContain('context');
    expect(text()).toContain('420');
    expect(text()).toContain('$1.25');
    // Meta line: task, account, both ends of the model change.
    expect(text()).toContain('2026-11-03-line-items');
    expect(text()).toContain('work');
    expect(text()).toContain('fell back to');
    // Alarms: cache cold and the conflict.
    await waitFor(() => expect(within(detail).getByTestId('swarm-conflict')).toBeTruthy());
    expect(text().toLowerCase()).toContain('cold');
    // Rail: the handoff brief.
    const rail = screen.getByRole('complementary', { name: 'session context' });
    expect(within(rail).getByRole('button', { name: 'copy resume command' })).toBeTruthy();
    // ⋯: the process and the plan limit.
    fireEvent.click(screen.getByRole('button', { name: 'more for this session' }));
    const menu = screen.getByRole('group', { name: 'more for this session' });
    expect(within(menu).getByText('exited')).toBeTruthy();
    expect(menu.textContent).toContain('91');
  });
});
