// @vitest-environment jsdom
//
// «Cache cold» chip + «continue from handoff» (migration 0122). The claims:
//
//   1. Nothing renders while the session is warm (cacheCold null).
//   2. With a brief, the action opens it: the session detail at #handoff — and
//      the click never reaches the clickable row around the chip.
//   3. Without one, the action asks the daemon (POST …/handoff): `generating`
//      shows a disabled "writing handoff…", `ready` opens the brief, an error
//      is shown inline.
//
// `npx vitest run src/pages/sessions/CacheColdBadge.test.tsx`.

import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Session } from '../../api/types';
import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { CacheColdBadge } from './CacheColdBadge';

const api = vi.hoisted(() => ({ requestSessionHandoff: vi.fn() }));
vi.mock('../../api', () => api);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function session(over: Partial<Session> = {}): Session {
  return {
    id: 7,
    projectId: 1,
    projectSlug: 'demo',
    projectName: 'demo',
    sessionUuid: 'uuid-7',
    model: 'claude-opus-5-5',
    gitBranch: 'main',
    cwd: '/work/demo',
    status: 'idle',
    startedAt: '2026-10-22T08:00:00Z',
    endedAt: '2026-10-22T11:00:00Z',
    title: 'fat session',
    source: 'jsonl',
    cacheCold: { since: '2026-10-22T12:00:00Z', kind: 'subscription', footprint: 212_000 },
    handoff: null,
    ...over,
  } as Session;
}

/** Shows where navigation landed. */
function Where(): JSX.Element {
  const loc = useLocation();
  return <output data-testid="where">{`${loc.pathname}${loc.hash}`}</output>;
}

function renderBadge(s: Session, onRow = vi.fn()): ReturnType<typeof vi.fn> {
  render(
    <MemoryRouter initialEntries={['/sessions']}>
      <Routes>
        <Route
          path="/sessions"
          element={
            <div role="link" tabIndex={0} onClick={onRow}>
              <CacheColdBadge session={s} />
            </div>
          }
        />
        <Route path="/sessions/:id" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
  return onRow;
}

describe('CacheColdBadge', () => {
  it('renders nothing while the cache is warm', () => {
    renderBadge(session({ cacheCold: null }));
    expect(screen.queryByText('cache cold')).toBeNull();
  });

  it('opens the existing brief without bubbling to the row', () => {
    const onRow = renderBadge(
      session({ handoff: { path: '/h/uuid-7.md', createdAt: '2026-10-22T10:00:00Z', contextTokens: 200_000 } }),
    );
    expect(screen.getByText('cache cold')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'continue from handoff' }));
    expect(screen.getByTestId('where').textContent).toBe('/sessions/7#handoff');
    expect(onRow).not.toHaveBeenCalled();
    expect(api.requestSessionHandoff).not.toHaveBeenCalled();
  });

  it('asks the daemon for a brief when there is none', async () => {
    api.requestSessionHandoff.mockResolvedValue({ status: 'generating' });
    const onRow = renderBadge(session());
    fireEvent.click(screen.getByRole('button', { name: 'continue from handoff' }));
    expect(api.requestSessionHandoff).toHaveBeenCalledWith(7);
    const busy = await screen.findByRole('button', { name: 'writing handoff…' });
    expect((busy as HTMLButtonElement).disabled).toBe(true);
    expect(onRow).not.toHaveBeenCalled();
  });

  it('opens the brief the daemon already had', async () => {
    api.requestSessionHandoff.mockResolvedValue({ status: 'ready', path: '/h/uuid-7.md' });
    renderBadge(session());
    fireEvent.click(screen.getByRole('button', { name: 'continue from handoff' }));
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/sessions/7#handoff'));
  });

  it('shows the refusal inline', async () => {
    api.requestSessionHandoff.mockRejectedValue(new Error('not a handoff candidate'));
    renderBadge(session());
    fireEvent.click(screen.getByRole('button', { name: 'continue from handoff' }));
    expect((await screen.findByRole('alert')).textContent).toBe('not a handoff candidate');
    expect(screen.getByRole('button', { name: 'continue from handoff' })).toBeTruthy();
  });

  it('names the billing channel and the footprint in the tooltip', () => {
    renderBadge(session({ cacheCold: { since: '2026-10-22T12:00:00Z', kind: 'api', footprint: 180_400 } }));
    const tip = screen.getByText('cache cold').getAttribute('data-tip') ?? '';
    expect(tip).toContain('API-key');
    expect(tip).toContain('180k-token');
  });
});
