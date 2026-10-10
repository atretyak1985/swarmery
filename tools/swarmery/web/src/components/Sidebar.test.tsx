// @vitest-environment jsdom
//
// The one sidebar (Canvas v3 phase 1). The claims:
//
//   1. It renders the eleven places in both scopes, and numeric badges only on
//      Inbox and Needs you — every other nav badge was retired with the old rails.
//   2. Under All projects the project-only places (Plans, Knowledge) are still
//      there, dimmed, and link through the last-visited project — or to the
//      project list when none was ever opened.
//   3. Sessions shows a live dot only while a session is live.
//   4. ⌘K / Ctrl+K opens the command palette (the sidebar owns the listener).
//   5. useSidebarSignals feeds Inbox = the Inbox's own count (useInboxItems) and
//      Needs you = the queue's undismissed count (useNeedsYou).
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/components/Sidebar.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { act, cleanup, fireEvent, render, screen, within } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Sidebar, useSidebarSignals } from './Sidebar';
import { fetchSessions, fetchStatsOverview } from '../api';

vi.mock('../api', () => ({
  fetchApprovals: vi.fn(async () => [{ id: 1 }, { id: 2 }]),
  fetchStatsOverview: vi.fn(async () => ({ active: 1, waiting_approval: 0 })),
  fetchSessions: vi.fn(async () => ({ sessions: [{ id: 1 }], nextCursor: null })),
}));

vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

// The badge is the Inbox's six-source aggregate; its own suite covers the sources.
vi.mock('../pages/inbox/useInboxItems', () => ({
  useInboxItems: () => ({ items: [], count: 5, loading: false, errors: [], reload: () => undefined }),
}));

// The Needs you badge is the queue's own count; NeedsYou.test.tsx covers the queue.
vi.mock('../lib/useNeedsYou', () => ({
  useNeedsYou: () => ({ items: [], count: 3, loading: false, error: null, reload: () => undefined, dismiss: () => undefined }),
}));

// The palette is its own feature; here it only has to appear.
vi.mock('./CommandPalette', () => ({
  CommandPalette: () => <div role="dialog" aria-label="command palette" />,
}));

function renderSidebar(
  props: { slug: string | null; inboxCount?: number; needsYouCount?: number; liveSessions?: boolean },
  path = '/',
): void {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Sidebar
        slug={props.slug}
        inboxCount={props.inboxCount ?? 0}
        needsYouCount={props.needsYouCount ?? 0}
        liveSessions={props.liveSessions ?? false}
      />
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

  it('dims project-only places under All projects and routes them through the last project', () => {
    window.localStorage.setItem('swarmery.lastProject', 'shop');
    renderSidebar({ slug: null });
    for (const name of ['Plans', 'Knowledge']) expect(row(name).className).toContain('opacity-60');
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

  it('never dims inside a project', () => {
    renderSidebar({ slug: 'shop' }, '/p/shop');
    expect(row('Plans').className).not.toContain('opacity-60');
  });

  it('shows the Sessions live dot only while sessions are live', () => {
    renderSidebar({ slug: null, liveSessions: false });
    expect(screen.queryByRole('img', { name: 'live sessions' })).toBeNull();
    cleanup();
    renderSidebar({ slug: null, liveSessions: true });
    expect(within(row('Sessions')).getByRole('img', { name: 'live sessions' })).toBeTruthy();
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
    fireEvent.click(screen.getByRole('button', { name: /search & actions/ }));
    expect(screen.getByRole('dialog', { name: 'command palette' })).toBeTruthy();
  });
});

function SignalsProbe(): JSX.Element {
  const s = useSidebarSignals();
  return <output>{`${s.inboxCount}|${s.needsYouCount}|${String(s.liveSessions)}`}</output>;
}

describe('useSidebarSignals', () => {
  it('takes the Inbox count from useInboxItems and the Needs you count from useNeedsYou', async () => {
    render(
      <MemoryRouter>
        <SignalsProbe />
      </MemoryRouter>,
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByRole('status').textContent).toBe('5|3|true');
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
    expect(screen.getByRole('status').textContent).toBe('5|3|false');
  });
});
