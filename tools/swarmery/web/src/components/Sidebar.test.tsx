// @vitest-environment jsdom
//
// The one sidebar (Canvas v3 phase 1). The claims:
//
//   1. It renders the eleven places in both scopes, and numeric badges only on
//      Inbox, Needs you and Sessions.
//   2. Under All projects the project-only places (Plans, Knowledge) are still
//      there, dimmed, and link through the last-visited project — or to the
//      project list when none was ever opened.
//   3. Sessions shows a live dot while a session is live and nothing needs you.
//   4. ⌘K / Ctrl+K opens the command palette (the sidebar owns the listener).
//   5. 1–5 go to the main places, t flips the theme, and neither fires in a field.
//   6. useSidebarSignals feeds Inbox = the non-blocking part of the Inbox's items,
//      Needs you = the queue's undismissed count, Sessions = the distinct sessions
//      held up in that queue.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/components/Sidebar.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { act, cleanup, fireEvent, render, screen, within } from '../test/render';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Sidebar, sessionsNeedingYou, useSidebarSignals } from './Sidebar';
import { fetchSessions, fetchStatsOverview } from '../api';
import type { NeedsYouItem } from '../api/types';
import { ThemeProvider } from '../lib/theme';

vi.mock('../api', () => ({
  fetchApprovals: vi.fn(async () => [{ id: 1 }, { id: 2 }]),
  fetchStatsOverview: vi.fn(async () => ({ active: 1, waiting_approval: 0 })),
  fetchSessions: vi.fn(async () => ({ sessions: [{ id: 1 }], nextCursor: null })),
}));

vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

// The badge counts the Inbox's aggregate minus approvals, which moved to Needs you;
// its own suite covers the sources.
vi.mock('../pages/inbox/useInboxItems', () => ({
  useInboxItems: () => ({
    items: [{ kind: 'lesson' }, { kind: 'advisor' }, { kind: 'approval' }, { kind: 'retire' }],
    count: 4,
    loading: false,
    errors: [],
    reload: () => undefined,
  }),
}));

// The Needs you badge is the queue's own count; NeedsYou.test.tsx covers the queue.
vi.mock('../lib/useNeedsYou', () => ({
  useNeedsYou: () => ({
    items: [
      { kind: 'approval', sessionId: 7 },
      { kind: 'question', sessionId: 7 },
      { kind: 'manual_phase', sessionId: 0 },
    ],
    count: 3,
    loading: false,
    error: null,
    reload: () => undefined,
    dismiss: () => undefined,
  }),
}));

// The palette is its own feature; here it only has to appear.
vi.mock('./CommandPalette', () => ({
  CommandPalette: () => <div role="dialog" aria-label="command palette" />,
}));

// The footer has its own suite and its own pollers.
vi.mock('./HealthFooter', () => ({ HealthFooter: () => null }));

function Where(): JSX.Element {
  return <output aria-label="where">{useLocation().pathname}</output>;
}

function renderSidebar(
  props: {
    slug: string | null;
    inboxCount?: number;
    needsYouCount?: number;
    sessionsNeedCount?: number;
    liveSessions?: boolean;
  },
  path = '/',
): void {
  render(
    <MemoryRouter initialEntries={[path]}>
      <ThemeProvider>
        <Sidebar
          slug={props.slug}
          inboxCount={props.inboxCount ?? 0}
          needsYouCount={props.needsYouCount ?? 0}
          sessionsNeedCount={props.sessionsNeedCount ?? 0}
          liveSessions={props.liveSessions ?? false}
        />
        <Where />
      </ThemeProvider>
    </MemoryRouter>,
  );
}

function rail(): HTMLElement {
  return screen.getByRole('navigation', { name: 'primary' });
}

function row(name: string): HTMLElement {
  return within(rail()).getByRole('link', { name: new RegExp(`^${name}`) });
}

beforeEach(() => {
  window.localStorage.clear();
  vi.clearAllMocks();
});

afterEach(cleanup);

