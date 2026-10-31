// @vitest-environment jsdom
//
// The context rail (Swarmery v4). The claims:
//
//   1. The six blocks from real data, with the design's empty states.
//   2. Lessons: active ones per touched area, none asked for when nothing was
//      touched, a failure said out loud rather than shown as "none matched".
//   3. The button collapses it to a strip with the handoff mark; the blocks stay
//      mounted behind desk:hidden, because below `desk` the rail never collapses.
//   4. Arriving at #handoff opens a collapsed rail.
//   5. `heavy` past $20.
//
// Runs with the rest of the web suite: `npm test`.

import { StrictMode } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as lessons from '../../api/lessons';
import type { SessionDetail } from '../../api/types';
import { reloadPanels } from '../../lib/panels';
import { cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { SessionRail } from './SessionRail';

vi.mock('../../api/lessons', () => ({ fetchLessons: vi.fn() }));
vi.mock('../../api', () => ({ fetchSessionHandoff: vi.fn(async () => ({ markdown: '## Brief', path: '/h.md', createdAt: '' })) }));

const NOW = Date.parse('2026-10-31T12:00:00Z');

function makeDetail(over: Partial<SessionDetail> = {}): SessionDetail {
  return {
    id: 1,
    projectId: 1,
    projectSlug: 'orders-api',
    projectName: 'orders-api',
    sessionUuid: 'uuid-1',
    model: 'claude-opus-5-5',
    modelLast: 'claude-opus-5-5',
    modelChanged: false,
    modelFellBack: false,
    gitBranch: 'main',
    cwd: '/work/repo',
    status: 'active',
    startedAt: '2026-10-31T11:00:00Z',
    endedAt: '2026-10-31T11:48:00Z',
    title: 'one',
    source: 'jsonl',
    turns: [],
    events: [],
    fileChanges: [],
    recovered: 0,
    ...over,
  } as SessionDetail;
}

const HANDOFF = { path: '/h.md', createdAt: '2026-10-31T11:56:00Z', contextTokens: 100_000 };
const LESSON = { id: 4, title: 'Dry-run migrations' } as lessons.Lesson;
const FILES = [{ filePath: '/work/repo/services/orders/a.go' }, { filePath: '/work/repo/web/src/b.ts' }] as SessionDetail['fileChanges'];

function renderRail(over: Partial<SessionDetail> = {}, costUsd: number | null = 3, path = '/sessions/1'): void {
  render(
    // StrictMode as main.tsx has it: effects run twice on mount.
    <StrictMode>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route
            path="*"
            element={<SessionRail detail={makeDetail(over)} state="needs" nowMs={NOW} costUsd={costUsd} />}
          />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
}

const rail = (): HTMLElement => screen.getByRole('complementary', { name: 'session context' });

beforeEach(() => {
  window.localStorage.clear();
  reloadPanels();
  vi.clearAllMocks();
  vi.mocked(lessons.fetchLessons).mockResolvedValue([]);
});

afterEach(cleanup);

describe('SessionRail', () => {
  it('shows every block, with its empty state where there is no data', async () => {
    renderRail();
    for (const label of ['swarm', 'linked', 'account', 'touches', 'lessons']) {
      expect(within(rail()).getByText(label)).toBeTruthy();
    }
    expect(screen.getByText('claude-opus-5-5')).toBeTruthy();
    expect(screen.getByText('paused on your decision')).toBeTruthy();
    expect(screen.getByText('no plan')).toBeTruthy();
    expect(screen.getByText('ad-hoc session')).toBeTruthy();
    expect(screen.getByText('default')).toBeTruthy();
    expect(screen.getByText('nothing touched yet')).toBeTruthy();
    expect(await screen.findByText('none matched')).toBeTruthy();
  });

  it('counts the files and lists their areas', () => {
    renderRail({ fileChanges: FILES });
    expect(screen.getByText('services/orders')).toBeTruthy();
    expect(screen.getByText('web/src')).toBeTruthy();
    expect(screen.getAllByText('1 file')).toHaveLength(2);
  });

  it('links a plan session to its phase', () => {
    renderRail({ planGroup: { taskId: 7, title: 'Rate limiter', role: 'phase', phaseId: 3, phaseSeq: 3, phaseName: 'x' } });
    expect(screen.getByRole('link', { name: /Rate limiter/ }).getAttribute('href')).toBe('/p/orders-api/plans?task=7&phase=3');
    expect(screen.queryByText('no plan')).toBeNull();
  });

  it('asks for the active lessons of each area and merges them', async () => {
    vi.mocked(lessons.fetchLessons).mockResolvedValue([LESSON]);
    renderRail({ fileChanges: FILES });
    const link = await screen.findByRole('link', { name: /Dry-run migrations/ });
    expect(link.getAttribute('href')).toBe('/learning?tab=lessons&item=lesson%3A4');
    expect(link.textContent).toContain('applies here');
    expect(lessons.fetchLessons).toHaveBeenCalledWith('active', 'services/orders');
    expect(lessons.fetchLessons).toHaveBeenCalledWith('active', 'web/src');
    expect(screen.getAllByRole('link', { name: /Dry-run migrations/ })).toHaveLength(1);
  });

  it('finds a lesson past the areas it lists, and lists the busiest', async () => {
    const dirs = ['a', 'b', 'c', 'd', 'e', 'f'];
    const fileChanges = [
      ...dirs.map((dir) => ({ filePath: `/work/repo/${dir}/x/1.ts` })),
      { filePath: '/work/repo/f/x/2.ts' },
    ] as SessionDetail['fileChanges'];
    let running = 0;
    let peak = 0;
    vi.mocked(lessons.fetchLessons).mockImplementation(async (_status, area) => {
      running += 1;
      peak = Math.max(peak, running);
      await Promise.resolve();
      running -= 1;
      return area === 'e/x' ? [LESSON] : [];
    });
    renderRail({ fileChanges });
    expect(await screen.findByRole('link', { name: /Dry-run migrations/ })).toBeTruthy();
    expect(lessons.fetchLessons).toHaveBeenCalledTimes(6);
    expect(peak).toBeLessThanOrEqual(4);
    expect(screen.getByText('f/x')).toBeTruthy();
    expect(screen.queryByText('e/x')).toBeNull();
  });

  it('asks for no lessons when the session touched nothing', async () => {
    renderRail();
    await screen.findByText('none matched');
    expect(lessons.fetchLessons).not.toHaveBeenCalled();
  });

  it('says so when lessons cannot be read', async () => {
    vi.mocked(lessons.fetchLessons).mockRejectedValue(new Error('down'));
    renderRail({ fileChanges: FILES });
    expect(await screen.findByText('lessons unavailable')).toBeTruthy();
    expect(screen.queryByText('none matched')).toBeNull();
  });

  it('marks a session past $20 as heavy', () => {
    renderRail({}, 21);
    expect(screen.getByText('heavy')).toBeTruthy();
    cleanup();
    renderRail({}, 20);
    expect(screen.queryByText('heavy')).toBeNull();
  });

  it('shows the handoff above the blocks, only when there is one', () => {
    renderRail({ handoff: HANDOFF });
    const card = document.getElementById('handoff') as HTMLElement;
    expect(card.compareDocumentPosition(screen.getByText('swarm')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    cleanup();
    renderRail();
    expect(document.getElementById('handoff')).toBeNull();
  });

  it('collapses on its button, keeping the blocks mounted for below desk', () => {
    renderRail({ handoff: HANDOFF });
    const block = screen.getByText('swarm');
    fireEvent.click(screen.getByRole('button', { name: 'collapse context · \\' }));
    expect(screen.getByRole('button', { name: 'expand context · \\' }).textContent).toBe('◂');
    expect(screen.getByText('swarm')).toBe(block);
    expect(document.getElementById('handoff')?.closest('.contents')?.className).toContain('desk:hidden');
    expect(rail().className).toContain('desk:flex-[0_0_28px]');
    expect(screen.getByText('context')).toBeTruthy();
    expect(screen.getByText('⇢')).toBeTruthy();
  });

  it('leaves the handoff mark out when there is no brief', () => {
    window.localStorage.setItem('swarmery.v4.panels', '{"rail":true}');
    reloadPanels();
    renderRail();
    expect(screen.getByText('context')).toBeTruthy();
    expect(screen.queryByText('⇢')).toBeNull();
  });

  it('opens a collapsed rail on arrival at #handoff', async () => {
    window.localStorage.setItem('swarmery.v4.panels', '{"rail":true}');
    reloadPanels();
    renderRail({ handoff: HANDOFF }, 3, '/sessions/1#handoff');
    await waitFor(() => expect(screen.getByRole('button', { name: 'collapse context · \\' })).toBeTruthy());
    expect(await screen.findByRole('heading', { name: 'Brief' })).toBeTruthy();
  });

  it('brings the card into view only once the rail shows it', async () => {
    const seen: (string | undefined)[] = [];
    const scroll = vi.fn(function record(this: Element) {
      seen.push(this.closest('.contents')?.className);
    });
    Element.prototype.scrollIntoView = scroll;
    window.localStorage.setItem('swarmery.v4.panels', '{"rail":true}');
    reloadPanels();
    renderRail({ handoff: HANDOFF }, 3, '/sessions/1#handoff');
    await screen.findByRole('heading', { name: 'Brief' });
    expect(scroll).toHaveBeenCalled();
    expect(seen.every((className) => className === 'contents')).toBe(true);
  });

  it('scrolls the brief inside the card, so the sticky rail keeps its foot in reach', async () => {
    renderRail({ handoff: HANDOFF }, 3, '/sessions/1#handoff');
    const brief = await screen.findByRole('region', { name: 'handoff brief' });
    expect(brief.className).toContain('max-h-[50vh]');
    expect(brief.className).toContain('overflow-y-auto');
    expect(brief.contains(await screen.findByRole('heading', { name: 'Brief' }))).toBe(true);
  });

  it('stays collapsed on #handoff when the session has no brief', () => {
    window.localStorage.setItem('swarmery.v4.panels', '{"rail":true}');
    reloadPanels();
    renderRail({}, 3, '/sessions/1#handoff');
    expect(screen.getByRole('button', { name: 'expand context · \\' })).toBeTruthy();
  });
});
