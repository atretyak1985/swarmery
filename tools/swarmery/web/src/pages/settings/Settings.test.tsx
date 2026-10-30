// @vitest-environment jsdom
//
// Settings in the frame (v4, handoff §9). The claims:
//
//   1. One accordion group of rows, Appearance selected by default, with no tab bar.
//   2. Every old `?tab=` value lands on its row: `?tab=projects` is the project list,
//      embedded, and Accounts and Notifications show alone.
//   3. Appearance really switches: the theme segments, compact and motion all reach
//      the provider and localStorage, and compact changes a ListRow's padding.
//   4. What the old Appearance tab held stays reachable: palette, language, the
//      host panels.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/settings`.

import { cleanup, fireEvent, render, screen } from '../../test/render';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ListRow } from '../../components/place/ListRow';
import { StateGlyph } from '../../components/place/StateGlyph';
import { ThemeProvider } from '../../lib/theme';
import { Settings } from '../Settings';

vi.mock('../../theme/ThemePicker', () => ({ ThemePickerPanel: () => <div>theme picker</div> }));
vi.mock('../../components/AccountsSection', () => ({ AccountsSection: () => <div>accounts list</div> }));
vi.mock('../../components/ConnectorsSection', () => ({ ConnectorsSection: () => <div>connectors list</div> }));
vi.mock('../../components/LanguagePicker', () => ({ LanguagePicker: () => <div>language picker</div> }));
vi.mock('../../components/Explain', () => ({
  ExplainPair: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock('./WorktreesPanel', () => ({ WorktreesPanel: () => <div>worktrees list</div> }));
vi.mock('../Projects', () => ({
  Projects: ({ embedded }: { embedded?: boolean }) => <div>project list · embedded={String(embedded)}</div>,
}));
vi.mock('../../lib/health', () => ({
  useHealth: () => ({ health: null, unreachable: false }),
  versionLabel: () => 'v',
  versionTitle: () => 'v',
}));
vi.mock('../../lib/notifyPrefsContext', () => ({
  useNotifyPrefs: () => ({
    prefs: { approvalRequested: false, sessionFinished: false, sessionError: false },
    setPrefs: vi.fn(),
  }),
}));

const STORAGE_KEY = 'swarmery.theme';

function renderAt(url: string): void {
  render(
    <ThemeProvider>
      <MemoryRouter initialEntries={[url]}>
        <Settings />
      </MemoryRouter>
    </ThemeProvider>,
  );
}

function stored(): Record<string, unknown> {
  return JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? '{}') as Record<string, unknown>;
}

beforeEach(() => window.localStorage.clear());
afterEach(cleanup);

describe('Settings', () => {
  it('lists its rows in one group, Appearance by default, with no tab bar', () => {
    renderAt('/settings');
    expect(screen.queryAllByRole('tab')).toEqual([]);
    expect(screen.getByRole('heading', { name: 'Appearance' })).toBeTruthy();
    for (const row of ['Palette', 'Language', 'Accounts', 'Notifications', 'Projects', 'This machine', 'Keyboard']) {
      expect(screen.getByRole('button', { name: new RegExp(row) })).toBeTruthy();
    }
  });

  it('shows the project list on ?tab=projects', () => {
    renderAt('/settings?tab=projects');
    expect(screen.getByText('project list · embedded=true')).toBeTruthy();
    expect(screen.queryByText('Theme')).toBeNull();
  });

  it('renders accounts and notifications on their own rows', () => {
    renderAt('/settings?tab=accounts');
    expect(screen.getByText('accounts list')).toBeTruthy();
    expect(screen.queryByRole('switch')).toBeNull();
    cleanup();
    renderAt('/settings?tab=notifications');
    expect(screen.getAllByRole('switch').map((s) => s.getAttribute('aria-label'))).toEqual([
      'Needs you',
      'Finished',
      'Failed',
    ]);
    expect(screen.queryByText('accounts list')).toBeNull();
  });

  it('keeps the palette, the language and the host panels reachable', () => {
    renderAt('/settings?tab=palette');
    expect(screen.getByText('theme picker')).toBeTruthy();
    cleanup();
    renderAt('/settings?tab=language');
    expect(screen.getByText('language picker')).toBeTruthy();
    cleanup();
    renderAt('/settings?tab=machine');
    expect(screen.getByText('worktrees list')).toBeTruthy();
    expect(screen.getByText('connectors list')).toBeTruthy();
    expect(screen.getByText('daemon healthy')).toBeTruthy();
  });

  it('switches the theme for real', () => {
    renderAt('/settings');
    fireEvent.click(screen.getByRole('radio', { name: 'light' }));
    expect(document.documentElement.dataset.mode).toBe('light');
    expect(stored().mode).toBe('light');
    fireEvent.click(screen.getByRole('radio', { name: 'dark' }));
    expect(document.documentElement.dataset.mode).toBe('dark');
    expect(stored().mode).toBe('dark');
  });

  it('stores compact locally and it changes the padding of a list row', () => {
    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={['/settings']}>
          <Settings />
          <ListRow glyph="●" title="a probe row" />
        </MemoryRouter>
      </ThemeProvider>,
    );
    const probe = (): HTMLElement => screen.getByRole('button', { name: /a probe row/ });
    expect(probe().className).toContain('py-[9px]');
    fireEvent.click(screen.getByRole('switch', { name: 'Compact rows' }));
    expect(probe().className).toContain('py-1.5');
    expect(stored().compact).toBe(true);
  });

  it('stops the state glyphs when motion is off', () => {
    render(
      <ThemeProvider>
        <MemoryRouter initialEntries={['/settings']}>
          <Settings />
          <StateGlyph state="working" />
        </MemoryRouter>
      </ThemeProvider>,
    );
    const glyph = (): HTMLElement => screen.getByRole('img', { name: 'working' });
    expect(glyph().className).toContain('animate-glyph-spin');
    fireEvent.click(screen.getByRole('switch', { name: 'Motion' }));
    expect(glyph().className).not.toContain('animate-glyph-spin');
    expect(stored().motion).toBe(false);
  });

  it('restores compact and motion from storage, and keeps an older stored choice whole', () => {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ mode: 'dark', palette: 'ocean' }));
    renderAt('/settings');
    expect(screen.getByRole('switch', { name: 'Compact rows' }).getAttribute('aria-checked')).toBe('false');
    expect(screen.getByRole('switch', { name: 'Motion' }).getAttribute('aria-checked')).toBe('true');
    cleanup();
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ mode: 'dark', compact: true, motion: false }));
    renderAt('/settings');
    expect(screen.getByRole('switch', { name: 'Compact rows' }).getAttribute('aria-checked')).toBe('true');
    expect(screen.getByRole('switch', { name: 'Motion' }).getAttribute('aria-checked')).toBe('false');
  });
});
