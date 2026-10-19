// @vitest-environment jsdom
//
// The "conflict" badge on a session card (mods-swarmery-link phase 6): a card
// whose session holds a path another session holds too wears it; others do not.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Session } from '../api/types';
import { SwarmConflictProvider } from '../lib/swarm';
import { cleanup, render, screen } from '../test/render';
import { SessionCard } from './SessionCard';

vi.mock('../api', async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  fetchSwarmClaims: vi.fn().mockResolvedValue({
    claims: [],
    conflicts: [{ path: '/r/a.go', sessions: [{ sessionId: 1, sessionUuid: 'u1' }, { sessionId: 2, sessionUuid: 'u2' }] }],
  }),
}));
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

afterEach(cleanup);

function makeSession(id: number): Session {
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
  };
}

describe('conflict badge on cards', () => {
  it('shows on the conflicted session only', async () => {
    render(
      <MemoryRouter>
        <SwarmConflictProvider>
          <SessionCard session={makeSession(1)} />
          <SessionCard session={makeSession(3)} />
        </SwarmConflictProvider>
      </MemoryRouter>,
    );
    const badges = await screen.findAllByTestId('swarm-conflict');
    expect(badges).toHaveLength(1);
    expect(badges[0]!.getAttribute('data-tip')).toContain('/r/a.go');
  });
});
