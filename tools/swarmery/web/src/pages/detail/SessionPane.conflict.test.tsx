// @vitest-environment jsdom
//
// The "conflict" badge in a session's detail (mods-swarmery-link phase 6): a
// session holding a path another session holds too wears it in the alarm row under
// the state sentence; a session that does not, does not.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import type { SessionDetail } from '../../api/types';
import { SwarmConflictProvider } from '../../lib/swarm';
import { act, cleanup, render, screen, waitFor } from '../../test/render';
import { SessionPane } from './SessionPane';

vi.mock('../../api', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  fetchSession: vi.fn(async (id: string) => makeDetail(Number(id))),
  fetchSwarmClaims: vi.fn().mockResolvedValue({
    claims: [],
    conflicts: [{ path: '/r/a.go', sessions: [{ sessionId: 1, sessionUuid: 'u1' }, { sessionId: 2, sessionUuid: 'u2' }] }],
  }),
}));
vi.mock('../../lib/ws', () => ({ useLiveUpdates: () => undefined }));
vi.mock('../../lib/scope', () => ({
  useScope: () => ({ scope: null, scopeName: null, scopeProject: null, projects: [], setScope: () => undefined }),
}));

afterEach(cleanup);

function makeDetail(id: number): SessionDetail {
  return {
    id,
    projectId: 1,
    projectSlug: 'proj',
    projectName: 'proj',
    sessionUuid: `uuid-${String(id)}`,
    model: 'claude-opus-5-5',
    modelLast: 'claude-opus-5-5',
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'main',
    cwd: '/work/proj',
    status: 'active',
    startedAt: new Date(Date.now() - 60_000).toISOString(),
    endedAt: null,
    title: `session ${String(id)}`,
    source: 'both',
    costUsd: 1,
    turns: [],
    events: [],
    fileChanges: [],
    recovered: 0,
  };
}

function renderPane(id: number): void {
  render(
    <MemoryRouter>
      <SwarmConflictProvider>
        <SessionPane
          id={String(id)}
          listHref="/sessions"
          gate={undefined}
          onGateChanged={() => undefined}
          actions={{ current: {} }}
          now={null}
        />
      </SwarmConflictProvider>
    </MemoryRouter>,
  );
}

describe('conflict badge in the session detail', () => {
  it('shows on a session that shares a claimed path, naming the path, above the numbers', async () => {
    renderPane(1);
    const badge = await screen.findByTestId('swarm-conflict');
    expect(badge.getAttribute('aria-label')).toContain('/r/a.go');
    const numbers = screen.getByText('tokens');
    expect(badge.compareDocumentPosition(numbers) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('stays away from a session the conflict does not involve', async () => {
    renderPane(3);
    await screen.findByRole('button', { name: 'session 3' });
    await waitFor(() => expect(api.fetchSwarmClaims).toHaveBeenCalled());
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.queryByTestId('swarm-conflict')).toBeNull();
  });
});
