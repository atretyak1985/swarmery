// Unit tests for the terminal dock's pure state/render decisions (bugfix:
// footer "Terminal" toggle must collapse, not destroy, the session). Pure
// logic, no DOM — the render itself (XTerm mount, live WebSocket) is left to a
// Playwright e2e; here we lock the decisions that were wrong.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/terminal/TerminalDock.test.ts`.
// web/tsconfig.json EXCLUDES *.test.ts, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { describe, expect, it } from 'vitest';
import {
  type DockState,
  dockView,
  emptyDock,
  openProjectTerminal,
  openWorktreeTerminal,
  toggleDock,
} from './TerminalDock';

const PATH = '/Volumes/Work/swarmery';

describe('toggleDock', () => {
  it('opens a first project-root terminal when none exist', () => {
    const next = toggleDock(emptyDock(), PATH);
    expect(next.open).toBe(true);
    expect(next.tabs).toHaveLength(1);
    expect(next.tabs[0].cwd).toBe(PATH);
    expect(next.activeId).toBe(next.tabs[0].id);
  });

  it('is a no-op while the project path is unresolved', () => {
    const before = emptyDock();
    expect(toggleDock(before, '')).toBe(before);
  });

  it('collapses without dropping tabs — the PTY survives (regression)', () => {
    const open = openProjectTerminal(emptyDock(), PATH);
    const collapsed = toggleDock(open, PATH);
    expect(collapsed.open).toBe(false);
    // The bug was clearing tabs on the second click; assert they persist so the
    // shell + scrollback are kept alive behind a hidden panel.
    expect(collapsed.tabs).toEqual(open.tabs);
    expect(collapsed.activeId).toBe(open.activeId);
  });

  it('expands the same tabs back on the next click (round-trip is identity)', () => {
    const open = openProjectTerminal(emptyDock(), PATH);
    const reopened = toggleDock(toggleDock(open, PATH), PATH);
    expect(reopened).toEqual(open);
  });

  it('preserves every tab and the active one when collapsing a multi-tab dock', () => {
    const a = openProjectTerminal(emptyDock(), PATH);
    const b = openWorktreeTerminal(a, 'task-1', '/wt/task-1');
    const collapsed = toggleDock(b, PATH);
    expect(collapsed.tabs).toHaveLength(2);
    expect(collapsed.tabs).toEqual(b.tabs);
    expect(collapsed.activeId).toBe(b.activeId);
  });
});

describe('dockView', () => {
  it('does not mount when there are no tabs', () => {
    expect(dockView(emptyDock())).toEqual({ mount: false, hidden: true });
  });

  it('mounts and shows an open dock with tabs', () => {
    const open = openProjectTerminal(emptyDock(), PATH);
    expect(dockView(open)).toEqual({ mount: true, hidden: false });
  });

  it('stays mounted but hidden while collapsed — never unmounts (regression)', () => {
    const collapsed: DockState = { ...openProjectTerminal(emptyDock(), PATH), open: false };
    const view = dockView(collapsed);
    // mount MUST remain true: unmounting closes the WebSocket and kills the shell.
    expect(view.mount).toBe(true);
    expect(view.hidden).toBe(true);
  });
});
