// @vitest-environment jsdom
//
// The session rail's handoff card renders the brief through the generic markdown
// renderer — so the `## Rejected` section (verify-not-trust phase 19) shows up like
// every other section, with no component of its own. The card itself says the brief
// is ready, offers the resume command up front, and turns red when the context is fat.
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/pages/detail/HandoffCard.test.tsx`.

import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { HandoffCard } from './HandoffCard';

const brief = `# Handoff: ship the cache layer
## Non-goals — what this session deliberately did not do or ruled out
- no cache metrics
## Rejected
- Redis — chose SQLite table instead [decision D-1]
- LRU eviction — Evicts the least recently used entry when full; chose TTL only instead [interview D-2]
- … and 2 more

## Open risks & failed checks
- unknown
`;

vi.mock('../../api', () => ({
  fetchSessionHandoff: vi.fn(async () => ({
    markdown: brief,
    path: '/home/u/.swarmery/handoffs/abc.md',
    createdAt: '2026-10-22T20:00:00Z',
  })),
}));

afterEach(cleanup);

const NOW = Date.parse('2026-10-22T20:04:00Z');
const HANDOFF = { path: '/home/u/.swarmery/handoffs/abc.md', createdAt: '2026-10-22T20:00:00Z', contextTokens: 340_000 };

function card(hot: boolean, wrapper: (props: { children: ReactNode }) => JSX.Element = MemoryRouter): void {
  // HandoffCard reads the location hash (useLocation): it needs a router.
  render(<HandoffCard sessionId={7} handoff={HANDOFF} hot={hot} nowMs={NOW} />, { wrapper });
}

describe('HandoffCard', () => {
  it('says how old the brief is and offers the command before anything is expanded', () => {
    card(false);
    expect(screen.getByRole('button', { name: /handoff/i }).textContent).toContain('ready · 4m ago');
    expect(screen.getByText('The daemon wrote a brief for a fresh session.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'copy resume command' })).toBeTruthy();
  });

  it('turns red and names the context when it is fat', () => {
    card(true);
    expect(document.getElementById('handoff')?.className).toContain('border-red');
    expect(screen.getByText(/^Context is past 340K/)).toBeTruthy();
  });

  it('opens the brief on arrival at #handoff', async () => {
    card(false, ({ children }) => <MemoryRouter initialEntries={['/sessions/7#handoff']}>{children}</MemoryRouter>);
    expect(await screen.findByRole('heading', { name: 'Rejected' })).toBeTruthy();
  });

  it('renders the Rejected section between Non-goals and Open risks', async () => {
    card(false);
    fireEvent.click(screen.getByRole('button', { name: /handoff/i }));

    const rejected = await waitFor(() => screen.getByRole('heading', { name: 'Rejected' }));
    const headings = screen.getAllByRole('heading').map((h) => h.textContent ?? '');
    const at = headings.indexOf('Rejected');
    expect(headings[at - 1]).toMatch(/^Non-goals/);
    expect(headings[at + 1]).toMatch(/^Open risks/);

    // The section's lines render as list items under its heading, verbatim.
    const list = rejected.nextElementSibling;
    expect(list?.tagName).toBe('UL');
    const items = Array.from(list?.querySelectorAll('li') ?? []).map((li) => li.textContent);
    expect(items).toEqual([
      'Redis — chose SQLite table instead [decision D-1]',
      'LRU eviction — Evicts the least recently used entry when full; chose TTL only instead [interview D-2]',
      '… and 2 more',
    ]);
  });
});
