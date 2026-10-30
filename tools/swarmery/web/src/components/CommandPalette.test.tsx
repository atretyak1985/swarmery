// @vitest-environment jsdom
//
// The palette's two v4 jobs (search itself is older and lives in the page flows):
//
//   1. Text longer than 8 characters + Enter creates a card in Todo through the
//      existing create API, in the project in scope. Exactly 8 does not.
//   2. Board and Playbooks, off the sidebar on purpose, are reachable from here.
//
// Runs with the rest of the web suite: `npm test`.

import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { createBoardTask } from '../api';
import { useScope } from '../lib/scope';
import { CommandPalette } from './CommandPalette';

vi.mock('../api', () => ({
  createBoardTask: vi.fn(async () => ({ id: 1 })),
  fetchSearch: vi.fn(async () => ({ sessions: [], turns: [], files: [], projects: [] })),
  fetchFileSessions: vi.fn(async () => ({ sessions: [] })),
}));

vi.mock('../lib/scope', () => ({ useScope: vi.fn() }));

const SHOP = { id: 7, name: 'Shop', slug: '-work-shop' };
const onClose = vi.fn();

function Where(): JSX.Element {
  return <output aria-label="where">{useLocation().pathname}</output>;
}

function renderPalette(slug: string | null): HTMLElement {
  render(
    <MemoryRouter>
      <CommandPalette slug={slug} onClose={onClose} />
      <Where />
    </MemoryRouter>,
  );
  return screen.getByRole('textbox');
}

function type(input: HTMLElement, value: string): void {
  fireEvent.change(input, { target: { value } });
}

beforeEach(() => {
  window.localStorage.clear();
  vi.clearAllMocks();
  Element.prototype.scrollIntoView = () => undefined; // jsdom has no layout
  vi.mocked(useScope).mockReturnValue({
    scope: null,
    scopeName: null,
    scopeProject: null,
    projects: [SHOP],
    setScope: () => undefined,
  } as never);
});

afterEach(cleanup);

describe('CommandPalette dispatch', () => {
  it('creates a Todo card from text longer than 8 characters', async () => {
    const input = renderPalette('-work-shop');
    type(input, 'fix the flaky login test');
    fireEvent.keyDown(input, { key: 'Enter' });
    await act(async () => {
      await Promise.resolve();
    });
    expect(createBoardTask).toHaveBeenCalledWith({
      projectId: 7,
      title: 'fix the flaky login test',
      prompt: 'fix the flaky login test',
      boardColumn: 'todo',
    });
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('does not treat exactly 8 characters as a task', () => {
    const input = renderPalette('-work-shop');
    type(input, '12345678');
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(createBoardTask).not.toHaveBeenCalled();
  });

  it('trims before counting, so padding cannot dispatch', () => {
    const input = renderPalette('-work-shop');
    type(input, '   short   ');
    expect(screen.queryByText(/new session/)).toBeNull();
  });

  it('names the project the card will land in', () => {
    const input = renderPalette('-work-shop');
    type(input, 'fix the flaky login test');
    expect(screen.getByText('⏎ new session: "fix the flaky login test"')).toBeTruthy();
    expect(screen.getByText('Shop')).toBeTruthy();
  });

  it('falls back to the last project under All projects', () => {
    window.localStorage.setItem('swarmery.lastProject', '-work-shop');
    const input = renderPalette(null);
    type(input, 'fix the flaky login test');
    expect(screen.getByText('Shop')).toBeTruthy();
  });

  it('offers no dispatch when there is no project to put the card in', () => {
    const input = renderPalette(null);
    type(input, 'fix the flaky login test');
    expect(screen.queryByText(/new session/)).toBeNull();
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(createBoardTask).not.toHaveBeenCalled();
  });

  it('stays open with the error when the create fails', async () => {
    vi.mocked(createBoardTask).mockRejectedValueOnce(new Error('board is down'));
    const input = renderPalette('-work-shop');
    type(input, 'fix the flaky login test');
    fireEvent.keyDown(input, { key: 'Enter' });
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.getByRole('alert').textContent).toBe('board is down');
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe('CommandPalette navigation', () => {
  it.each([
    ['Board', '/p/shop/board'],
    ['Playbooks', '/p/shop/playbooks'],
  ])('reaches %s, which the sidebar no longer lists', (label, path) => {
    const input = renderPalette('-work-shop');
    type(input, label);
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(screen.getByRole('status', { name: 'where' }).textContent).toBe(path);
  });

  // Enter on a dispatch row starts a session. "Playbooks" is past the 8-character
  // mark, so a place's name must not be read as a task.
  it('navigates on Enter when long text names a place, and keeps dispatch one arrow away', () => {
    const input = renderPalette('-work-shop');
    type(input, 'Playbooks');
    expect(screen.getByText('⏎ new session: "Playbooks"')).toBeTruthy();
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(createBoardTask).not.toHaveBeenCalled();
    expect(screen.getByRole('status', { name: 'where' }).textContent).toBe('/p/shop/playbooks');
  });

  it('keeps the older targets', () => {
    renderPalette(null);
    for (const label of ['Overview', 'Analytics', 'Approvals', 'Projects', 'System', 'Docs']) {
      expect(screen.getByText(label)).toBeTruthy();
    }
  });

  it('leaves project targets out until there is a project', () => {
    renderPalette(null);
    expect(screen.queryByText('Board')).toBeNull();
  });
});
