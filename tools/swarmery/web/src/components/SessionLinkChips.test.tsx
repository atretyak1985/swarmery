// @vitest-environment jsdom
//
// The link-mod chips (mods-swarmery-link phase 3, SC-5). The claims:
//
//   1. A session card with `link` shows context %, the top rate limit with its
//      reset time (every limit in the tip) and the mod cost with the
//      pricing-table cost in the tip.
//   2. Without `link` (explicit null, or an older daemon that omits the key)
//      the card renders exactly the markup it rendered before — nothing new.
//   3. The source marker carries an accessible name and a tip: "live from
//      session mod" while link.live, "derived from transcript" once stale.
//   4. A mod cost more than 10 % away from the pricing table is highlighted
//      with visible and screen-reader text; within 10 % it is not.
//   5. The Today command deck's Live now row renders the same chips.
//
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/components/SessionLinkChips.test.tsx`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import type { Session, SessionLink } from '../api/types';
import { fmtTime } from '../lib/format';
import { LiveNow } from '../pages/today/LiveNow';
import { cleanup, render, screen, within } from '../test/render';
import { SessionLinkChips } from './SessionLinkChips';

afterEach(cleanup);

/** Today 18:00 local — renders as a bare time (same local day). */
function todayAt(h: number): string {
  const d = new Date();
  d.setHours(h, 0, 0, 0);
  return d.toISOString();
}

const RESETS = todayAt(18);

function makeLink(over: Partial<SessionLink> = {}): SessionLink {
  return {
    status: 'working',
    seenAt: new Date().toISOString(),
    live: true,
    contextPct: 78,
    rateLimits: [
      { kind: 'seven_day', percentUsed: 12.5, resetsAt: null },
      { kind: 'five_hour', percentUsed: 64, resetsAt: RESETS },
    ],
    costUsd: 1.25,
    controllable: false,
    ...over,
  };
}

function makeSession(over: Partial<Session> = {}): Session {
  return {
    id: 42,
    projectId: 1,
    projectSlug: 'proj',
    projectName: 'proj',
    sessionUuid: 'uuid-42',
    model: 'claude-opus-5-5',
    modelLast: 'claude-opus-5-5',
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'main',
    cwd: '/work/proj',
    status: 'active',
    startedAt: new Date(Date.now() - 60_000).toISOString(),
    endedAt: null,
    title: 'wire the link chips',
    source: 'both',
    costUsd: 1.2,
    ...over,
  };
}

function renderCard(session: Session): HTMLElement {
  const { container } = render(
    <MemoryRouter>
      <SessionLinkChips session={session} />
    </MemoryRouter>,
  );
  return container;
}

describe('SessionLinkChips on the session card', () => {
  it('shows context %, the top rate limit with its reset, and the mod cost', () => {
    renderCard(makeSession({ link: makeLink() }));
    expect(screen.getByTestId('link-context').textContent).toBe('ctx 78 %');
    const rl = screen.getByTestId('link-rate-limit');
    expect(rl.textContent).toBe(`plan 64 % · resets ${fmtTime(RESETS)}`);
    // Every limit in the tip, not only the headline one.
    expect(rl.getAttribute('data-tip')).toContain('7-day window 12.5 % used');
    expect(rl.getAttribute('data-tip')).toContain(`5-hour window 64 % used, resets ${fmtTime(RESETS)}`);
    const cost = screen.getByTestId('link-cost');
    expect(cost.textContent).toBe('$1.25');
    expect(cost.getAttribute('data-tip')).toContain('pricing table: $1.20');
    expect(cost.getAttribute('data-tip')).toContain('for this process');
  });

  it('renders the unchanged card when the session has no link', () => {
    const withNull = renderCard(makeSession({ link: null })).innerHTML;
    cleanup();
    // An older daemon omits the key altogether — the pre-phase shape.
    const without = renderCard(makeSession()).innerHTML;
    expect(withNull).toBe(without);
    expect(screen.queryByTestId('session-link')).toBeNull();
    expect(screen.queryByTestId('link-source')).toBeNull();
  });

  it('marks the source: live from the mod, or derived from the transcript', () => {
    renderCard(makeSession({ link: makeLink({ live: true }) }));
    const live = screen.getByRole('img', { name: 'live from session mod' });
    expect(live.getAttribute('data-tip')).toBe('live from session mod');
    cleanup();
    renderCard(makeSession({ link: makeLink({ live: false }) }));
    const stale = screen.getByRole('img', { name: 'derived from transcript' });
    expect(stale.getAttribute('data-tip')).toBe('derived from transcript');
  });

  it('highlights a mod cost more than 10 % away from the pricing table', () => {
    renderCard(makeSession({ costUsd: 1.0, link: makeLink({ costUsd: 1.25 }) }));
    const cost = screen.getByTestId('link-cost');
    expect(cost.className).toContain('text-amber');
    expect(cost.textContent).toContain('Δ +25 %');
    expect(within(cost).getByText('differs from the pricing table by +25 %').className).toContain('sr-only');
  });

  it('does not highlight a cost within 10 % of the pricing table', () => {
    renderCard(makeSession({ costUsd: 1.2, link: makeLink({ costUsd: 1.25 }) }));
    const cost = screen.getByTestId('link-cost');
    expect(cost.className).not.toContain('text-amber');
    expect(cost.textContent).toBe('$1.25');
  });

  it('draws only the source marker when the mod reported no usage yet', () => {
    renderCard(makeSession({ link: makeLink({ contextPct: null, rateLimits: [], costUsd: null }) }));
    expect(screen.getByTestId('link-source')).toBeTruthy();
    expect(screen.queryByTestId('link-context')).toBeNull();
    expect(screen.queryByTestId('link-rate-limit')).toBeNull();
    expect(screen.queryByTestId('link-cost')).toBeNull();
  });
});

describe('SessionLinkChips on the command deck', () => {
  it('renders the chips in a Live now row, and nothing for a row without link', () => {
    render(
      <MemoryRouter>
        <LiveNow
          sessions={[makeSession({ link: makeLink() }), makeSession({ id: 43, sessionUuid: 'uuid-43', link: null })]}
          epics={[]}
          loading={false}
          now={Date.now()}
        />
      </MemoryRouter>,
    );
    const rows = screen.getAllByTestId('live-row');
    expect(within(rows[0]).getByTestId('link-context').textContent).toBe('ctx 78 %');
    expect(within(rows[0]).getByRole('img', { name: 'live from session mod' })).toBeTruthy();
    expect(within(rows[1]).queryByTestId('session-link')).toBeNull();
  });
});
