// @vitest-environment jsdom
//
// Settings (Canvas v3 phase 8). The claims:
//
//   1. Four tabs in order — Appearance · Accounts · Notifications · Projects —
//      with Appearance (theme + host footer) selected by default.
//   2. `?tab=projects` shows the project list, embedded (no heading of its own).
//   3. Accounts and Notifications each render their section alone.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/settings`.

import { cleanup, render, screen } from '../../test/render';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Settings } from '../Settings';

vi.mock('../../theme/ThemePicker', () => ({ ThemePickerPanel: () => <div>theme picker</div> }));
vi.mock('../../components/AccountsSection', () => ({ AccountsSection: () => <div>accounts list</div> }));
vi.mock('../../components/ConnectorsSection', () => ({ ConnectorsSection: () => <div>connectors list</div> }));
vi.mock('../../components/NotifySettings', () => ({ NotifySettings: () => <div>notify controls</div> }));
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
  useNotifyPrefs: () => ({ prefs: {}, setPrefs: vi.fn() }),
}));

function renderAt(url: string): void {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Settings />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('Settings', () => {
  it('renders the four tabs in order, Appearance by default', () => {
    renderAt('/settings');
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual([
      'Appearance',
      'Accounts',
      'Notifications',
      'Projects',
    ]);
    expect(screen.getByRole('tab', { name: 'Appearance' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByText('theme picker')).toBeTruthy();
    expect(screen.getByText('connectors list')).toBeTruthy();
    expect(screen.queryByText('accounts list')).toBeNull();
  });

  it('shows the project list on ?tab=projects', () => {
    renderAt('/settings?tab=projects');
    expect(screen.getByRole('tab', { name: 'Projects' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByText('project list · embedded=true')).toBeTruthy();
    expect(screen.queryByText('theme picker')).toBeNull();
  });

  it('renders accounts and notifications on their own tabs', () => {
    renderAt('/settings?tab=accounts');
    expect(screen.getByText('accounts list')).toBeTruthy();
    expect(screen.queryByText('notify controls')).toBeNull();
    cleanup();
    renderAt('/settings?tab=notifications');
    expect(screen.getByText('notify controls')).toBeTruthy();
    expect(screen.queryByText('accounts list')).toBeNull();
  });
});
