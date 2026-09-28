// @vitest-environment jsdom
//
// The one sidebar (Canvas v3 phase 1). The claims:
//
//   1. It renders the nine places in both scopes, and exactly one numeric badge
//      (Inbox) — every other nav badge was retired with the old rails.
//   2. Under All projects the project-only places (Plans, Knowledge) are still
//      there, dimmed, and link through the last-visited project — or to the
//      project list when none was ever opened.
//   3. Sessions shows a live dot only while a session is live.
//   4. ⌘K / Ctrl+K opens the command palette (the sidebar owns the listener).
//   5. useSidebarSignals feeds Inbox = the Inbox's own count (useInboxItems).
//
// Dev-only suite (web/tsconfig.json excludes *.test.tsx). Run with
//   npx vitest run src/components/Sidebar.test.tsx
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Sidebar, useSidebarSignals } from './Sidebar';

vi.mock('../api', () => ({
  fetchApprovals: vi.fn(async () => [{ id: 1 }, { id: 2 }]),
  fetchStatsOverview: vi.fn(async () => ({ active: 1, waiting_approval: 0 })),
}));

vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

// The badge is the Inbox's six-source aggregate; its own suite covers the sources.
vi.mock('../pages/inbox/useInboxItems', () => ({
  useInboxItems: () => ({ items: [], count: 5, loading: false, errors: [], reload: () => undefined }),
}));

// The palette is its own feature; here it only has to appear.
vi.mock('./CommandPalette', () => ({
  CommandPalette: () => <div role="dialog" aria-label="command palette" />,
}));

function renderSidebar(
  props: { slug: string | null; inboxCount?: number; liveSessions?: boolean },
  path = '/',
): void {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Sidebar
        slug={props.slug}
        inboxCount={props.inboxCount ?? 0}
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
});

afterEach(cleanup);

describe('Sidebar', () => {
  it('renders the nine places in both scopes', () => {
    const labels = ['Today', 'Inbox', 'Sessions', 'Plans', 'Health', 'Learning', 'Knowledge', 'System', 'Settings'];
    renderSidebar({ slug: null });
    for (const label of labels) expect(row(label)).toBeTruthy();
    cleanup();
    renderSidebar({ slug: 'shop' }, '/p/shop');
    for (const label of labels) expect(row(label)).toBeTruthy();
    expect(row('Plans').getAttribute('href')).toBe('/p/shop/plans');
  });

  it('shows exactly one numeric badge, on Inbox', () => {
    renderSidebar({ slug: null, inboxCount: 9, liveSessions: true });
    const numeric = within(rail())
      .getAllByRole('link')
      .filter((a) => /\d/.test(a.textContent ?? ''));
    expect(numeric).toHaveLength(1);
    expect(numeric[0]?.textContent).toContain('Inbox');
    expect(within(row('Inbox')).getByText('9')).toBeTruthy();
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
  return <output>{`${s.inboxCount}|${String(s.liveSessions)}`}</output>;
}

describe('useSidebarSignals', () => {
  it('takes the Inbox count from useInboxItems', async () => {
    render(
      <MemoryRouter>
        <SignalsProbe />
      </MemoryRouter>,
    );
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByRole('status').textContent).toBe('5|true');
  });
});