describe('Sidebar', () => {
  it('renders the eleven places in both scopes', () => {
    const labels = ['Today', 'Inbox', 'Needs you', 'Sessions', 'Plans', 'Health', 'Learning', 'Knowledge', 'Docs', 'System', 'Settings'];
    renderSidebar({ slug: null });
    for (const label of labels) expect(row(label)).toBeTruthy();
    cleanup();
    renderSidebar({ slug: 'shop' }, '/p/shop');
    for (const label of labels) expect(row(label)).toBeTruthy();
    expect(row('Plans').getAttribute('href')).toBe('/p/shop/plans');
    expect(row('Docs').getAttribute('href')).toBe('/docs');
    expect(row('Needs you').getAttribute('href')).toBe('/p/shop/needs-you');
  });

  it('shows only the Inbox badge while nothing needs you', () => {
    renderSidebar({ slug: null, inboxCount: 9, liveSessions: true });
    const numeric = within(rail())
      .getAllByRole('link')
      .filter((a) => /\d/.test(a.textContent ?? ''));
    expect(numeric).toHaveLength(1);
    expect(numeric[0]?.textContent).toContain('Inbox');
    expect(within(row('Inbox')).getByText('9')).toBeTruthy();
  });

  it('shows the Needs you count badge next to the Inbox one, and hides it at zero', () => {
    renderSidebar({ slug: null, inboxCount: 9, needsYouCount: 4 });
    const numeric = within(rail())
      .getAllByRole('link')
      .filter((a) => /\d/.test(a.textContent ?? ''));
    expect(numeric.map((a) => a.textContent)).toEqual(['☐Inbox9', '⚑Needs you4']);
    expect(within(row('Needs you')).getByLabelText('4 waiting')).toBeTruthy();
    cleanup();
    renderSidebar({ slug: null, needsYouCount: 0 });
    expect(row('Needs you').textContent).toBe('⚑Needs you');
  });

  it('hides the Inbox badge at zero', () => {
    renderSidebar({ slug: null, inboxCount: 0 });
    expect(row('Inbox').textContent).toBe('☐Inbox');
  });

  it('keeps amber for what blocks: Needs you and Sessions, never Inbox', () => {
    renderSidebar({ slug: null, inboxCount: 5, needsYouCount: 7, sessionsNeedCount: 2 });
    expect(within(row('Needs you')).getByText('7').className).toContain('text-brand');
    expect(within(row('Sessions')).getByText('2').className).toContain('text-brand');
    expect(within(row('Inbox')).getByText('5').className).not.toContain('text-brand');
  });

  it('never badges System', () => {
    renderSidebar({ slug: null, inboxCount: 5, needsYouCount: 7, sessionsNeedCount: 2 });
    expect(row('System').textContent).toBe('☷System');
  });

  it('is the 196px rail', () => {
    renderSidebar({ slug: null });
    expect(rail().className).toContain('w-[196px]');
  });

  it('carries the wordmark home, on the fleet and inside a project', () => {
    renderSidebar({ slug: null });
    expect(within(rail()).getByRole('link', { name: 'swarmery home' }).getAttribute('href')).toBe('/');
    cleanup();
    renderSidebar({ slug: 'shop' }, '/p/shop');
    expect(within(rail()).getByRole('link', { name: 'back to all projects' }).getAttribute('href')).toBe('/');
  });

  it('dims project-only places under All projects and routes them through the last project', () => {
    window.localStorage.setItem('swarmery.lastProject', 'shop');
    renderSidebar({ slug: null });
    for (const name of ['Plans', 'Knowledge']) expect(row(name).getAttribute('title')).toContain('opens in a project');
    for (const name of ['Today', 'Inbox', 'Sessions', 'Health']) {
      expect(row(name).className).not.toContain('opacity-60');
    }
    expect(row('Plans').getAttribute('href')).toBe('/p/shop/plans');
    expect(row('Knowledge').getAttribute('href')).toBe('/p/shop/knowledge');
  });

  it('sends project-only places to the project list when no project was ever opened', () => {
    renderSidebar({ slug: null });
    expect(row('Plans').getAttribute('href')).toBe('/projects');
  });

  it('shows the switcher in its All-projects state in the fleet shell', () => {
    renderSidebar({ slug: null });
    expect(screen.getByRole('button', { name: 'switch project' }).textContent).toContain('All projects');
    cleanup();
    renderSidebar({ slug: 'shop' }, '/p/shop');
    expect(screen.getByRole('button', { name: 'switch project' }).textContent).toContain('shop');
  });

  it('renders project-only places like any other row, explaining the hop only under All projects', () => {
    renderSidebar({ slug: null });
    expect(row('Plans').className).not.toContain('opacity-60');
    expect(row('Knowledge').className).not.toContain('opacity-60');
    cleanup();
    renderSidebar({ slug: 'shop' }, '/p/shop');
    expect(row('Plans').getAttribute('title')).toBeNull();
  });

  it('shows the Sessions live dot only while sessions are live', () => {
    renderSidebar({ slug: null, liveSessions: false });
    expect(screen.queryByRole('img', { name: 'live sessions' })).toBeNull();
    cleanup();
    renderSidebar({ slug: null, liveSessions: true });
    expect(within(row('Sessions')).getByRole('img', { name: 'live sessions' })).toBeTruthy();
  });

  it('gives the Sessions row the badge instead of the dot when sessions need you', () => {
    renderSidebar({ slug: null, liveSessions: true, sessionsNeedCount: 2 });
    expect(screen.queryByRole('img', { name: 'live sessions' })).toBeNull();
    expect(within(row('Sessions')).getByText('2')).toBeTruthy();
  });

  it('marks the place owning the current path, including absorbed pages', () => {
    renderSidebar({ slug: 'shop' }, '/p/shop/serena');
    expect(row('Knowledge').getAttribute('aria-current')).toBe('page');
    expect(row('Today').getAttribute('aria-current')).toBeNull();
  });

  it('opens the command palette on ⌘K and Ctrl+K, and from the footer button', () => {
    renderSidebar({ slug: null });
    expect(screen.queryByRole('dialog', { name: 'command palette' })).toBeNull();
    fireEvent.keyDown(window, { key: 'k', metaKey: true });
    expect(screen.getByRole('dialog', { name: 'command palette' })).toBeTruthy();
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true }); // toggles closed
    expect(screen.queryByRole('dialog', { name: 'command palette' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /Search & actions/ }));
    expect(screen.getByRole('dialog', { name: 'command palette' })).toBeTruthy();
  });

  it('goes to the five main places on 1–5, in the design order', () => {
    renderSidebar({ slug: 'shop' }, '/p/shop');
    const where = (): string | null => screen.getByRole('status', { name: 'where' }).textContent;
    const expected: [string, string][] = [
      ['2', '/p/shop/inbox'],
      ['3', '/p/shop/needs-you'],
      ['4', '/p/shop/sessions'],
      ['5', '/p/shop/plans'],
      ['1', '/p/shop'],
    ];
    for (const [key, path] of expected) {
      fireEvent.keyDown(window, { key });
      expect(where()).toBe(path);
    }
  });

  it('flips the theme on t and on the rail button', () => {
    renderSidebar({ slug: null });
    const before = document.documentElement.dataset.mode;
    fireEvent.keyDown(window, { key: 't' });
    const after = document.documentElement.dataset.mode;
    expect(after).not.toBe(before);
    fireEvent.click(screen.getByRole('button', { name: 'theme' }));
    expect(document.documentElement.dataset.mode).toBe(before);
  });

  it('leaves 1–5 and t alone while typing in a field', () => {
    renderSidebar({ slug: 'shop' }, '/p/shop');
    const before = document.documentElement.dataset.mode;
    const field = document.body.appendChild(document.createElement('input'));
    fireEvent.keyDown(field, { key: '5' });
    fireEvent.keyDown(field, { key: 't' });
    field.remove();
    expect(screen.getByRole('status', { name: 'where' }).textContent).toBe('/p/shop');
    expect(document.documentElement.dataset.mode).toBe(before);
  });
});

