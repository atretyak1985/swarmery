// @vitest-environment jsdom
//
// One session row (artboard 2a on v4, plan D-7): the shared ListRow grid, the
// project tag only when the list spans every project (D-2), and at most ONE
// warning flag, worst first: context, then a shared file, then the plan's limit.
//
// Runs with the rest of the web suite: `npm test`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Session, SessionLink } from '../api/types';
import { cleanup, render, screen } from '../test/render';
import { SessionRow } from './SessionRow';

const conflicts = vi.hoisted(() => ({ ids: new Set<number>() }));
vi.mock('../lib/swarm', () => ({
  useSessionConflicts: (id: number) => (conflicts.ids.has(id) ? ['src/orders.ts'] : []),
}));

function link(percentUsed: number): SessionLink {
  return {
    status: null,
    seenAt: null,
    live: false,
    contextPct: null,
    rateLimits: [{ kind: 'five_hour', percentUsed, resetsAt: null }],
    costUsd: null,
  } as unknown as SessionLink;
}

function makeSession(over: Partial<Session> = {}): Session {
  return {
    id: 7,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'orders-api',
    sessionUuid: 'uuid-7',
    model: 'claude-opus-5-5',
    modelLast: 'claude-opus-5-5',
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'feat/x',
    cwd: '/w',
    status: 'completed',
    startedAt: new Date(Date.now() - 30 * 60_000).toISOString(),
    endedAt: new Date(Date.now() - 60_000).toISOString(),
    title: 'line items',
    source: 'jsonl',
    ...over,
  };
}

function renderRow(session: Session, hideProject = false): HTMLElement {
  render(
    <MemoryRouter>
      <SessionRow session={session} hideProject={hideProject} />
    </MemoryRouter>,
  );
  return screen.getByRole('link', { name: /line items/ });
}

afterEach(() => {
  cleanup();
  conflicts.ids.clear();
});

describe('SessionRow', () => {
  it("keeps ListRow's grid: glyph, title and sentence, age", () => {
    const row = renderRow(makeSession());
    expect(row.className).toContain('grid-cols-[18px_minmax(0,1fr)_auto]');
  });

  it('names the project in the all-projects list and drops it in a project list', () => {
    renderRow(makeSession());
    expect(screen.getByText('orders-api')).toBeTruthy();
    cleanup();
    renderRow(makeSession(), true);
    expect(screen.queryByText('orders-api')).toBeNull();
  });

  it('carries no flag under every threshold', () => {
    renderRow(makeSession({ contextTokens: 299_999, link: link(79.9) }));
    expect(screen.queryByTestId('row-flag')).toBeNull();
  });

  it('puts context first, over a conflict and a limit', () => {
    conflicts.ids.add(7);
    renderRow(makeSession({ contextTokens: 300_000, link: link(95) }));
    expect(screen.getAllByTestId('row-flag').map((el) => el.textContent)).toEqual(['⚠ context']);
  });

  it('puts a conflict over a limit', () => {
    conflicts.ids.add(7);
    renderRow(makeSession({ contextTokens: 120_000, link: link(95) }));
    expect(screen.getAllByTestId('row-flag').map((el) => el.textContent)).toEqual(['⚠ conflict']);
  });

  it('flags the limit alone at 80 %', () => {
    renderRow(makeSession({ link: link(80) }));
    expect(screen.getAllByTestId('row-flag').map((el) => el.textContent)).toEqual(['⚠ limit']);
  });
});