describe('sessionsNeedingYou', () => {
  const item = (kind: string, sessionId: number): NeedsYouItem => ({ kind, sessionId }) as NeedsYouItem;

  it('counts one session once, however many requests it holds', () => {
    expect(sessionsNeedingYou([item('approval', 1), item('question', 1), item('awaiting_reply', 2)])).toBe(2);
  });

  it('leaves out phases and failures: they wait on you without being held-up sessions', () => {
    expect(sessionsNeedingYou([item('manual_phase', 0), item('awaiting_merge', 0), item('failed', 3)])).toBe(0);
  });
});

function SignalsProbe(): JSX.Element {
  const s = useSidebarSignals();
  return <output>{`${s.inboxCount}|${s.needsYouCount}|${s.sessionsNeedCount}|${String(s.liveSessions)}`}</output>;
}

describe('useSidebarSignals', () => {
  it('counts the Inbox without approvals, the whole queue, and the sessions held up in it', async () => {
    render(
      <MemoryRouter>
        <SignalsProbe />
      </MemoryRouter>,
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByRole('status').textContent).toBe('3|3|1|true');
  });

  // The live dot is a now-property: one active session is enough. It must come
  // from the cheap one-row sessions page, never from stats/overview, whose
  // 14-day series held the daemon's single DB connection for seconds on every
  // page and every session update.
  it('asks for one active session, not the overview stats', async () => {
    render(
      <MemoryRouter>
        <SignalsProbe />
      </MemoryRouter>,
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(fetchSessions).toHaveBeenCalledWith({ status: 'active' }, { limit: 1 });
    expect(fetchStatsOverview).not.toHaveBeenCalled();
  });

  it('turns the live dot off when no session is active', async () => {
    vi.mocked(fetchSessions).mockResolvedValueOnce({ sessions: [], nextCursor: null } as never);
    render(
      <MemoryRouter>
        <SignalsProbe />
      </MemoryRouter>,
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByRole('status').textContent).toBe('3|3|1|false');
  });
});
